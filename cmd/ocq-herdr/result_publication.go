package main

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/spf13/cobra"
	"ocq/internal/core"
)

// Manifest/evidence bytes never enter command arguments, stdout or diagnostics.
// Only explicit authenticated retained preview carries them back to the UI.
func resultPublicationCommands() []*cobra.Command {
	out := []*cobra.Command{viewCommand("result-publish-context SPACE PROJECT JOB", 3, func(r *core.Reader, a []string) (any, error) {
		result, err := r.ResultPublicationContext(a[0], a[1], a[2])
		if err != nil {
			return nil, errors.New("exact local publication context unavailable")
		}
		return result, nil
	})}
	for _, action := range []string{"list", "status", "receive", "rendezvous", "preview", "arm", "attempt"} {
		action := action
		n := 5
		use := "result-publish-" + action + " SPACE QUEUE PROJECT JOB RECEIPT_SEQUENCE"
		switch action {
		case "status":
			n++
			use += " MANIFEST_SHA256"
		case "receive", "rendezvous":
			n++
			use += " LAUNCH"
		case "preview":
			n += 3
			use += " MANIFEST_SHA256 PAYLOAD_SHA256 LAUNCH"
		case "arm", "attempt":
			n += 2
			use += " MANIFEST_SHA256 PAYLOAD_SHA256"
		}
		out = append(out, &cobra.Command{Use: use, Args: cobra.ExactArgs(n), RunE: func(cmd *cobra.Command, a []string) error {
			t := core.ResultPublicationTarget{CommandResolutionTarget: core.CommandResolutionTarget{Machine: "local", Space: a[0], Queue: a[1], Job: a[3], Sequence: a[4]}, Project: a[2]}
			dir := os.Getenv("OCQ_STATE_DIR")
			var result any
			var err error
			switch action {
			case "list":
				result, err = core.ListResultPublications(dir, t)
			case "status":
				result, err = core.InspectResultPublication(dir, t, a[5])
			case "receive":
				result, err = core.ReceiveResultPublication(dir, t, a[5])
			case "rendezvous":
				result, err = core.ResultPublicationRendezvous(dir, t, a[5])
			case "preview":
				result, err = core.PreviewResultPublication(dir, t, a[5], a[6], a[7])
			case "arm":
				result, err = core.ArmResultPublication(dir, t, a[5], a[6])
			case "attempt":
				result, err = core.AttemptResultPublication(dir, t, a[5], a[6])
			}
			if err != nil {
				return errors.New("publication unavailable or refused; check retained intent and exact association only; do not automatically resend")
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}})
	}
	return out
}
