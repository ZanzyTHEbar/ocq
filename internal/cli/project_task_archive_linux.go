package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"ocq/internal/core"
)

func projectTaskArchive(state *string) *cobra.Command {
	root := &cobra.Command{Use: "task-archive", Short: "Explicit local archive of one Main-handled standalone task; never deletes"}
	root.AddCommand(&cobra.Command{Use: "check PROJECT REQUEST SESSION", Args: cobra.ExactArgs(3),
		Short: "Read eligibility and obtain a scope-bound preview",
		RunE: func(_ *cobra.Command, args []string) error {
			out, err := core.CheckProjectTaskArchive(*state, args[0], args[1], args[2])
			return errors.Join(output(out), err)
		}})
	for _, name := range []string{"confirm", "reconcile"} {
		recovery := name == "reconcile"
		confirmed := false
		cmd := &cobra.Command{Use: name + " PROJECT REQUEST SESSION PREVIEW_SHA256", Args: cobra.ExactArgs(4),
			RunE: func(cmd *cobra.Command, args []string) error {
				out, err := core.RunProjectTaskArchive(cmd.Context(), *state, args[0], args[1], args[2], args[3], confirmed, recovery)
				return errors.Join(output(out), err)
			}}
		if recovery {
			cmd.Short = "Recover the exact archived observation by native GET only; never resends"
		} else {
			cmd.Short = "Archive once; requires exclusive control of the native session and its viewers"
			cmd.Flags().BoolVar(&confirmed, "confirm-exclusive-window", false, "Attest there are no other viewers, native writers or concurrent OCQ changes affecting this task")
		}
		root.AddCommand(cmd)
	}
	return root
}
