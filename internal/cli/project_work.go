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

func workInput(path string, limit int64) (string, error) {
	if path == "" {
		return "", errors.New("explicit input file required")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(b)) > limit {
		return "", errors.New("work input exceeds bound")
	}
	if !utf8.Valid(b) {
		return "", errors.New("work input must be UTF-8; no replacement/normalization")
	}
	return string(b), nil
}

func projectWork(state *string) *cobra.Command {
	root := &cobra.Command{Use: "work", Short: "Prepare, preview and pointer-confirm parent-driven project work (no scheduler)"}
	wait := &cobra.Command{Use: "wait PROJECT REQUEST", Args: cobra.ExactArgs(2), Short: "Read-only existing observation lease; timeout is pending, not completion"}
	timeout := wait.Flags().Int("timeout-seconds", 20, "bounded 1..20 second lease; renew pending under the same goal")
	wait.RunE = func(cmd *cobra.Command, args []string) error {
		out, err := core.WaitProjectWork(cmd.Context(), *state, args[0], args[1], *timeout)
		if err != nil {
			return err
		}
		return output(out)
	}
	root.AddCommand(wait)
	for _, kind := range []string{"implementation", "capture", "review", "review_result"} {
		kind := kind
		name := map[string]string{"implementation": "prepare", "capture": "capture-prepare", "review": "review-prepare", "review_result": "review-result-prepare"}[kind]
		cmd := &cobra.Command{Use: name + " PROJECT", Args: cobra.ExactArgs(1)}
		id := cmd.Flags().String("request-id", "", "caller-known nonsecret operation identity; retain for recovery")
		revision := cmd.Flags().String("binding-revision", "", "exact selected project binding revision (decimal)")
		var profile, brief, parent, criteria, rules, result *string
		switch kind {
		case "implementation":
			profile = cmd.Flags().String("profile-file", "", "explicit immutable agent/model/memory/ordered permission profile")
			brief = cmd.Flags().String("brief-file", "", "plain UTF-8 task brief (<=16 KiB)")
		case "capture":
			parent = cmd.Flags().String("implementation", "", "exact implementation work request ID with proven terminal execution")
		case "review":
			parent = cmd.Flags().String("capture", "", "exact confirmed capture request ID, never latest result")
			profile = cmd.Flags().String("profile-file", "", "explicit read-only reviewer execution profile")
			criteria = cmd.Flags().String("criteria-file", "", "plain review acceptance criteria")
			rules = cmd.Flags().String("rules-file", "", "plain review rules (including expected final report format)")
		case "review_result":
			parent = cmd.Flags().String("review", "", "exact terminal review request ID")
			result = cmd.Flags().String("result-file", "", "strict authored verdict/findings/limitations JSON; no prose approval parsing")
		}
		cmd.RunE = func(_ *cobra.Command, args []string) error {
			in := core.WorkPrepareRequest{Protocol: core.WorkProtocol, ProjectID: args[0], RequestID: *id, Kind: kind, BindingRevision: *revision}
			if parent != nil {
				in.ParentRequestID = *parent
			}
			for _, item := range []struct {
				path  *string
				dest  *string
				limit int64
			}{{profile, &in.Profile, 16 << 10}, {brief, &in.Brief, 16 << 10}, {criteria, &in.Criteria, 16 << 10}, {rules, &in.Rules, 16 << 10}, {result, &in.ReviewResult, 16 << 10}} {
				if item.path != nil && !(kind == "review_result" && *item.path == "") {
					value, err := workInput(*item.path, item.limit)
					if err != nil {
						return err
					}
					*item.dest = value
				}
			}
			raw, err := json.Marshal(in)
			if err != nil {
				return err
			}
			status, err := core.PrepareProjectWork(*state, raw)
			if err != nil {
				return err
			}
			return output(status)
		}
		root.AddCommand(cmd)
	}
	list := &cobra.Command{Use: "list PROJECT", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		value, err := core.ListProjectWork(*state, args[0])
		if err != nil {
			return err
		}
		return output(value)
	}}
	status := &cobra.Command{Use: "status PROJECT REQUEST", Args: cobra.ExactArgs(2), RunE: func(_ *cobra.Command, args []string) error {
		value, err := core.InspectProjectWork(*state, args[0], args[1])
		if err != nil {
			return err
		}
		return output(value)
	}}
	preview := &cobra.Command{Use: "preview PROJECT REQUEST", Args: cobra.ExactArgs(2), RunE: func(_ *cobra.Command, args []string) error {
		c, err := core.WorkCLIContext(*state, args[0])
		if err != nil {
			return err
		}
		value, err := core.PreviewProjectWork(*state, args[0], args[1], c)
		if err != nil {
			return err
		}
		return output(value)
	}}
	confirm := &cobra.Command{Use: "confirm PROJECT REQUEST", Args: cobra.ExactArgs(2)}
	digest := confirm.Flags().String("preview-sha256", "", "exact reviewed CLI preview digest; bridge previews are not CLI authorizations")
	confirm.RunE = func(_ *cobra.Command, args []string) error {
		c, err := core.WorkCLIContext(*state, args[0])
		if err != nil {
			return err
		}
		value, err := core.ConfirmProjectWork(*state, args[0], args[1], *digest, c)
		if err != nil {
			return err
		}
		return output(value)
	}
	handoff := &cobra.Command{Use: "handoff PROJECT REVIEW_RESULT_REQUEST", Args: cobra.ExactArgs(2), RunE: func(_ *cobra.Command, args []string) error {
		value, err := core.ProjectWorkHandoff(*state, args[0], args[1])
		if err != nil {
			return err
		}
		return output(value)
	}}
	report := &cobra.Command{Use: "output PROJECT REQUEST", Args: cobra.ExactArgs(2), RunE: func(_ *cobra.Command, args []string) error {
		value, err := core.ReadProjectWorkOutput(*state, args[0], args[1])
		if err != nil {
			return err
		}
		return output(value)
	}}
	root.AddCommand(list, status, preview, confirm, handoff, report)
	return root
}
