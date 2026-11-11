package main

import (
	"encoding/json"
	"github.com/spf13/cobra"
	"ocq/internal/core"
	"os"
)

func queuedCancelCommands() []*cobra.Command {
	var commands []*cobra.Command
	for _, action := range []string{"status", "prepare", "arm", "attempt"} {
		action := action
		count := 5
		use := "queued-cancel-" + action + " SPACE QUEUE_ID JOB RECEIPT_SEQUENCE DISPLAYED_STATE"
		if action == "arm" || action == "attempt" {
			count++
			use += " INTENT"
		}
		commands = append(commands, &cobra.Command{Use: use, Args: cobra.ExactArgs(count), RunE: func(cmd *cobra.Command, args []string) error {
			s := core.QueuedCancelScope{Machine: "local", Space: args[0], Queue: args[1], Job: args[2], Sequence: args[3], DisplayedState: args[4]}
			dir := os.Getenv("OCQ_STATE_DIR")
			var out core.QueuedCancelStatus
			var err error
			switch action {
			case "status":
				out, err = core.InspectQueuedCancellation(dir, s)
			case "prepare":
				out, err = core.PrepareQueuedCancellation(dir, s)
			case "arm":
				out, err = core.ArmQueuedCancellation(dir, s, args[5])
			case "attempt":
				out, err = core.AttemptQueuedCancellation(dir, s, args[5])
			}
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
		}})
	}
	return commands
}
