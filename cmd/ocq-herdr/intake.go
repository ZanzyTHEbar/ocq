package main

import (
	"encoding/json"
	"os"

	"github.com/spf13/cobra"

	"ocq/internal/core"
)

// Request bytes travel only through the authenticated local connection.
func intakeCommands() []*cobra.Command {
	state := func() string { return os.Getenv("OCQ_STATE_DIR") }
	command := func(use string, n int, run func([]string) (any, error)) *cobra.Command {
		return &cobra.Command{Use: use, Args: cobra.ExactArgs(n), RunE: func(cmd *cobra.Command, args []string) error {
			result, err := run(args)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}}
	}
	return []*cobra.Command{
		command("intake-implementation-draft LAUNCH", 1, func(a []string) (any, error) { return core.DraftLocalProjectImplementation(state(), a[0]) }),
		command("intake-review-draft LAUNCH", 1, func(a []string) (any, error) { return core.DraftLocalProjectReview(state(), a[0]) }),
		command("intake-registration-draft LAUNCH", 1, func(a []string) (any, error) { return core.DraftLocalProjectRegistration(state(), a[0]) }),
		command("intake-receive KIND LAUNCH", 2, func(a []string) (any, error) { return core.ReceiveLocalProjectIntake(state(), a[0], a[1]) }),
		command("intake-receive-text KIND LAUNCH", 2, func(a []string) (any, error) { return core.ReceiveLocalProjectIntakeText(state(), a[0], a[1]) }),
		command("intake-preview ID LAUNCH", 2, func(a []string) (any, error) { return core.PreviewLocalProjectIntake(state(), a[0], a[1]) }),
		command("intake-review-preview ID LAUNCH", 2, func(a []string) (any, error) { return core.ReviewLocalProjectIntake(state(), a[0], a[1]) }),
		command("intake-rendezvous LAUNCH", 1, func(a []string) (any, error) { return core.LocalProjectIntakeRendezvous(state(), a[0]) }),
		command("intake-status", 0, func(_ []string) (any, error) { return core.LocalProjectIntakeStatus(state()) }),
		command("intake-arm ID", 1, func(a []string) (any, error) { return core.ArmLocalProjectIntake(state(), a[0]) }),
		command("intake-dispatch ID", 1, func(a []string) (any, error) { return core.DispatchLocalProjectIntake(state(), a[0]) }),
		command("intake-reconcile ID", 1, func(a []string) (any, error) { return core.ReconcileLocalProjectIntake(state(), a[0]) }),
		command("intake-cancel ID", 1, func(a []string) (any, error) { return core.CancelLocalProjectIntake(state(), a[0]) }),
		command("intake-retire ID", 1, func(a []string) (any, error) {
			if err := core.RetireLocalProjectIntake(state(), a[0]); err != nil {
				return nil, err
			}
			return core.ProjectIntakeStatus{State: "retired", ID: a[0]}, nil
		}),
	}
}
