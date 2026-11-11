package main

import (
	"encoding/json"
	"github.com/spf13/cobra"
	"ocq/internal/core"
	"os"
)

func agentHandoffCommands() []*cobra.Command {
	var out []*cobra.Command
	for _, action := range []string{"status", "verify"} {
		action := action
		n := 6
		use := "agent-handoff-" + action + " SPACE QUEUE JOB RECEIPT_SEQUENCE SESSION WORKTREE"
		if action == "verify" {
			n++
			use += " SERVICE_IDENTITY"
		}
		out = append(out, &cobra.Command{Use: use, Args: cobra.ExactArgs(n), RunE: func(cmd *cobra.Command, a []string) error {
			t := core.AgentReconcileTarget{CommandResolutionTarget: core.CommandResolutionTarget{Machine: "local", Space: a[0], Queue: a[1], Job: a[2], Sequence: a[3]}, Session: a[4], Worktree: a[5]}
			verify := ""
			if action == "verify" {
				verify = a[6]
			}
			s, e := core.InspectAgentHandoff(os.Getenv("OCQ_STATE_DIR"), t, verify)
			if e != nil {
				return e
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(s)
		}})
	}
	return out
}
