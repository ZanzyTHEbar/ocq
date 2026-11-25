package main

import (
	"errors"

	"github.com/spf13/cobra"
	"ocq/internal/core"
)

func sessionDeleteCommands() []*cobra.Command {
	var commands []*cobra.Command
	for _, name := range []string{"session-delete-check-v1", "session-delete-v1"} {
		commit := name == "session-delete-v1"
		n, use := 9, name+" SPACE QUEUE PROJECT SESSION PEER FOLDER STORE IMAGE WORK_OR_EMPTY"
		if commit {
			n++
			use += " PREVIEW_SHA256"
		}
		commands = append(commands, &cobra.Command{Use: use, Args: cobra.ExactArgs(n), RunE: func(cmd *cobra.Command, a []string) error {
			// Only the endpoint dispatches confirmation. A matching local CLI
			// invocation is sufficient for read-only candidate checks.
			if commit {
				if peer, _ := cmd.Context().Value(runtimePeerKey{}).(string); peer == "" || peer != a[4] {
					return errors.New("session deletion requires the owning runtime endpoint")
				}
			}
			b, err := compatibilityContext(cmd.Context(), a[0], a[5])
			pin, flagErr := cmd.Flags().GetString("work-observation-context")
			if err != nil || flagErr != nil || b.Queue != a[1] || b.ObservationStoreContext != a[6] ||
				b.Image != a[7] || b.ObservationContext != pin || !workSpace.MatchString(a[0]) {
				return errors.New("session deletion scope changed")
			}
			dir, err := existingBridgeStateDir()
			if err != nil {
				return err
			}
			c := core.WorkActionContext{QueueIdentity: a[1], StoreContext: a[6], Folder: a[5]}
			var out core.SessionDeleteView
			if commit {
				out, err = core.DeleteProjectSession(dir, a[2], a[3], a[8], a[9], c)
			} else {
				out, err = core.CheckProjectSessionDelete(dir, a[2], a[3], a[8], c)
			}
			if err != nil {
				return err
			}
			return writeWork(cmd, out)
		}})
	}
	return commands
}
