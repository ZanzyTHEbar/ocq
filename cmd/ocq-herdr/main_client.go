package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"ocq/internal/core"
)

var clientDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// HC 0.1.21 IDs use consonant/base-30 suffixes (e.g. t1b), not decimal.
var nativeTerminalID = regexp.MustCompile(`^t[1-9][0-9bcdfghjkmnpqrstvwxyz]*$`)

// Workflow authoring is a separate versioned, owner-registry-bound capability.
func mainClientCommands() []*cobra.Command {
	commands := []*cobra.Command{{Use: "hc-binding", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		b, _ := cmd.Context().Value(hcBindingKey{}).(*hcBinding)
		if b == nil {
			return errors.New("no explicit private HC binding")
		}
		conn, err := b.connect(cmd.Context())
		if err != nil {
			return err
		}
		conn.Close()
		return writeWork(cmd, map[string]any{"binding": b, "binding_sha256": b.digest(), "validation": "existing_socket_peer_and_core_verified", "launch": false})
	}}, {Use: "hc-binding-check FILE SPACE FOLDER QUEUE", Args: cobra.ExactArgs(4), RunE: func(cmd *cobra.Command, a []string) error {
		b, err := loadHCBinding(a[0], a[1], a[2], a[3])
		if err != nil {
			return err
		}
		conn, err := b.connect(cmd.Context())
		if err != nil {
			return err
		}
		conn.Close()
		return writeWork(cmd, map[string]any{"binding_sha256": b.digest(), "validation": "existing_socket_peer_and_core_verified", "launch": false})
	}}}
	for _, action := range []string{"main-list", "main-status", "main-recover", "main-preview", "main-start", "client-list", "client-status", "client-prepare", "client-attention-prepare-v1", "client-open", "client-verify"} {
		action := action
		n, use := 3, action+" SPACE QUEUE PROJECT"
		if action != "main-list" && action != "client-list" {
			n++
			use += " POINTER"
		}
		contextual := action == "main-preview" || action == "main-start" || action == "client-prepare" || action == "client-attention-prepare-v1" || action == "client-open" || action == "client-verify"
		if contextual {
			n += 4
			use += " PEER FOLDER STORE BRIDGE_IMAGE"
		}
		if action == "main-start" {
			n++
			use += " PREVIEW_SHA256"
		}
		if action == "client-prepare" || action == "client-attention-prepare-v1" {
			n += 2
			use += " worker|integrator WORK_REQUEST_OR_EMPTY"
		}
		commands = append(commands, &cobra.Command{Use: use, Args: cobra.ExactArgs(n), RunE: func(cmd *cobra.Command, a []string) error {
			dir, err := existingBridgeStateDir()
			if err != nil {
				return err
			}
			if !workSpace.MatchString(a[0]) || (n > 3 && !workPointer.MatchString(a[3])) {
				return errors.New("invalid Main/client scope or pointer")
			}
			r, err := core.OpenReader(dir)
			if err != nil {
				return err
			}
			queue, store, err := r.WorkQueueContext()
			if closeErr := r.Close(); err == nil {
				err = closeErr
			}
			if err != nil || queue != a[1] {
				return errors.New("Main/client queue context changed")
			}
			var c core.WorkActionContext
			if contextual {
				b, e := compatibility(a[0], a[5])
				observation, flagErr := cmd.Flags().GetString("work-observation-context")
				if e != nil || flagErr != nil || a[4] == "" || len(a[4]) > 128 || strings.ContainsAny(a[4], "\r\n\x00") ||
					b.Queue != queue || b.ObservationStoreContext != store || store != a[6] || b.Image != a[7] || observation != b.ObservationContext {
					return errors.New("Main/client bridge/store/incarnation changed")
				}
				c = core.WorkActionContext{Protocol: core.MainProtocol, Peer: a[4], Space: a[0], Folder: a[5], QueueIdentity: queue, StoreContext: store, ImageSHA256: b.Image, ImageRole: "bridge", Action: "start"}
				if strings.HasPrefix(action, "client-") {
					c.Protocol, c.Action = core.ClientProtocol, "client-open"
				}
			}
			var out any
			switch action {
			case "main-list":
				out, err = core.ListProjectMain(dir, a[2])
			case "main-status":
				out, err = core.InspectProjectMain(dir, a[2], a[3])
			case "main-recover":
				out, err = core.RecoverProjectMain(dir, a[2], a[3])
			case "main-preview":
				out, err = core.PreviewProjectMain(dir, a[2], a[3], core.MainActionContext(c))
			case "main-start":
				if registry, _ := cmd.Context().Value(workflowKey{}).(*workflowRegistry); registry != nil {
					if _, err = registry.ready(cmd.Context(), dir); err != nil {
						return err
					}
				}
				out, err = core.StartProjectMain(dir, a[2], a[3], a[8], core.MainActionContext(c))
			case "client-list":
				out, err = core.ListProjectClients(dir, a[2])
			case "client-status":
				out, err = core.InspectProjectClient(dir, a[2], a[3])
			case "client-prepare", "client-attention-prepare-v1":
				if a[8] != "integrator" && a[8] != "worker" || a[8] == "integrator" && a[9] != "" || a[8] == "worker" && !workPointer.MatchString(a[9]) {
					return errors.New("exact client role/work pointer required")
				}
				bindingReader, e := core.OpenReader(dir)
				if e != nil {
					return e
				}
				binding, e := bindingReader.WorkBinding(a[2])
				bindingReader.Close()
				if e != nil || binding.RepoRoot != c.Folder {
					return errors.New("native client selected project/folder changed")
				}
				protocol := core.ClientProtocol
				if action == "client-attention-prepare-v1" {
					protocol = core.AttentionClientProtocol
				}
				raw, e := json.Marshal(core.ClientPrepareRequest{Protocol: protocol, ProjectID: a[2], NavigationID: a[3], TargetKind: a[8], WorkRequestID: a[9], Space: a[0]})
				if e != nil {
					return e
				}
				out, err = core.PrepareProjectClient(dir, raw)
			case "client-open", "client-verify":
				if !clientDigest.MatchString(a[3]) {
					return errors.New("exact native pointer digest required")
				}
				pin, e := cmd.Flags().GetString("hc-binding-sha256")
				if e != nil {
					return e
				}
				if _, e = boundHC(cmd.Context(), pin); e != nil {
					return e
				}
				binding, _ := boundHC(cmd.Context(), pin)
				if binding.Space != c.Space || binding.Folder != c.Folder || binding.Queue != c.QueueIdentity {
					return errors.New("native client HC scope changed")
				}
				c.NativeBindingSHA256 = pin
				if action == "client-open" {
					out, err = openNativeClient(cmd.Context(), dir, a[2], a[3], core.ClientActionContext(c))
				} else {
					out, err = inspectNativeClient(cmd.Context(), dir, a[2], a[3], core.ClientActionContext(c))
				}
			}
			if err != nil {
				return err
			}
			return writeWork(cmd, out)
		}})
		if action == "client-open" || action == "client-verify" {
			commands[len(commands)-1].Flags().String("hc-binding-sha256", "", "Exact reviewed endpoint HC binding digest")
		}
	}
	return append(commands, workflowCommands()...)
}

