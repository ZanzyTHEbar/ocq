package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"ocq/internal/core"
)

type observationWireSlot struct {
	Slot    string `json:"slot"`
	Kind    string `json:"kind"`
	Status  string `json:"status"`
	Cursor  string `json:"cursor,omitempty"`
	HasMore bool   `json:"has_more"`
	Data    any    `json:"data"`
}

type observationWireSnapshot struct {
	Protocol      string                     `json:"protocol"`
	Status        string                     `json:"status"`
	QueueIdentity string                     `json:"queue_identity"`
	Schema        int                        `json:"schema"`
	Revision      string                     `json:"revision"`
	ReadAt        string                     `json:"read_at"`
	Consistency   string                     `json:"consistency"`
	StoreContext  string                     `json:"store_context"`
	BridgeContext string                     `json:"bridge_context"`
	Slots         []observationWireSlot      `json:"slots"`
	Log           *core.ObservationLogOutput `json:"log,omitempty"`
	Work          any                        `json:"work,omitempty"`
	Main          any                        `json:"main,omitempty"`
	Clients       any                        `json:"clients,omitempty"`
}

func observationCommands() []*cobra.Command {
	return []*cobra.Command{
		{Use: "view-snapshot REQUEST_JSON", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			req, err := core.DecodeObservationRequest([]byte(args[0]))
			if err != nil {
				return err
			}
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			ctx, deadlineCancel := context.WithTimeout(ctx, 5*time.Second)
			defer deadlineCancel()
			binding, err := observationBinding(ctx, cmd)
			if err != nil {
				return err
			}
			if req.BridgeContext == "" {
				if req.QueueIdentity != "" || req.StoreContext != "" || (req.Log != nil && req.Log.FileIdentity != "") {
					return errors.New("continuation requires exact bridge_context")
				}
			} else if req.BridgeContext != binding.ObservationContext || req.StoreContext != binding.ObservationStoreContext || req.QueueIdentity != binding.Queue {
				return writeObservation(cmd, observationReset(binding))
			}
			req.QueueIdentity, req.StoreContext = binding.Queue, binding.ObservationStoreContext
			result, err := core.ObserveSnapshot(ctx, os.Getenv("OCQ_STATE_DIR"), req)
			if err != nil {
				return err
			}
			wire, err := observationWire(result)
			if err != nil {
				return err
			}
			after, err := observationBinding(ctx, cmd)
			if err != nil {
				return err
			}
			if after.ObservationContext != binding.ObservationContext {
				return writeObservation(cmd, observationReset(after))
			}
			wire.BridgeContext = binding.ObservationContext
			return writeObservation(cmd, wire)
		}},
		{Use: "watch-wait REQUEST_JSON", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			req, err := core.DecodeObservationWaitRequest([]byte(args[0]))
			if err != nil {
				return err
			}
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			ctx, deadlineCancel := context.WithTimeout(ctx, 20*time.Second)
			defer deadlineCancel()
			binding, err := observationBinding(ctx, cmd)
			if err != nil {
				return fmt.Errorf("watch initial descriptor: %w", err)
			}
			if req.BridgeContext == "" {
				return errors.New("watch requires exact bridge_context")
			}
			if req.BridgeContext != binding.ObservationContext || req.StoreContext != binding.ObservationStoreContext || req.QueueIdentity != binding.Queue {
				return writeObservation(cmd, observationReset(binding))
			}
			// Core stops notification blocking before completion is due. Its fresh
			// reads and this final descriptor check share ONE 20-second deadline;
			// do not cancel a valid read early to create a separate 100 ms budget.
			result, err := core.ObserveWait(ctx, os.Getenv("OCQ_STATE_DIR"), req)
			if err != nil {
				return fmt.Errorf("watch observation: %w", err)
			}
			after, err := observationBinding(ctx, cmd)
			if err != nil {
				return fmt.Errorf("watch final descriptor: %w", err)
			}
			if after.ObservationContext != binding.ObservationContext {
				return writeObservation(cmd, observationReset(after))
			}
			return writeObservation(cmd, struct {
				core.ObservationWaitOutput
				BridgeContext string `json:"bridge_context"`
			}{result, binding.ObservationContext})
		}},
	}
}

