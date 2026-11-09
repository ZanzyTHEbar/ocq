package cli

import (
	"os"

	"github.com/spf13/cobra"

	"ocq/internal/core"
)

func projectID(primary, alternate string) (string, error) {
	if alternate != "" {
		if primary != "" && primary != alternate {
			return "", validation("conflicting --project and --project-id")
		}
		return alternate, nil
	}
	return primary, nil
}

func projectShow(state *string) *cobra.Command {
	cmd := readerCommand(state, "show PROJECT", nil)
	f := cmd.Flags()
	project := f.String("project", "", "opaque product-project ID (proj-...)")
	projectAlt := f.String("project-id", "", "alias for --project")
	cmd.RunE = readerRun(state, func(r *core.Reader, args []string) error {
		id, err := projectID(*project, *projectAlt)
		if err != nil {
			return err
		}
		if len(args) == 1 && id == "" {
			id = args[0]
		} else if len(args) != 0 {
			return validation("usage: ocq [--state-dir DIR] project show PROJECT")
		}
		out, err := r.Project(id)
		if err != nil {
			return err
		}
		return output(out)
	})
	return cmd
}

func projectResults(state *string) *cobra.Command {
	cmd := readerCommand(state, "results PROJECT", nil)
	f := cmd.Flags()
	after := f.Int64("after-sequence", 0, "cursor sequence (>= 0)")
	limit := f.Int("limit", 20, "maximum items (1..100)")
	project := f.String("project", "", "opaque product-project ID (proj-...)")
	projectAlt := f.String("project-id", "", "alias for --project")
	cmd.RunE = readerRun(state, func(r *core.Reader, args []string) error {
		id, err := projectID(*project, *projectAlt)
		if err != nil {
			return err
		}
		if len(args) == 1 && id == "" {
			id = args[0]
		} else if len(args) != 0 {
			return validation("usage: ocq [--state-dir DIR] project results [--after-sequence N] [--limit N] PROJECT")
		}
		if *after < 0 {
			return validation("project results --after-sequence must be >= 0")
		}
		if *limit < 1 || *limit > 100 {
			return validation("project results --limit must be between 1 and 100")
		}
		out, err := r.Results(id, *after, *limit)
		if err != nil {
			return err
		}
		return output(out)
	})
	return cmd
}

func projectJob(state *string) *cobra.Command {
	cmd := readerCommand(state, "job PROJECT JOB", nil)
	f := cmd.Flags()
	project := f.String("project", "", "opaque product-project ID (proj-...)")
	projectAlt := f.String("project-id", "", "alias for --project")
	job := f.String("job", "", "attributed job ID (job-...)")
	jobAlt := f.String("job-id", "", "alias for --job")
	cmd.RunE = readerRun(state, func(r *core.Reader, args []string) error {
		id, err := projectID(*project, *projectAlt)
		if err != nil {
			return err
		}
		jobID := *job
		if *jobAlt != "" {
			if jobID != "" && jobID != *jobAlt {
				return validation("conflicting --job and --job-id")
			}
			jobID = *jobAlt
		}
		if len(args) == 2 && id == "" && jobID == "" {
			id, jobID = args[0], args[1]
		} else if len(args) != 0 {
			return validation("usage: ocq [--state-dir DIR] project job PROJECT JOB")
		}
		out, err := r.Job(id, jobID)
		if err != nil {
			return err
		}
		return output(out)
	})
	return cmd
}

func projectManifest(state *string) *cobra.Command {
	cmd := readerCommand(state, "manifest PROJECT DIGEST", nil)
	f := cmd.Flags()
	project := f.String("project", "", "opaque product-project ID (proj-...)")
	projectAlt := f.String("project-id", "", "alias for --project")
	digest := f.String("digest", "", "64-hex SHA-256 manifest digest")
	cmd.RunE = readerRun(state, func(r *core.Reader, args []string) error {
		id, err := projectID(*project, *projectAlt)
		if err != nil {
			return err
		}
		if len(args) == 2 && id == "" && *digest == "" {
			id, *digest = args[0], args[1]
		} else if len(args) != 0 {
			return validation("usage: ocq [--state-dir DIR] project manifest PROJECT DIGEST")
		}
		canonical, err := r.Manifest(id, *digest)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(canonical)
		return err
	})
	return cmd
}

