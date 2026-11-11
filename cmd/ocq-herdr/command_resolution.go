package main

import (
	"encoding/json"
	"github.com/spf13/cobra"
	"ocq/internal/core"
	"os"
)

// Only identifiers and digests are arguments. Evidence uses the mutually
// authenticated exact-byte connection, including explicit retained preview.
func commandResolutionCommands() []*cobra.Command {
	var out []*cobra.Command
	for _, action := range []string{"status", "receive", "rendezvous", "preview", "arm", "attempt"} {
		action := action
		n := 4
		use := "command-resolve-" + action + " SPACE QUEUE JOB RECEIPT_SEQUENCE"
		switch action {
		case "receive", "rendezvous":
			n++
			use += " LAUNCH"
		case "preview":
			n += 2
			use += " PAYLOAD_SHA256 LAUNCH"
		case "arm", "attempt":
			n++
			use += " PAYLOAD_SHA256"
		}
		out = append(out, &cobra.Command{Use: use, Args: cobra.ExactArgs(n), RunE: func(cmd *cobra.Command, args []string) error {
			t := core.CommandResolutionTarget{Machine: "local", Space: args[0], Queue: args[1], Job: args[2], Sequence: args[3]}
			dir := os.Getenv("OCQ_STATE_DIR")
			var result any
			var err error
			switch action {
			case "status":
				result, err = core.InspectCommandResolution(dir, t)
			case "receive":
				result, err = core.ReceiveCommandResolution(dir, t, args[4])
			case "rendezvous":
				result, err = core.CommandResolutionRendezvous(dir, t, args[4])
			case "preview":
				result, err = core.PreviewCommandResolution(dir, t, args[4], args[5])
			case "arm":
				result, err = core.ArmCommandResolution(dir, t, args[4])
			case "attempt":
				result, err = core.AttemptCommandResolution(dir, t, args[4])
			}
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}})
	}
	return out
}
