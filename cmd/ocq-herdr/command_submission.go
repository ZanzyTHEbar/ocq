package main

import (
	"encoding/json"
	"os"

	"github.com/spf13/cobra"
	"ocq/internal/core"
)

func commandSubmissionCommands() []*cobra.Command {
	commands := []*cobra.Command{viewCommand("command-submit-context SPACE", 1, func(r *core.Reader, a []string) (any, error) { return r.CommandSubmissionContext(a[0]) })}
	for _, action := range []string{"status", "arm", "attempt", "reconcile", "cancel", "retire", "draft", "receive", "preview", "rendezvous"} {
		action := action
		count := 2
		use := "command-submit-" + action + " SPACE QUEUE"
		if action != "status" {
			count++
			use += " ID_OR_LAUNCH"
		}
		if action == "preview" {
			count++
			use += " LAUNCH"
		}
		commands = append(commands, &cobra.Command{Use: use, Args: cobra.ExactArgs(count), RunE: func(cmd *cobra.Command, a []string) error {
			s := core.CommandSubmissionScope{Machine: "local", Space: a[0], Queue: a[1]}
			dir := os.Getenv("OCQ_STATE_DIR")
			var out any
			var e error
			switch action {
			case "status":
				out, e = core.InspectCommandSubmission(dir, s)
			case "arm":
				out, e = core.ArmCommandSubmission(dir, s, a[2])
			case "attempt":
				out, e = core.AttemptCommandSubmission(dir, s, a[2])
			case "reconcile":
				out, e = core.ReconcileCommandSubmission(dir, s, a[2])
			case "cancel":
				out, e = core.CancelCommandSubmission(dir, s, a[2])
			case "retire":
				out, e = core.RetireCommandSubmission(dir, s, a[2])
			case "draft", "receive":
				out, e = core.ExchangeCommandSubmission(dir, s, action, "", a[2])
			case "preview":
				out, e = core.ExchangeCommandSubmission(dir, s, action, a[2], a[3])
			case "rendezvous":
				out, e = core.CommandSubmissionRendezvous(dir, s, a[2])
			}
			if e != nil {
				return e
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
		}})
	}
	return commands
}