type nativeClientView struct {
	Protocol      string            `json:"protocol"`
	Status        core.ClientStatus `json:"status"`
	TerminalState string            `json:"terminal_state"`
	CurrentRoute  string            `json:"current_route"`
}

type hcTerminal struct {
	Address string            `json:"address"`
	Cwd     string            `json:"cwd"`
	Argv    []string          `json:"argv"`
	Labels  map[string]string `json:"labels"`
	State   string            `json:"state"`
}

func hcRead(conn *hcConnection, id string, p core.ClientPointer, space, binding string) (hcTerminal, error) {
	var t hcTerminal
	if !nativeTerminalID.MatchString(id) {
		return t, errors.New("unsupported exact returned HC terminal ID")
	}
	if err := conn.call("terminal.get", map[string]any{"terminal": id}, &t); err != nil {
		return t, err
	}
	if t.Address != id || t.Cwd != p.Cwd || !reflect.DeepEqual(t.Argv, p.LauncherArgv) || t.Labels["ocq.handoff"] != p.PointerSHA256 || t.Labels["ocq.hc_binding"] != binding || t.Labels["herdr.space"] != space || (t.State != "live" && t.State != "finished") {
		return t, errors.New("exact HC terminal identity/launch metadata mismatch")
	}
	return t, nil
}

