package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/spf13/cobra"
	"ocq/internal/core"
)

const pushProtocol = "ocq-push-v1"

type pushEndpoint struct {
	workflow                    *workflowRegistry
	hc                          *hcBinding
	allowActions                bool
	state, space, folder, token string
	ctx                         context.Context
	slots                       chan struct{}
	sessions                    sync.WaitGroup
	mu                          sync.Mutex
	stopping                    bool
	runtime                     bool
}

// Credentials are never command arguments, URLs, responses or log fields.
func pushSecret(file string) ([]byte, error) {
	f, err := os.OpenFile(file, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("private token file unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || int(st.Uid) != os.Getuid() || info.Size() != 65 {
		return nil, errors.New("token must be owned regular0600 file containing64 hex digits and newline")
	}
	raw := make([]byte, 65)
	if _, err = f.ReadAt(raw, 0); err != nil {
		return nil, err
	}
	if raw[64] != '\n' {
		return nil, errors.New("invalid token file")
	}
	if _, err = hex.DecodeString(string(raw[:64])); err != nil {
		return nil, errors.New("invalid token file")
	}
	return raw[:64], nil
}

func pushCommands() []*cobra.Command {
	token := &cobra.Command{Use: "push-token NEW_PRIVATE_FILE", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		file := args[0]
		if !filepath.IsAbs(file) {
			return errors.New("absolute token path required")
		}
		parent, err := os.Lstat(filepath.Dir(file))
		if err != nil || !parent.IsDir() || parent.Mode().Perm() != 0700 {
			return errors.New("token parent must be existing private0700 directory")
		}
		if st, ok := parent.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
			return errors.New("token parent must be owned by the current user")
		}
		var secret [32]byte
		if _, err = rand.Read(secret[:]); err != nil {
			return err
		}
		f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err = f.WriteString(hex.EncodeToString(secret[:]) + "\n"); err != nil {
			return err
		}
		if err = f.Sync(); err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"token_file": file, "created": true, "secret_printed": false})
	}}
	var state, listen, tokenFile, cert, key, hcFile, workflowFile, advertised string
	var allowActions, machineRuntime bool
	serve := &cobra.Command{Use: "push-serve", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !filepath.IsAbs(state) {
			return errors.New("explicit absolute --queue-dir for an EXISTING store required")
		}
		canonical, err := filepath.EvalSymlinks(state)
		if err != nil || canonical != state {
			return errors.New("canonical existing queue required")
		}
		host, _, err := net.SplitHostPort(listen)
		if err != nil {
			return errors.New("explicit listen host:port required")
		}
		loopback := net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
		if (cert == "") != (key == "") || !loopback && cert == "" {
			return errors.New("non-loopback listen requires explicit TLS certificate and key")
		}
		secret, err := pushSecret(tokenFile)
		if err != nil {
			return err
		}
		defer clear(secret)
		// Scoped to this explicitly started foreground endpoint process only.
		if err = os.Setenv("OCQ_STATE_DIR", state); err != nil {
			return err
		}
		space, _ := cmd.Flags().GetString("mod-space")
		folder, _ := cmd.Flags().GetString("mod-folder")
		binding, err := compatibilityContext(cmd.Context(), space, folder)
		if err != nil || binding.Queue == "" || !pushObservationSchema(binding.ObservationSchema) {
			return errors.New("push requires an existing private schema6/7/8 store and explicit mod-space/mod-folder")
		}
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		endpoint := &pushEndpoint{state: state, space: space, folder: folder, token: string(secret), ctx: ctx, slots: make(chan struct{}, 8)}
		endpoint.allowActions = allowActions
		endpoint.runtime = machineRuntime
		if hcFile != "" {
			if !allowActions && !machineRuntime {
				return errors.New("private HC binding requires explicit --allow-actions")
			}
			endpoint.hc, err = loadHCBinding(hcFile, space, folder, binding.Queue)
			if err != nil {
				return err
			}
			check, e := endpoint.hc.connect(ctx)
			if e != nil {
				return e
			}
			check.Close()
		}
		if workflowFile != "" {
			if !allowActions || endpoint.hc == nil {
				return errors.New("workflow requires explicit actions and pinned private HC")
			}
			registrySpace, registryFolder := space, folder
			if machineRuntime {
				registrySpace, registryFolder = "", ""
			}
			endpoint.workflow, err = loadWorkflowRegistry(workflowFile, registrySpace, registryFolder, binding.Queue)
			if err != nil {
				return err
			}
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/ocq", endpoint.accept)
		server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8192}
		listener, err := net.Listen("tcp", listen)
		if err != nil {
			return err
		}
		defer listener.Close()
		if machineRuntime {
			if endpoint.hc == nil {
				return errors.New("machine runtime requires an existing verified HC binding")
			}
			address := advertised
			localOnly := address == ""
			if localOnly {
				address = "ws://" + listener.Addr().String() + "/ocq"
				if cert != "" {
					address = "wss://" + listener.Addr().String() + "/ocq"
				}
			}
			retire, err := endpoint.publishRuntime(address, localOnly)
			if err != nil {
				return err
			}
			defer retire()
		}
		scheme := "ws"
		if cert != "" {
			scheme = "wss"
		}
		if err = json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"protocol": pushProtocol, "endpoint": scheme + "://" + listener.Addr().String() + "/ocq", "queue_identity": binding.Queue, "read_only": !allowActions}); err != nil {
			return err
		}
		go func() { <-ctx.Done(); _ = server.Close() }()
		if cert != "" {
			err = server.ServeTLS(listener, cert, key)
		} else {
			err = server.Serve(listener)
		}
		cancel()
		endpoint.mu.Lock()
		endpoint.stopping = true
		endpoint.mu.Unlock()
		endpoint.sessions.Wait()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}}
	serve.Flags().StringVar(&state, "queue-dir", "", "Existing private queue (never initialized/migrated)")
	serve.Flags().StringVar(&listen, "listen", "127.0.0.1:0", "Foreground listener; ws only on literal loopback")
	serve.Flags().StringVar(&tokenFile, "token-file", "", "Owned0600 first-frame credential file")
	serve.Flags().StringVar(&cert, "tls-cert", "", "Explicit TLS certificate; no auto provisioning")
	serve.Flags().StringVar(&key, "tls-key", "", "Explicit TLS private key")
	serve.Flags().BoolVar(&allowActions, "allow-actions", false, "Explicitly allow supported reviewed OCQ actions; no native launch, dispatcher or agent reconciliation")
	serve.Flags().StringVar(&hcFile, "hc-binding-file", "", "Explicit private existing HC socket/owner/core/scope pins; never starts HC")
	serve.Flags().StringVar(&workflowFile, "workflow-registry-file", "", "Owner-provisioned exact workflow profiles and bounded foreground dispatcher identity")
	serve.Flags().BoolVar(&machineRuntime, "runtime", false, "Publish one machine-local runtime for Herdr discovery; no route provisioning")
	serve.Flags().StringVar(&advertised, "advertise-endpoint", "", "Owner-provisioned client-reachable wss endpoint; no forwarding or listener exposure")
	return []*cobra.Command{token, serve, runtimeDiscoverCommand()}
}

