package cli

import (
	"encoding/json"
	"github.com/spf13/cobra"
	"ocq/internal/core"
)

func projectClient(state *string) *cobra.Command {
	root := &cobra.Command{Use: "client", Short: "Journal one exact native HC client navigation; no target creation or steering"}
	packaging := &cobra.Command{Use: "check-package", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		out, err := core.InspectNativeClientPackage()
		if err != nil {
			return err
		}
		return output(out)
	}}
	prepare := &cobra.Command{Use: "prepare PROJECT", Args: cobra.ExactArgs(1)}
	id := prepare.Flags().String("navigation-id", "", "caller-known nonsecret navigation operation ID")
	kind := prepare.Flags().String("kind", "", "worker or integrator; explicit")
	space := prepare.Flags().String("space", "", "exact selected native space")
	work := prepare.Flags().String("work-request", "", "exact admitted worker request ID (worker kind only)")
	prepare.RunE = func(_ *cobra.Command, args []string) error {
		raw, err := json.Marshal(core.ClientPrepareRequest{Protocol: core.ClientProtocol, ProjectID: args[0], NavigationID: *id, TargetKind: *kind, WorkRequestID: *work, Space: *space})
		if err != nil {
			return err
		}
		out, err := core.PrepareProjectClient(*state, raw)
		if err != nil {
			return err
		}
		return output(out)
	}
	status := &cobra.Command{Use: "status PROJECT POINTER", Args: cobra.ExactArgs(2), RunE: func(_ *cobra.Command, args []string) error {
		out, err := core.InspectProjectClient(*state, args[0], args[1])
		if err != nil {
			return err
		}
		return output(out)
	}}
	for _, attempt := range []bool{false, true} {
		attempt := attempt
		name := "arm"
		if attempt {
			name = "attempt"
		}
		cmd := &cobra.Command{Use: name + " PROJECT POINTER", Args: cobra.ExactArgs(2), RunE: func(_ *cobra.Command, args []string) error {
			c, err := core.ClientCLIContext(*state, args[0], args[1])
			if err != nil {
				return err
			}
			var out core.ClientStatus
			if attempt {
				out, err = core.AttemptProjectClient(*state, args[0], args[1], c)
			} else {
				out, err = core.ArmProjectClient(*state, args[0], args[1], c)
			}
			if err != nil {
				return err
			}
			return output(out)
		}}
		root.AddCommand(cmd)
	}
	record := &cobra.Command{Use: "record PROJECT POINTER", Args: cobra.ExactArgs(2)}
	file := record.Flags().String("receipt-file", "", "private exact returned+observed HC receipt JSON, never guessed IDs")
	record.RunE = func(_ *cobra.Command, args []string) error {
		text, err := workInput(*file, 16<<10)
		if err != nil {
			return err
		}
		receipt, err := core.DecodeClientTerminalReceipt([]byte(text))
		if err != nil {
			return err
		}
		c, err := core.ClientCLIContext(*state, args[0], args[1])
		if err != nil {
			return err
		}
		out, err := core.RecordProjectClient(*state, args[0], args[1], c, receipt)
		if err != nil {
			return err
		}
		return output(out)
	}
	run := &cobra.Command{Use: "run PROJECT POINTER", Args: cobra.ExactArgs(2), RunE: func(_ *cobra.Command, args []string) error { return core.RunProjectClient(*state, args[0], args[1]) }}
	root.AddCommand(prepare, status, record, run, packaging)
	return root
}