func openNativeClient(parent context.Context, dir, project, digest string, c core.ClientActionContext) (nativeClientView, error) {
	ctx, cancel := context.WithTimeout(parent, 25*time.Second)
	defer cancel()
	view := nativeClientView{Protocol: core.ClientProtocol, TerminalState: "unobserved", CurrentRoute: "unverified"}
	binding, err := boundHC(ctx, c.NativeBindingSHA256)
	if err != nil {
		return view, err
	}
	conn, err := binding.connect(ctx)
	if err != nil {
		return view, err
	}
	defer conn.Close()
	s, err := core.ArmProjectClient(dir, project, digest, c)
	if err != nil {
		return view, err
	}
	if s, err = core.AttemptProjectClient(dir, project, digest, c); err != nil {
		return view, err
	}
	view.Status = s
	if !s.AttemptPermitted {
		// Same pointer only returns the original receipt, never another run.
		return view, nil
	}
	// The private HC core need not share the bridge's HOME/config/PATH.
	// Propagate only the original same-user validation context, never caller
	// supplied environment, and keep the ordinary TUI's generated state private.
	storageRoot, err := os.MkdirTemp(os.Getenv("TMPDIR"), "ocq-inspection-client-")
	if err != nil {
		return view, err
	}
	storageRoot, err = filepath.Abs(storageRoot)
	if err != nil {
		return view, err
	}
	launchEnv := map[string]string{"OCQ_CLIENT_STORAGE_ROOT": storageRoot}
	launchEnv["OCQ_CLIENT_CONTEXT"], err = core.NativeClientLaunchContext()
	if err != nil {
		return view, err
	}
	var returned hcTerminal
	err = conn.call("terminal.run", map[string]any{"argv": s.Pointer.LauncherArgv, "cwd": s.Pointer.Cwd, "env": launchEnv, "no_parent": true, "no_notify": true, "wait": false, "keep": false, "labels": map[string]string{"ocq.handoff": digest, "ocq.hc_binding": binding.digest(), "herdr.space": binding.Space}}, &returned)
	if err != nil {
		view.Status, _ = core.InspectProjectClient(dir, project, digest)
		return view, nil // durable attempted_unknown; NEVER respawn or adopt
	}
	if !nativeTerminalID.MatchString(returned.Address) {
		view.Status, _ = core.InspectProjectClient(dir, project, digest)
		return view, nil
	}
	observed, err := hcRead(conn, returned.Address, s.Pointer, binding.Space, binding.digest())
	if err != nil {
		view.Status, _ = core.InspectProjectClient(dir, project, digest)
		return view, nil
	}
	receipt := core.ClientTerminalReceipt{ReturnedTerminalID: returned.Address, ObservedTerminalID: observed.Address, Peer: c.Peer, Cwd: observed.Cwd, Argv: observed.Argv, Labels: observed.Labels}
	view.Status, err = core.RecordProjectClientContext(ctx, dir, project, digest, c, receipt)
	view.TerminalState = observed.State
	return view, err
}

func inspectNativeClient(parent context.Context, dir, project, digest string, c core.ClientActionContext) (nativeClientView, error) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	binding, err := boundHC(ctx, c.NativeBindingSHA256)
	if err != nil {
		return nativeClientView{}, err
	}
	s, err := core.VerifyProjectClient(dir, project, digest, c)
	view := nativeClientView{Protocol: core.ClientProtocol, Status: s, TerminalState: "unobserved", CurrentRoute: "unverified"}
	if err != nil {
		return view, err
	}
	if s.Terminal == nil {
		return view, nil
	}
	conn, err := binding.connect(ctx)
	if err != nil {
		return view, err
	}
	defer conn.Close()
	observed, err := hcRead(conn, s.Terminal.ReturnedTerminalID, s.Pointer, binding.Space, binding.digest())
	if err != nil {
		return view, err
	}
	view.TerminalState = observed.State
	return view, nil
}
