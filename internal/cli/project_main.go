package cli

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"ocq/internal/core"
)

func mainPromptFile(path string) (string, error) {
	if path != "-" {
		return workInput(path, 16<<10)
	}
	b, err := io.ReadAll(io.LimitReader(os.Stdin, 16<<10+1))
	if len(b) > 16<<10 {
		return "", errors.New("Main prompt exceeds 16 KiB")
	}
	if !utf8.Valid(b) {
		return "", errors.New("Main prompt must be UTF-8; no replacement/normalization")
	}
	return string(b), err
}

func projectMain(state *string) *cobra.Command {
	root := &cobra.Command{Use: "main", Short: "Persistent project Main/Integrator on its explicit canonical MAIN checkout; never a queued worker"}
	prepare := &cobra.Command{Use: "prepare [PROJECT]", Args: cobra.MaximumNArgs(1)}
	file := prepare.Flags().String("file", "", "private exact ocq-main-v1 JSON; do not mix with form flags")
	id := prepare.Flags().String("operation-id", "", "caller-known exact Main operation ID")
	mode := prepare.Flags().String("mode", "", "create, adopt, refresh, send or reopen (explicit)")
	repo := prepare.Flags().String("repo", "", "canonical primary checkout for initial setup")
	branch := prepare.Flags().String("main-branch", "", "full configured refs/heads/... branch; never switched by OCQ")
	revision := prepare.Flags().String("expected-revision", "", "explicit registration CAS revision (create=0)")
	label := prepare.Flags().String("label", "", "explicit initial/existing project label")
	profile := prepare.Flags().String("profile-file", "", "immutable explicit Main integrator role/agent/model/ordered permissions")
	role := prepare.Flags().String("role-file", "", "explicit ocq-main-role-v1 implementation/review profile selection; retained instructions are previewed")
	prompt := prepare.Flags().String("prompt-file", "", "private initial/send prompt file, or - for stdin; NEVER prompt argv")
	prepare.RunE = func(cmd *cobra.Command, args []string) error {
		var raw []byte
		if *file != "" {
			if len(args) != 0 || cmd.Flags().NFlag() != 1 {
				return validation("--file cannot be mixed with Main form flags or PROJECT")
			}
			value, err := workInput(*file, core.WorkRequestBytes)
			if err != nil {
				return err
			}
			raw = []byte(value)
		} else {
			if len(args) != 1 {
				return validation("Main prepare requires PROJECT or --file")
			}
			in := core.MainPrepareRequest{Protocol: core.MainProtocol, ProjectID: args[0], OperationID: *id, Mode: *mode, Repo: *repo, MainBranch: *branch, ExpectedRevision: *revision, Label: *label}
			if *profile != "" {
				value, err := workInput(*profile, 16<<10)
				if err != nil {
					return err
				}
				in.Profile = value
			}
			if *role != "" {
				value, err := workInput(*role, 48<<10)
				if err != nil {
					return err
				}
				in.Role = value
			}
			if *prompt != "" {
				value, err := mainPromptFile(*prompt)
				if err != nil {
					return err
				}
				in.Prompt = value
			}
			var err error
			raw, err = json.Marshal(in)
			if err != nil {
				return err
			}
		}
		out, err := core.PrepareProjectMain(*state, raw)
		if err != nil {
			return err
		}
		return output(out)
	}
	list := &cobra.Command{Use: "list PROJECT", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		out, err := core.ListProjectMain(*state, args[0])
		if err != nil {
			return err
		}
		return output(out)
	}}
	for _, live := range []bool{false, true} {
		live := live
		name := "status"
		if live {
			name = "recover"
		}
		cmd := &cobra.Command{Use: name + " PROJECT [OPERATION]", Args: cobra.RangeArgs(1, 2), RunE: func(_ *cobra.Command, args []string) error {
			id := ""
			if len(args) == 2 {
				id = args[1]
			}
			var out core.MainStatus
			var err error
			if live {
				out, err = core.RecoverProjectMain(*state, args[0], id)
			} else {
				out, err = core.InspectProjectMain(*state, args[0], id)
			}
			if err != nil {
				return err
			}
			return output(out)
		}}
		root.AddCommand(cmd)
	}
	preview := &cobra.Command{Use: "preview PROJECT OPERATION", Args: cobra.ExactArgs(2), RunE: func(_ *cobra.Command, args []string) error {
		c, err := core.MainCLIContext(*state, args[0], args[1])
		if err != nil {
			return err
		}
		out, err := core.PreviewProjectMain(*state, args[0], args[1], c)
		if err != nil {
			return err
		}
		return output(out)
	}}
	start := &cobra.Command{Use: "start PROJECT OPERATION", Args: cobra.ExactArgs(2)}
	digest := start.Flags().String("preview-sha256", "", "exact fully reviewed Main preview and CLI action context")
	start.RunE = func(_ *cobra.Command, args []string) error {
		c, err := core.MainCLIContext(*state, args[0], args[1])
		if err != nil {
			return err
		}
		out, err := core.StartProjectMain(*state, args[0], args[1], *digest, c)
		if err != nil {
			return err
		}
		return output(out)
	}
	roleRead := &cobra.Command{Use: "role PROJECT", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		out, err := core.ProjectMainRole(*state, args[0])
		if err != nil {
			return err
		}
		return output(out)
	}}
	root.AddCommand(prepare, list, preview, start, roleRead)
	return root
}
