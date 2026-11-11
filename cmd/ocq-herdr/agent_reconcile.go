package main

import (
	"encoding/json"
	"github.com/spf13/cobra"
	"ocq/internal/core"
	"os"
)

func agentReconcileCommands() []*cobra.Command {
	var out []*cobra.Command
	for _, action := range []string{"status", "prepare", "arm", "attempt"} {
		action := action
		n := 6
		use := "agent-reconcile-" + action + " SPACE QUEUE JOB RECEIPT_SEQUENCE SESSION WORKTREE"
		if action == "prepare" {
			n++
			use += " SNAPSHOT_SHA256"
		}
		if action == "arm" || action == "attempt" {
			n += 2
			use += " OPERATION INTENT_SHA256"
		}
		out = append(out, &cobra.Command{Use: use, Args: cobra.ExactArgs(n), RunE: func(cmd *cobra.Command, a []string) error {
			t := core.AgentReconcileTarget{CommandResolutionTarget: core.CommandResolutionTarget{Machine: "local", Space: a[0], Queue: a[1], Job: a[2], Sequence: a[3]}, Session: a[4], Worktree: a[5]}
			var status core.AgentReconcileStatus
			var e error
			dir := os.Getenv("OCQ_STATE_DIR")
			switch action {
			case "status":
				status, e = core.InspectAgentReconciliation(dir, t)
			case "prepare":
				status, e = core.PrepareAgentReconciliation(dir, t, a[6])
			case "arm":
				status, e = core.ArmAgentReconciliation(dir, t, a[6], a[7])
			case "attempt":
				status, e = core.AttemptAgentReconciliation(dir, t, a[6], a[7])
			}
			if e != nil {
				return e
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
		}})
	}
	return out
}
