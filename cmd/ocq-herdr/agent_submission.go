package main

import (
	"encoding/json"
	"os"

	"github.com/spf13/cobra"
	"ocq/internal/core"
)

func agentSubmissionCommands() []*cobra.Command {
	commands := []*cobra.Command{viewCommand("agent-submit-context SPACE", 1, func(r *core.Reader, a []string) (any, error) { return r.CommandSubmissionContext(a[0]) })}
	for _, action := range []string{"status", "arm", "attempt", "reconcile", "cancel", "retire", "draft", "import", "receive", "preview", "rendezvous"} {
		action := action
		count := 2
		use := "agent-submit-" + action + " SPACE QUEUE"
		if action != "status" {
			count++
			use += " ID_OR_LAUNCH"
		}
		if action == "preview" {
			count++
			use += " LAUNCH"
		}
		commands = append(commands, &cobra.Command{Use: use, Args: cobra.ExactArgs(count), RunE: func(cmd *cobra.Command, a []string) error {
			s := core.AgentSubmissionScope{Machine: "local", Space: a[0], Queue: a[1]}
			dir := os.Getenv("OCQ_STATE_DIR")
			var out any
			var e error
			switch action {
			case "status":
				out, e = core.InspectAgentSubmission(dir, s)
			case "arm":
				out, e = core.ArmAgentSubmission(dir, s, a[2])
			case "attempt":
				out, e = core.AttemptAgentSubmission(dir, s, a[2])
			case "reconcile":
				out, e = core.ReconcileAgentSubmission(dir, s, a[2])
			case "cancel":
				out, e = core.CancelAgentSubmission(dir, s, a[2])
			case "retire":
				out, e = core.RetireAgentSubmission(dir, s, a[2])
			case "draft", "import", "receive":
				out, e = core.ExchangeAgentSubmission(dir, s, action, "", a[2])
			case "preview":
				out, e = core.ExchangeAgentSubmission(dir, s, action, a[2], a[3])
			case "rendezvous":
				out, e = core.AgentSubmissionRendezvous(dir, s, a[2])
			}
			if e != nil {
				return e
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
		}})
	}
	return commands
}
