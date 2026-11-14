package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"ocq/internal/core"
)

func projectCommand(state *string) *cobra.Command {
	cmd := &cobra.Command{Use: "project", Short: "Project control plane", Args: func(_ *cobra.Command, args []string) error {
		if len(args) != 0 {
			return validation(fmt.Sprintf("unknown project subcommand %q", args[0]))
		}
		return nil
	}, RunE: func(*cobra.Command, []string) error {
		return validation("usage: ocq [--state-dir DIR] project register|submit|record-result|capture-result|verify-worktree|lanes|inbox|mail-send|mail-ack|list|board|show|results|job|manifest ...")
	}}
	cmd.AddCommand(projectPalette(state)...)
	return cmd
}

func projectPalette(state *string) []*cobra.Command {
	list := readerCommand(state, "list", func(r *core.Reader, args []string) error {
		if len(args) != 0 {
			return validation("usage: ocq [--state-dir DIR] project list")
		}
		projects, err := r.Projects()
		if err != nil {
			return err
		}
		return output(projects)
	})
	return []*cobra.Command{
		projectRegister(state), projectSubmit(state), projectRecordResult(state), projectCaptureResult(state),
		projectMailAck(state), projectMailSend(state), list, projectBoard(state), projectShow(state),
		projectResults(state), projectJob(state), projectManifest(state), projectLanes(state),
		projectInbox(state), projectVerifyWorktree(state),
		projectWork(state),
		projectMain(state), projectClient(state),
	}
}

func readerCommand(state *string, use string, run func(*core.Reader, []string) error) *cobra.Command {
	cmd := &cobra.Command{Use: use, RunE: readerRun(state, run)}
	cmd.Flags().SetInterspersed(false)
	return cmd
}

func readerRun(state *string, run func(*core.Reader, []string) error) func(*cobra.Command, []string) error {
	return func(_ *cobra.Command, args []string) error {
		reader, err := core.OpenReader(*state)
		if err != nil {
			return err
		}
		defer reader.Close()
		return run(reader, args)
	}
}
