package cli

import (
	"errors"
	"io"

	"github.com/spf13/cobra"

	"ocq/internal/core"
)

func projectMainHandled(state *string) *cobra.Command {
	root := &cobra.Command{Use: "main-handled", Short: "Explicit schema opt-in or read back one exact Main handling receipt"}
	root.AddCommand(
		&cobra.Command{Use: "upgrade", Args: cobra.NoArgs, Short: "Explicitly install schema 8 with dispatcher stopped",
			RunE: func(_ *cobra.Command, _ []string) error {
				return core.UpgradeProjectMainHandled(*state)
			}},
		&cobra.Command{Use: "receipt PROJECT REQUEST INBOX SESSION MESSAGE CALL", Args: cobra.ExactArgs(6),
			Short: "Read the exact immutable receipt; never execute or retry",
			RunE: func(_ *cobra.Command, args []string) error {
				ptr := core.MainHandledPointer{RequestID: args[1], InboxID: args[2]}
				receipt, err := core.ReadProjectMainHandled(*state, args[0], ptr, args[3], args[4], args[5])
				if err != nil {
					return err
				}
				return output(receipt)
			}},
		&cobra.Command{Use: "invoke-tool PROJECT", Args: cobra.ExactArgs(1), Hidden: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				raw, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), core.MainHandledInputLimit+1))
				if err != nil || len(raw) > core.MainHandledInputLimit {
					return errors.New("Main handled helper stdin unavailable or exceeds bound")
				}
				got, err := core.RecordProjectMainHandled(cmd.Context(), *state, args[0], raw)
				if err != nil {
					return err
				}
				return output(got)
			}},
	)
	return root
}
