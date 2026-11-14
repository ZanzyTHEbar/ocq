package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"ocq/internal/core"
)

var clientDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var nativeTerminalID = regexp.MustCompile(`^t[1-9][0-9]*$`)

// No Main authoring/upload endpoint. Setup/profile/prompt files stay server-side.
func mainClientCommands() []*cobra.Command {
	var commands []*cobra.Command
	for _, action := range []string{"main-list", "main-status", "main-recover", "main-preview", "main-start", "client-list", "client-status", "client-prepare", "client-open", "client-verify"} {
		action := action
		n, use := 3, action+" SPACE QUEUE PROJECT"
		if action != "main-list" && action != "client-list" {
			n++
			use += " POINTER"
		}
		contextual := action == "main-preview" || action == "main-start" || action == "client-prepare" || action == "client-open" || action == "client-verify"
		if contextual {
			n += 4
			use += " PEER FOLDER STORE BRIDGE_IMAGE"
		}
		if action == "main-start" {
			n++
			use += " PREVIEW_SHA256"
		}
		if action == "client-prepare" {
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
				out, err = core.StartProjectMain(dir, a[2], a[3], a[8], core.MainActionContext(c))
			case "client-list":
				out, err = core.ListProjectClients(dir, a[2])
			case "client-status":
				out, err = core.InspectProjectClient(dir, a[2], a[3])
			case "client-prepare":
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
				raw, e := json.Marshal(core.ClientPrepareRequest{Protocol: core.ClientProtocol, ProjectID: a[2], NavigationID: a[3], TargetKind: a[8], WorkRequestID: a[9], Space: a[0]})
				if e != nil {
					return e
				}
				out, err = core.PrepareProjectClient(dir, raw)
			case "client-open", "client-verify":
				if !clientDigest.MatchString(a[3]) {
					return errors.New("exact native pointer digest required")
				}
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
	}
	return commands
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

type hcOutput struct{ data bytes.Buffer }

func (b *hcOutput) Write(p []byte) (int, error) {
	if b.data.Len()+len(p) > 256<<10 {
		return 0, errors.New("HC response exceeds bound")
	}
	return b.data.Write(p)
}

// Official HC 0.1.21 JSON responses flatten Terminal fields at the root.
// No shell, guessed ID, label scan, auth retry, service start or CLI fallback.
func hcCall(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "hc", args...)
	cmd.WaitDelay = 250 * time.Millisecond
	var out hcOutput
	cmd.Stdout, cmd.Stderr = &out, io.Discard
	if err := cmd.Run(); err != nil {
		return nil, errors.New("exact HC command unavailable; outcome may be unknown")
	}
	return out.data.Bytes(), nil
}

func hcRead(ctx context.Context, id string, p core.ClientPointer) (hcTerminal, error) {
	var t hcTerminal
	if !nativeTerminalID.MatchString(id) {
		return t, errors.New("unsupported exact returned HC terminal ID")
	}
	b, err := hcCall(ctx, "get", "--json", "--", id)
	if err != nil {
		return t, err
	}
	if json.Unmarshal(b, &t) != nil || t.Address != id || t.Cwd != p.Cwd || !reflect.DeepEqual(t.Argv, p.LauncherArgv) || t.Labels["ocq.handoff"] != p.PointerSHA256 || (t.State != "live" && t.State != "finished") {
		return t, errors.New("exact HC terminal identity/launch metadata mismatch")
	}
	return t, nil
}

func openNativeClient(parent context.Context, dir, project, digest string, c core.ClientActionContext) (nativeClientView, error) {
	ctx, cancel := context.WithTimeout(parent, 25*time.Second)
	defer cancel()
	view := nativeClientView{Protocol: core.ClientProtocol, TerminalState: "unobserved", CurrentRoute: "unverified"}
	// Gate version before arm/attempt. A refused/missing HC is not a spawn.
	version, err := hcCall(ctx, "--version")
	if err != nil || (strings.TrimSpace(string(version)) != "hc 0.1.21" && strings.TrimSpace(string(version)) != "herdr-core 0.1.21") {
		return view, errors.New("native client requires supported HC 0.1.21")
	}
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
	args := []string{"run", "--bg", "--json", "--no-parent", "--no-notify", "--label", "ocq.handoff=" + digest, "--cwd", s.Pointer.Cwd, "--"}
	args = append(args, s.Pointer.LauncherArgv...)
	raw, err := hcCall(ctx, args...)
	if err != nil {
		view.Status, _ = core.InspectProjectClient(dir, project, digest)
		return view, nil // durable attempted_unknown; NEVER respawn or adopt
	}
	var returned hcTerminal
	if json.Unmarshal(raw, &returned) != nil || !nativeTerminalID.MatchString(returned.Address) {
		view.Status, _ = core.InspectProjectClient(dir, project, digest)
		return view, nil
	}
	observed, err := hcRead(ctx, returned.Address, s.Pointer)
	if err != nil {
		view.Status, _ = core.InspectProjectClient(dir, project, digest)
		return view, nil
	}
	receipt := core.ClientTerminalReceipt{ReturnedTerminalID: returned.Address, ObservedTerminalID: observed.Address, Peer: c.Peer, Cwd: observed.Cwd, Argv: observed.Argv, Labels: observed.Labels}
	view.Status, err = core.RecordProjectClient(dir, project, digest, c, receipt)
	view.TerminalState = observed.State
	return view, err
}

func inspectNativeClient(parent context.Context, dir, project, digest string, c core.ClientActionContext) (nativeClientView, error) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	s, err := core.VerifyProjectClient(dir, project, digest, c)
	view := nativeClientView{Protocol: core.ClientProtocol, Status: s, TerminalState: "unobserved", CurrentRoute: "unverified"}
	if err != nil {
		return view, err
	}
	if s.Terminal == nil {
		return view, nil
	}
	observed, err := hcRead(ctx, s.Terminal.ReturnedTerminalID, s.Pointer)
	if err != nil {
		return view, err
	}
	view.TerminalState = observed.State
	return view, nil
}
