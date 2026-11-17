package main

import (
	"errors"

	"github.com/spf13/cobra"
	"ocq/internal/core"
)

func captureReceiptCommands() []*cobra.Command {
	var commands []*cobra.Command
	for _, preview := range []bool{false, true} {
		name := "capture-receipt"
		if preview {
			name += "-preview"
		}
		preview := preview
		commands = append(commands, &cobra.Command{Use: name + " SPACE QUEUE PROJECT JOB SEQUENCE MANIFEST_SHA256", Args: cobra.ExactArgs(6), RunE: func(cmd *cobra.Command, a []string) error {
			dir, err := existingBridgeStateDir()
			if err != nil {
				return err
			}
			r, err := core.OpenReader(dir)
			if err != nil {
				return err
			}
			defer r.Close()
			t := core.ResultPublicationTarget{CommandResolutionTarget: core.CommandResolutionTarget{Machine: "local", Space: a[0], Queue: a[1], Job: a[3], Sequence: a[4]}, Project: a[2]}
			status, exact, err := r.RecordedCaptureReceipt(t, a[5])
			if err != nil {
				return errors.New("exact original capture receipt unavailable")
			}
			if preview {
				return writeWork(cmd, map[string]any{"status": status, "exact": exact})
			}
			return writeWork(cmd, status)
		}})
	}
	return commands
}
