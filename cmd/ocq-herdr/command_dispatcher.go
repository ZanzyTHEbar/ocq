package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"time"

	"github.com/spf13/cobra"
	"ocq/internal/core"
)

func commandDispatcherCommands() []*cobra.Command {
	read := &cobra.Command{Use: "dispatcher-read SPACE QUEUE", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, a []string) error {
		dir, e := existingBridgeStateDir()
		if e != nil {
			return e
		}
		out, e := core.InspectCommandDispatcher(dir, core.CommandDispatcherTarget{Scope: core.CommandSubmissionScope{Machine: "local", Space: a[0], Queue: a[1]}})
		if e != nil {
			return e
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
	}}
	commands := []*cobra.Command{{Use: "dispatcher-context SPACE", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, a []string) error {
		dir, e := existingBridgeStateDir()
		if e != nil {
			return e
		}
		r, e := core.OpenReader(dir)
		if e != nil {
			return e
		}
		defer r.Close()
		s, e := r.CommandSubmissionContext(a[0])
		if e != nil {
			return e
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(s)
	}}}
	for _, action := range []string{"status", "prepare", "arm", "attempt", "cancel", "drain-prepare", "drain-arm", "drain-attempt", "retire", "bind"} {
		action := action
		count := 6
		use := "dispatcher-" + action + " SPACE QUEUE PANE ANCHOR_TERMINAL TAB FOLDER"
		if action != "status" && action != "prepare" {
			count++
			use += " OPERATION"
		}
		if action == "bind" {
			count += 5
			use += " HELPER_TERMINAL HELPER_PANE HELPER_TAB PID START"
		}
		commands = append(commands, &cobra.Command{Use: use, Args: cobra.ExactArgs(count), RunE: func(cmd *cobra.Command, a []string) error {
			t := core.CommandDispatcherTarget{Scope: core.CommandSubmissionScope{Machine: "local", Space: a[0], Queue: a[1]}, Pane: a[2], Terminal: a[3], Tab: a[4], Folder: a[5]}
			var native *hcConnection
			if cmd.Context().Value(hcBindingKey{}) != nil {
				pin, _ := cmd.Flags().GetString("hc-binding-sha256")
				binding, err := boundHC(cmd.Context(), pin)
				if err != nil {
					return err
				}
				if binding.Space != a[0] || binding.Queue != a[1] || binding.Folder != a[5] {
					return errors.New("dispatcher HC scope changed")
				}
				t.HCBindingSHA256 = pin
				native, err = binding.connect(cmd.Context())
				if err != nil {
					return err
				}
				defer native.Close()
				// The anchor authorizes native admission, not an everlasting
				// dependency for stopping its already-bound owned helper.
				if action == "prepare" || action == "arm" || action == "attempt" || action == "bind" {
					var anchor hcTerminal
					if !nativeTerminalID.MatchString(t.Terminal) {
						return errors.New("exact local anchor terminal required")
					}
					if err = native.call("terminal.get", map[string]any{"terminal": t.Terminal}, &anchor); err != nil {
						return err
					}
					if anchor.Address != t.Terminal || anchor.State != "live" || anchor.Labels["herdr.space"] != t.Scope.Space || anchor.Labels["herdr.tab"] != t.Tab {
						return errors.New("pinned HC anchor space/tab changed")
					}
				}
			}
			dir, e := existingBridgeStateDir()
			if e != nil {
				return e
			}
			var out core.CommandDispatcherStatus
			if native != nil && (action == "drain-prepare" || action == "drain-arm" || action == "drain-attempt") {
				owned, err := core.InspectCommandDispatcher(dir, t)
				if err != nil {
					return err
				}
				if owned.ID != a[6] || owned.Owner == nil || owned.Process != "alive" {
					return errors.New("exact retained live dispatcher helper required")
				}
				if err = verifyPinnedDispatcherHelper(native, owned, t, owned.Owner.Terminal, strconv.Itoa(owned.Owner.PID)); err != nil {
					return err
				}
			}
			var events *core.ObservationEvents
			if native != nil && (action == "attempt" || action == "bind") {
				events, e = core.WatchObservationEvents(cmd.Context(), dir)
				if e != nil {
					return e
				}
				defer events.Close()
			}
			switch action {
			case "status":
				out, e = core.InspectCommandDispatcher(dir, t)
			case "prepare":
				out, e = core.PrepareCommandDispatcher(dir, t)
			case "bind":
				if native != nil {
					exact, err := core.InspectCommandDispatcher(dir, t)
					if err != nil {
						return err
					}
					if exact.ID != a[6] {
						return errors.New("exact dispatcher operation required")
					}
					if err = verifyPinnedDispatcherHelper(native, exact, t, a[7], a[10]); err != nil {
						return err
					}
				}
				out, e = core.BindCommandDispatcher(dir, t, a[6], a[7], a[8], a[9], a[10], a[11])
			default:
				out, e = core.MarkCommandDispatcher(dir, t, a[6], action)
			}
			if e != nil {
				return e
			}
			if native != nil && action == "attempt" && out.AttemptPermitted {
				var returned hcTerminal
				// Durable attempt precedes the only native run. Never retry/adopt.
				err := native.call("terminal.run", map[string]any{"argv": []string{out.Executable, "dispatcher-helper", out.ID}, "cwd": t.Folder, "no_parent": true, "no_notify": true, "wait": false, "keep": false, "env": map[string]string{"OCQ_STATE_DIR": dir}, "labels": map[string]string{"herdr.space": t.Scope.Space, "herdr.tab": t.Tab, "ocq.hc_binding": t.HCBindingSHA256, "ocq.dispatcher": out.ID}}, &returned)
				if err != nil || !nativeTerminalID.MatchString(returned.Address) {
					out, e = core.InspectCommandDispatcher(dir, t)
					if e != nil {
						return e
					}
					return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
				}
				if e = core.RecordCommandDispatcherNativeReturn(cmd.Context(), dir, t, out.ID, returned.Address); e != nil {
					return e
				}
				out, e = waitDispatcherChange(cmd.Context(), events, dir, t, out.ID, "born")
				if e != nil {
					return e
				}
				if out.Owner == nil || out.Owner.Terminal != returned.Address {
					return errors.New("returned HC helper does not match durable birth; no adoption")
				}
			}
			if native != nil && action == "bind" {
				out, e = waitDispatcherChange(cmd.Context(), events, dir, t, out.ID, "ready")
				if e != nil {
					return e
				}
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
		}})
		commands[len(commands)-1].Flags().String("hc-binding-sha256", "", "Exact reviewed private HC binding digest")
	}
	commands = append(commands, read)
	return append(commands, &cobra.Command{Use: "dispatcher-helper OPERATION", Hidden: true, Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, a []string) error {
		dir, e := existingBridgeStateDir()
		if e != nil {
			return e
		}
		return core.RunCommandDispatcher(dir, a[0])
	}})
}