func (e *pushEndpoint) accept(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	if e.stopping {
		e.mu.Unlock()
		http.Error(w, "endpoint stopping", http.StatusServiceUnavailable)
		return
	}
	e.sessions.Add(1)
	e.mu.Unlock()
	defer e.sessions.Done()
	// Native client only. Browser origins are never admitted, even same-host.
	if r.Method != "GET" || r.URL.RawQuery != "" || len(r.Header.Values("Origin")) != 0 {
		http.Error(w, "native WebSocket required", http.StatusForbidden)
		return
	}
	select {
	case e.slots <- struct{}{}:
	default:
		http.Error(w, "connection limit", http.StatusServiceUnavailable)
		return
	}
	defer func() { <-e.slots }()
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(core.ObservationRequestBytes)
	ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
	frame, err := readPush(ctx, c)
	cancel()
	if err != nil || frame.Type != "auth" || frame.Protocol != pushProtocol || len(frame.Token) != 64 ||
		subtle.ConstantTimeCompare([]byte(frame.Token), []byte(e.token)) != 1 ||
		(!e.runtime && (frame.Space != e.space || frame.Folder != e.folder || frame.Core != "" || frame.Queue != "" || frame.Peer != "")) ||
		(e.runtime && (frame.Space != "" || frame.Folder != "" || frame.Core != e.hc.Core || frame.Queue == "" || len(frame.Peer) > 128 || strings.ContainsAny(frame.Peer, "/\r\n\x00"))) ||
		frame.ID != "" || len(frame.Request) != 0 {
		_ = c.Close(websocket.StatusPolicyViolation, "authentication or explicit scope refused")
		return
	}
	if e.runtime {
		b, checkErr := compatibilityContext(e.ctx, e.space, e.folder)
		if checkErr != nil || b.Queue != frame.Queue {
			_ = c.Close(websocket.StatusPolicyViolation, "runtime identity refused")
			return
		}
	}
	frame.Token = ""
	e.session(c, frame.Peer)
}

func pushWrite(ctx context.Context, c *websocket.Conn, value any) error {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > core.ObservationResponseBytes+16384 {
		return errors.New("push response bound exceeded")
	}
	deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return c.Write(deadline, websocket.MessageText, raw)
}

func pushNonce() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(raw[:])
}

// Retain an explicit allowlist; do not turn the socket into an arbitrary bridge
// argv dispatcher or silently transport a mutation without its original fences.
var pushControls = []string{"subscribe", "unsubscribe", "ping", "close"}

func pushObservationSchema(version int) bool { return version == 6 || version == 7 || version == 8 }

func validPushID(s string) bool {
	return len(s) > 0 && len(s) <= 16 && strings.Trim(s, "0123456789") == "" && s[0] != '0'
}
