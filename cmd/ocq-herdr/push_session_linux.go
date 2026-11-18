package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"unicode/utf8"

	"github.com/coder/websocket"
	"ocq/internal/core"
)

type pushFrame struct {
	Type     string          `json:"type"`
	Protocol string          `json:"protocol,omitempty"`
	Token    string          `json:"token,omitempty"`
	Space    string          `json:"space,omitempty"`
	Folder   string          `json:"folder,omitempty"`
	ID       string          `json:"id,omitempty"`
	Request  json.RawMessage `json:"request,omitempty"`
	Core     string          `json:"core_id,omitempty"`
	Queue    string          `json:"queue_identity,omitempty"`
	Peer     string          `json:"peer,omitempty"`
}

func readPush(ctx context.Context, c *websocket.Conn) (pushFrame, error) {
	var f pushFrame
	kind, raw, err := c.Read(ctx)
	if err != nil {
		return f, err
	}
	if kind != websocket.MessageText || len(raw) > core.ObservationResponseBytes || !utf8.Valid(raw) {
		return f, errors.New("bounded text frame required")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(&f); err != nil {
		return f, err
	}
	if d.Decode(new(any)) != io.EOF {
		return f, errors.New("single JSON frame required")
	}
	if f.Type != "command" && len(raw) > core.ObservationRequestBytes {
		return f, errors.New("control frame bound exceeded")
	}
	return f, nil
}

func (e *pushEndpoint) session(c *websocket.Conn, peer string) {
	c.SetReadLimit(core.ObservationResponseBytes)
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	if e.runtime {
		if peer == "" {
			peer = "local"
		}
		ctx = context.WithValue(ctx, runtimePeerKey{}, peer)
	}
	binding, err := compatibilityContext(ctx, e.space, e.folder)
	if err != nil || !pushObservationSchema(binding.ObservationSchema) || binding.Queue == "" {
		return
	}
	connection := pushNonce()
	if connection == "" {
		return
	}
	// Install all relevant watches BEFORE welcome/first snapshot.
	watch, err := core.WatchObservationEvents(ctx, e.state)
	if err != nil {
		_ = c.Close(websocket.StatusPolicyViolation, "filesystem notifications unavailable")
		return
	}
	defer watch.Close()
	welcome := map[string]any{"protocol": pushProtocol, "type": "welcome", "connection_id": connection, "descriptor": binding, "commands": append(append([]string{}, pushControls...), "command"), "mutations": e.allowActions}
	if e.workflow != nil && e.workflow.peer() != "local" {
		welcome["execution_peer"] = e.workflow.peer()
		welcome["execution_core_id"] = e.hc.Core
	}
	if e.runtime {
		welcome["runtime"] = map[string]any{"protocol": runtimeProtocol, "core_id": e.hc.Core, "queue_identity": binding.Queue}
		welcome["execution_peer"] = peer
		welcome["execution_core_id"] = e.hc.Core
	}
	if pushWrite(ctx, c, welcome) != nil {
		return
	}
	frames := make(chan pushFrame, 1)
	failed := make(chan struct{})
	go func() {
		defer close(failed)
		for {
			f, err := readPush(ctx, c)
			if err != nil {
				return
			}
			select {
			case frames <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() { cancel(); _ = c.CloseNow(); <-failed }()
	var req *core.ObservationRequest
	id := ""
	var sequence uint64
	var lastID uint64
	lastProjection := ""
	logBytes := 0
	project := func() error {
		if req == nil {
			return nil
		}
		current, err := compatibilityContext(ctx, e.space, e.folder)
		if err != nil || current.ObservationContext != binding.ObservationContext || current.Queue != binding.Queue {
			return errors.New("context changed")
		}
		result, err := core.ObserveSnapshot(ctx, e.state, *req)
		if err != nil {
			return err
		}
		if result.Log != nil && result.Log.Status == "ready" && len(result.Log.Bytes) > 65536-logBytes {
			result.Log.Bytes = result.Log.Bytes[:65536-logBytes]
			offset, _ := strconv.ParseInt(result.Log.Offset, 10, 64)
			result.Log.NextOffset = strconv.FormatInt(offset+int64(len(result.Log.Bytes)), 10)
			result.Log.Cursor.Offset = result.Log.NextOffset
			result.Log.EOF = false
		}
		wire, err := observationWire(result)
		if err != nil {
			return err
		}
		wire.BridgeContext = binding.ObservationContext
		if wire.Status != "ready" || !pushObservationSchema(wire.Schema) || wire.QueueIdentity != binding.Queue || wire.StoreContext != binding.ObservationStoreContext {
			return errors.New("snapshot context unavailable")
		}
		after, err := compatibilityContext(ctx, e.space, e.folder)
		if err != nil || after.ObservationContext != binding.ObservationContext {
			return errors.New("context changed across snapshot")
		}
		raw, err := json.Marshal(wire)
		if err != nil || len(raw) > core.ObservationResponseBytes {
			return errors.New("snapshot bound exceeded")
		}
		// Inotify is a hint; suppress unchanged/uncommitted/duplicate hints.
		// Read timestamps are not changes to authoritative projected state.
		var logical any
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if d.Decode(&logical) != nil {
			return errors.New("projection unavailable")
		}
		stripPushReadTimes(logical)
		stable, _ := json.Marshal(logical)
		hash := compatHash(stable)
		if hash == lastProjection {
			return nil
		}
		lastProjection = hash
		sequence++
		if err := pushWrite(ctx, c, map[string]any{"protocol": pushProtocol, "type": "snapshot", "connection_id": connection, "id": id, "sequence": strconv.FormatUint(sequence, 10), "snapshot": wire}); err != nil {
			return err
		}
		if result.Log != nil && result.Log.Status == "ready" {
			cursor := result.Log.Cursor
			req.Log = &cursor
			logBytes += len(result.Log.Bytes)
		}
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-failed:
			return
		case <-watch.Errors:
			_ = c.Close(websocket.StatusPolicyViolation, "filesystem watch lost; reconnect explicitly")
			return
		case <-watch.Changes:
			if err := project(); err != nil {
				_ = c.Close(websocket.StatusPolicyViolation, "projection reset; reconnect explicitly")
				return
			}
		case f := <-frames:
			n, err := strconv.ParseUint(f.ID, 10, 64)
			if !validPushID(f.ID) || err != nil || n <= lastID || f.Protocol != "" || f.Token != "" || f.Space != "" || f.Folder != "" || f.Core != "" || f.Queue != "" || f.Peer != "" {
				_ = c.Close(websocket.StatusPolicyViolation, "invalid control identity")
				return
			}
			lastID = n
			switch f.Type {
			case "command":
				current, err := compatibilityContext(ctx, e.space, e.folder)
				if err != nil || current.ObservationContext != binding.ObservationContext {
					return
				}
				// Transport receipt only, never durable action/execution proof.
				// Once received, finish the original guarded operation even if
				// its reply is lost; recovery uses its existing exact journal.
				if pushWrite(ctx, c, map[string]any{"protocol": pushProtocol, "type": "command-received", "connection_id": connection, "id": f.ID}) != nil {
					return
				}
				reply := e.command(ctx, connection, f.Request)
				if pushWrite(ctx, c, map[string]any{"protocol": pushProtocol, "type": "command-result", "connection_id": connection, "id": f.ID, "result": reply}) != nil {
					return
				}
			case "subscribe":
				next, err := core.DecodeObservationRequest(f.Request)
				if err != nil || next.QueueIdentity != binding.Queue || next.StoreContext != binding.ObservationStoreContext || next.BridgeContext != binding.ObservationContext {
					_ = c.Close(websocket.StatusPolicyViolation, "invalid pinned subscription")
					return
				}
				req = &next
				id = f.ID
				lastProjection = ""
				logBytes = 0
				if err = project(); err != nil {
					_ = c.Close(websocket.StatusPolicyViolation, "projection unavailable")
					return
				}
			case "unsubscribe":
				if len(f.Request) != 0 {
					return
				}
				req = nil
				if pushWrite(ctx, c, map[string]any{"protocol": pushProtocol, "type": "unsubscribed", "id": f.ID, "connection_id": connection}) != nil {
					return
				}
			case "ping":
				if len(f.Request) != 0 {
					return
				}
				if pushWrite(ctx, c, map[string]any{"protocol": pushProtocol, "type": "pong", "id": f.ID, "connection_id": connection}) != nil {
					return
				}
			case "close":
				_ = c.Close(websocket.StatusNormalClosure, "explicit disconnect")
				return
			default:
				_ = c.Close(websocket.StatusPolicyViolation, "command not supported; mutations disabled")
				return
			}
		}
		// Continue at most one bounded 64 KiB log window without client read
		// renewals. Further bytes require an explicit new selected window.
		if req != nil && req.Log != nil {
			if logBytes >= 65536 {
				req.Log = nil
			} else if size, err := strconv.ParseInt(req.Log.ObservedSize, 10, 64); err == nil {
				offset, _ := strconv.ParseInt(req.Log.Offset, 10, 64)
				if offset < size {
					select {
					case <-ctx.Done():
						return
					default:
					}
					if err := project(); err != nil {
						return
					}
				}
			}
		}
	}
}

func stripPushReadTimes(value any) {
	switch v := value.(type) {
	case map[string]any:
		delete(v, "read_at")
		delete(v, "observed_at")
		for _, item := range v {
			stripPushReadTimes(item)
		}
	case []any:
		for _, item := range v {
			stripPushReadTimes(item)
		}
	}
}
