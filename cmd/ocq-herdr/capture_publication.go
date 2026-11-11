package main

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/spf13/cobra"
	"ocq/internal/core"
)

func capturePublicationCommands() []*cobra.Command {
	out := []*cobra.Command{viewCommand("capture-context SPACE PROJECT JOB", 3, func(r *core.Reader, a []string) (any, error) {
		v, err := r.CapturePublicationContext(a[0], a[1], a[2])
		if err != nil {
			return nil, errors.New("exact owned local capture context unavailable")
		}
		return v, nil
	})}
	for _, action := range []string{"list", "status", "prepare", "rendezvous", "preview", "arm", "attempt"} {
		action := action
		n := 5
		use := "capture-" + action + " SPACE QUEUE PROJECT JOB RECEIPT_SEQUENCE"
		switch action {
		case "status":
			n++
			use += " CAPTURE_ID"
		case "rendezvous":
			n++
			use += " LAUNCH"
		case "prepare":
			n += 2
			use += " CAPTURE_ID LAUNCH"
		case "preview":
			n += 3
			use += " CAPTURE_ID PAYLOAD_SHA256 LAUNCH"
		case "arm", "attempt":
			n += 2
			use += " CAPTURE_ID PAYLOAD_SHA256"
		}
		out = append(out, &cobra.Command{Use: use, Args: cobra.ExactArgs(n), RunE: func(cmd *cobra.Command, a []string) error {
			t := core.ResultPublicationTarget{CommandResolutionTarget: core.CommandResolutionTarget{Machine: "local", Space: a[0], Queue: a[1], Job: a[3], Sequence: a[4]}, Project: a[2]}
			dir := os.Getenv("OCQ_STATE_DIR")
			var result any
			var err error
			switch action {
			case "list":
				result, err = core.ListCapturePublications(dir, t)
			case "status":
				result, err = core.InspectCapturePublication(dir, t, a[5])
			case "prepare":
				result, err = core.PrepareCapturePublication(dir, t, a[5], a[6])
			case "rendezvous":
				result, err = core.CapturePublicationRendezvous(dir, t, a[5])
			case "preview":
				result, err = core.PreviewCapturePublication(dir, t, a[5], a[6], a[7])
			case "arm":
				result, err = core.ArmCapturePublication(dir, t, a[5], a[6])
			case "attempt":
				result, err = core.AttemptCapturePublication(dir, t, a[5], a[6])
			}
			if err != nil {
				return errors.New("capture unavailable or refused; inspect retained pointer only, never automatically recapture or retry publication")
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}})
	}
	return out
}
