package main

import (
	"encoding/json"
	"os"

	"github.com/spf13/cobra"
	"ocq/internal/core"
)

// These commands act only on the queue local to this bridge. The panel exposes
// mutation only for the local Herdr machine; selected remote actions are disabled.
func notificationAckCommands() []*cobra.Command {
	commands := []*cobra.Command{}
	for _, action := range []string{"status", "prepare", "arm", "attempt"} {
		action := action
		count := 5
		use := "notification-ack-" + action + " SPACE QUEUE_ID EVENT JOB SEQUENCE"
		if action == "arm" || action == "attempt" {
			count++
			use += " INTENT"
		}
		commands = append(commands, &cobra.Command{Use: use, Args: cobra.ExactArgs(count), RunE: func(cmd *cobra.Command, args []string) error {
			scope := core.NotificationAckScope{Machine: "local", Space: args[0], Queue: args[1], Event: args[2], Job: args[3], Sequence: args[4]}
			var status core.NotificationAckStatus
			var err error
			state := os.Getenv("OCQ_STATE_DIR")
			switch action {
			case "status":
				status, err = core.InspectNotificationAcknowledgement(state, scope)
			case "prepare":
				status, err = core.PrepareNotificationAcknowledgement(state, scope)
			case "arm":
				status, err = core.ArmNotificationAcknowledgement(state, scope, args[5])
			case "attempt":
				status, err = core.AttemptNotificationAcknowledgement(state, scope, args[5])
			}
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
		}})
	}
	return commands
}