// Teardown checks the retained helper on the pinned core, not the old anchor.
// Core bind/drain additionally recheck pidfd/start/boot/argv/image/foreground
// and (for drain) the exact dispatch-lock device/inode/owner before signalling.
func verifyPinnedDispatcherHelper(native *hcConnection, exact core.CommandDispatcherStatus, t core.CommandDispatcherTarget, terminal, pid string) error {
	if !nativeTerminalID.MatchString(terminal) || exact.Owner == nil || exact.Owner.Terminal != terminal || strconv.Itoa(exact.Owner.PID) != pid || exact.Process != "alive" {
		return errors.New("exact local owned helper required")
	}
	var helper struct {
		hcTerminal
		PID        int `json:"pid"`
		Foreground *struct {
			PID  int      `json:"pid"`
			Argv []string `json:"argv"`
		} `json:"foreground"`
	}
	if err := native.call("terminal.get", map[string]any{"terminal": terminal}, &helper); err != nil {
		return err
	}
	argv := []string{exact.Executable, "dispatcher-helper", exact.ID}
	foregroundMismatch := helper.Foreground != nil && (helper.Foreground.PID != helper.PID || !reflect.DeepEqual(helper.Foreground.Argv, argv))
	if helper.Address != terminal || helper.State != "live" || helper.Cwd != t.Folder || helper.Labels["herdr.space"] != t.Scope.Space || helper.Labels["herdr.tab"] != t.Tab || helper.Labels["ocq.hc_binding"] != t.HCBindingSHA256 || helper.Labels["ocq.dispatcher"] != exact.ID || !reflect.DeepEqual(helper.Argv, argv) || foregroundMismatch || strconv.Itoa(helper.PID) != pid {
		return errors.New("exact pinned HC foreground helper required")
	}
	return nil
}

// Subscribe before the transition; events are hints, never queue authority.
// No timer refresh, native model scan, launch retry or polling fallback.
func waitDispatcherChange(parent context.Context, events *core.ObservationEvents, dir string, t core.CommandDispatcherTarget, id, want string) (core.CommandDispatcherStatus, error) {
	ctx, cancel := context.WithTimeout(parent, 12*time.Second)
	defer cancel()
	for {
		s, err := core.InspectCommandDispatcher(dir, t)
		if err != nil {
			return s, err
		}
		if s.ID != id {
			return s, errors.New("dispatcher lifetime changed")
		}
		if s.State == want {
			return s, nil
		}
		if s.State == "exit_recorded" || s.State == "stopped" {
			return s, errors.New("dispatcher exited before acknowledged transition")
		}
		select {
		case <-ctx.Done():
			return s, errors.New("dispatcher observation uncertain; read retained status, never retry launch")
		case err := <-events.Errors:
			return s, err
		case <-events.Changes:
		}
	}
}