// Never fall back to serializing an unfamiliar core type. Existing reviewed
// project converters remain the sole allowlist for private core detail views.
func observationWire(s core.ObservationSnapshot) (observationWireSnapshot, error) {
	out := observationWireSnapshot{Protocol: s.Protocol, Status: s.Status, QueueIdentity: s.QueueIdentity, Schema: s.Schema, Revision: s.Revision, ReadAt: s.ReadAt, Consistency: s.Consistency, StoreContext: s.StoreContext, Log: s.Log, Slots: []observationWireSlot{}}
	if s.Work != nil {
		var err error
		out.Work, err = workProjection(s.Work)
		if err != nil {
			return out, err
		}
		out.Main, err = workProjection(s.Main)
		if err != nil {
			return out, err
		}
		out.Clients, err = workProjection(s.Clients)
		if err != nil {
			return out, err
		}
	}
	for _, slot := range s.Slots {
		var safe any
		if slot.Status == "ready" {
			switch data := slot.Data.(type) {
			case core.Board:
				safe = boardView(data)
			case core.ProjectJobDetailOutput:
				safe = projectDetail(data)
			case core.ProjectResultsOutput:
				safe = projectResults(data)
			case core.ProjectInboxOutput:
				messages := []inboxMessage{}
				for _, m := range data.Messages {
					messages = append(messages, inboxMessage{m, strconv.FormatInt(m.Sequence, 10)})
				}
				safe = struct {
					core.ProjectInboxOutput
					Messages []inboxMessage `json:"messages"`
				}{data, messages}
			case core.ProjectInboxMessage:
				safe = inboxMessage{data, strconv.FormatInt(data.Sequence, 10)}
			case []core.ProjectSummary, []core.ProjectLane, []core.QueueNotification, core.QueueJobSummary, core.QueueJobsOutput, core.QueueNotification, string:
				safe = data
			default:
				return out, errors.New("unsupported observation projection")
			}
		}
		// Preserve decimal precision, including the older nested project views.
		raw, err := json.Marshal(safe)
		if err != nil {
			return out, err
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&safe); err != nil {
			return out, err
		}
		observationDecimalFields(safe)
		out.Slots = append(out.Slots, observationWireSlot{slot.Slot, slot.Kind, slot.Status, slot.Cursor, slot.HasMore, safe})
	}
	return out, nil
}

func observationBinding(ctx context.Context, cmd *cobra.Command) (modCompatibility, error) {
	space, err := cmd.Flags().GetString("mod-space")
	if err != nil {
		return modCompatibility{}, err
	}
	folder, err := cmd.Flags().GetString("mod-folder")
	if err != nil {
		return modCompatibility{}, err
	}
	return compatibilityContext(ctx, space, folder)
}

func observationReset(binding modCompatibility) any {
	return struct {
		Protocol      string `json:"protocol"`
		Status        string `json:"status"`
		ResetScope    string `json:"reset_scope"`
		QueueIdentity string `json:"queue_identity"`
		StoreContext  string `json:"store_context"`
		BridgeContext string `json:"bridge_context"`
		Schema        int    `json:"schema"`
	}{core.ObservationProtocol, "reset", "context", binding.Queue, binding.ObservationStoreContext, binding.ObservationContext, binding.ObservationSchema}
}

func observationDecimalFields(value any) {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			switch key {
			case "sequence", "after_sequence", "next_sequence", "binding_revision", "revision":
				if n, ok := item.(json.Number); ok {
					v[key] = n.String()
					continue
				}
			}
			observationDecimalFields(item)
		}
	case []any:
		for _, item := range v {
			observationDecimalFields(item)
		}
	}
}

func writeObservation(cmd *cobra.Command, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(raw)+1 > core.ObservationResponseBytes {
		// No partial projections and no implicit fallback to legacy unbounded reads.
		raw = []byte(`{"protocol":"ocq-observe-v1","status":"limit_exceeded"}`)
	}
	raw = append(raw, '\n')
	_, err = cmd.OutOrStdout().Write(raw)
	return err
}
