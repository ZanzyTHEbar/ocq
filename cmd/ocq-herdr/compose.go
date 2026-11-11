package main

import (
	"encoding/json"
	"os"

	"github.com/spf13/cobra"

	"ocq/internal/core"
)

// Only compose-dispatch sends; the other operations never retry it.
func composeCommands() []*cobra.Command {
	return []*cobra.Command{
		{
			Use: "compose-context PROJECT LANE MANIFEST REPLY", Args: cobra.ExactArgs(4),
			RunE: func(cmd *cobra.Command, args []string) error {
				reader, err := core.OpenReader(os.Getenv("OCQ_STATE_DIR"))
				if err != nil {
					return err
				}
				defer reader.Close()
				context, err := reader.ComposeContext(args[0], args[1], args[2], args[3])
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(context)
			},
		},
		{
			Use: "compose-receive LAUNCH", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				status, err := core.ReceiveLocalComposeIntent(os.Getenv("OCQ_STATE_DIR"), args[0])
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
			},
		},
		{
			Use: "compose-preview ID LAUNCH", Args: cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				status, err := core.PreviewLocalComposeIntent(os.Getenv("OCQ_STATE_DIR"), args[0], args[1])
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
			},
		},
		{
			Use: "compose-rendezvous LAUNCH", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				entry, err := core.LocalComposeRendezvous(os.Getenv("OCQ_STATE_DIR"), args[0])
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(entry)
			},
		},
		{
			Use: "compose-status", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				status, err := core.LocalComposeStatus(os.Getenv("OCQ_STATE_DIR"))
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
			},
		},
		{
			Use: "compose-dispatch-intent ID", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				status, err := core.MarkLocalComposeDispatchIntent(os.Getenv("OCQ_STATE_DIR"), args[0])
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
			},
		},
		{
			Use: "compose-arm ID", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				status, err := core.ArmLocalComposeIntent(os.Getenv("OCQ_STATE_DIR"), args[0])
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
			},
		},
		{
			Use: "compose-dispatch ID", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				status, err := core.DispatchLocalComposeIntent(os.Getenv("OCQ_STATE_DIR"), args[0])
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
			},
		},
		{
			Use: "compose-reconcile ID", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				status, err := core.ReconcileLocalComposeIntent(os.Getenv("OCQ_STATE_DIR"), args[0])
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
			},
		},
		{
			Use: "compose-cancel ID", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				status, err := core.CancelLocalComposeIntent(os.Getenv("OCQ_STATE_DIR"), args[0])
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
			},
		},
		{
			Use: "compose-retire ID", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := core.RetireLocalComposeIntent(os.Getenv("OCQ_STATE_DIR"), args[0]); err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(core.ComposeStatus{State: "retired", ID: args[0]})
			},
		},
	}
}