func projectLanes(state *string) *cobra.Command {
	cmd := readerCommand(state, "lanes PROJECT", nil)
	f := cmd.Flags()
	project := f.String("project", "", "opaque product-project ID (proj-...)")
	projectAlt := f.String("project-id", "", "alias for --project")
	cmd.RunE = readerRun(state, func(r *core.Reader, args []string) error {
		id, err := projectID(*project, *projectAlt)
		if err != nil {
			return err
		}
		if len(args) == 1 && id == "" {
			id = args[0]
		} else if len(args) != 0 {
			return validation("usage: ocq [--state-dir DIR] project lanes PROJECT")
		}
		out, err := r.Lanes(id)
		if err != nil {
			return err
		}
		return output(out)
	})
	return cmd
}

func projectInbox(state *string) *cobra.Command {
	cmd := readerCommand(state, "inbox PROJECT", nil)
	f := cmd.Flags()
	recipient := f.String("recipient", "", "integrator:<project>, lane:<lane_id>, or all")
	unread := f.Bool("unread-only", false, "only messages without read_at")
	after := f.Int64("after-sequence", 0, "cursor sequence (>= 0)")
	limit := f.Int("limit", 20, "maximum messages (1..100)")
	project := f.String("project", "", "opaque product-project ID (proj-...)")
	projectAlt := f.String("project-id", "", "alias for --project")
	cmd.RunE = readerRun(state, func(r *core.Reader, args []string) error {
		id, err := projectID(*project, *projectAlt)
		if err != nil {
			return err
		}
		if len(args) == 1 && id == "" {
			id = args[0]
		} else if len(args) != 0 {
			return validation("usage: ocq [--state-dir DIR] project inbox [--recipient RECIPIENT] [--unread-only] [--after-sequence N] [--limit N] PROJECT")
		}
		out, err := r.Inbox(id, *recipient, *unread, *after, *limit)
		if err != nil {
			return err
		}
		return output(out)
	})
	return cmd
}

func projectVerifyWorktree(state *string) *cobra.Command {
	cmd := readerCommand(state, "verify-worktree", nil)
	f := cmd.Flags()
	project := f.String("project", "", "opaque product-project ID (proj-...)")
	projectAlt := f.String("project-id", "", "alias for --project")
	job := f.String("job", "", "attributed implementation job ID (job-...)")
	jobAlt := f.String("job-id", "", "alias for --job")
	manifest := f.String("manifest", "", "optional 64-hex result manifest digest (defaults to latest result association)")
	manifestAlt := f.String("manifest-digest", "", "alias for --manifest")
	digestAlt := f.String("digest", "", "alias for --manifest")
	worktree := f.String("worktree", "", "worktree path when job.Agent.Worktree is empty")
	cmd.RunE = readerRun(state, func(r *core.Reader, args []string) error {
		id, err := projectID(*project, *projectAlt)
		if err != nil {
			return err
		}
		jobID := *job
		if *jobAlt != "" {
			if jobID != "" && jobID != *jobAlt {
				return validation("conflicting --job and --job-id")
			}
			jobID = *jobAlt
		}
		if len(args) == 2 && id == "" && jobID == "" {
			id, jobID = args[0], args[1]
		} else if len(args) != 0 {
			return validation("project verify-worktree takes no positional arguments beyond optional PROJECT JOB")
		}
		digest := *manifest
		for _, alt := range []string{*manifestAlt, *digestAlt} {
			if alt != "" {
				if digest != "" && digest != alt {
					return validation("conflicting --manifest / --manifest-digest / --digest flags")
				}
				digest = alt
			}
		}
		out, err := r.VerifyWorktree(core.WorktreeCheckRequest{
			ProjectID: id, JobID: jobID, ManifestDigest: digest, Worktree: *worktree,
		})
		if err != nil {
			return err
		}
		return output(out)
	})
	return cmd
}
