package main

import (
	"encoding/json"

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
			dir, e := existingBridgeStateDir()
			if e != nil {
				return e
			}
			var out core.CommandDispatcherStatus
			switch action {
			case "status":
				out, e = core.InspectCommandDispatcher(dir, t)
			case "prepare":
				out, e = core.PrepareCommandDispatcher(dir, t)
			case "bind":
				out, e = core.BindCommandDispatcher(dir, t, a[6], a[7], a[8], a[9], a[10], a[11])
			default:
				out, e = core.MarkCommandDispatcher(dir, t, a[6], action)
			}
			if e != nil {
				return e
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
		}})
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
