package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/ZanzyTHEbar/faults-go"
	"github.com/spf13/cobra"

	"ocq/internal/core"
)

func readInput(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, faults.Wrap(faults.CodeStorageFailed, "read project input", err)
	}
	return data, nil
}

func projectRegister(state *string) *cobra.Command {
	cmd := queueCommand(state, "register", nil)
	f := cmd.Flags()
	file := f.String("file", "", "version-1 registration JSON file (<=16 KiB)")
	regFile := f.String("registration-file", "", "alias for --file")
	key := f.String("key", "", "registration idempotency key")
	project := f.String("project", "", "opaque product-project ID (proj-...)")
	projectAlt := f.String("project-id", "", "alias for --project")
	label := f.String("label", "", "human project label")
	expectedRev := f.Int("expected-revision", 0, "expected binding revision (0 for creation, >=1 for CAS rebind)")
	repo := f.String("repo", "", "canonical repository root")
	commitAnchor := f.String("commit-anchor", "", "40-hex commit anchor SHA")
	elephant := f.String("elephant-session", "", "exact Elephant session ID (ses_...)")
	elephantAlt := f.String("elephant-session-id", "", "alias for --elephant-session")
	expectedOCProj := f.String("expected-opencode-project-id", "", "expected OpenCode project ID")
	expectedOCDir := f.String("expected-opencode-directory", "", "expected OpenCode session directory")
	authRef := f.String("auth-ref", "", "operator authorization reference")
	handoffFile := f.String("handoff-file", "", "version-1 handoff manifest JSON file")
	cmd.RunE = queueRun(state, func(q *core.Queue, args []string) error {
		if len(args) != 0 {
			return validation("project register takes no positional arguments")
		}
		targetFile := *file
		if *regFile != "" {
			if targetFile != "" && targetFile != *regFile {
				return validation("conflicting --file and --registration-file")
			}
			targetFile = *regFile
		}
		var raw []byte
		var err error
		if targetFile != "" {
			if *key != "" || *project != "" || *projectAlt != "" || *label != "" || *repo != "" || *commitAnchor != "" || *elephant != "" || *elephantAlt != "" || *expectedOCProj != "" || *expectedOCDir != "" || *authRef != "" || *handoffFile != "" {
				return validation("project register --file cannot be combined with individual registration flags")
			}
			raw, err = readInput(targetFile)
			if err != nil {
				return err
			}
		} else {
			if *handoffFile == "" {
				return validation("project register requires --file REGISTRATION.json (or explicit registration flags with --handoff-file)")
			}
			handoff, err := readInput(*handoffFile)
			if err != nil {
				return err
			}
			id, err := projectID(*project, *projectAlt)
			if err != nil {
				return err
			}
			session := *elephant
			if *elephantAlt != "" {
				if session != "" && session != *elephantAlt {
					return validation("conflicting --elephant-session and --elephant-session-id")
				}
				session = *elephantAlt
			}
			sum := sha256.Sum256(handoff)
			rev := *expectedRev
			request := core.ProjectRegistrationRequest{
				Version: 1, Key: *key, ProjectID: id, Label: *label, ExpectedRevision: &rev,
				Repo: *repo, CommitAnchor: *commitAnchor, ElephantSessionID: session,
				ExpectedOpenCodeProjectID: *expectedOCProj, ExpectedOpenCodeDirectory: *expectedOCDir,
				AuthRef: *authRef, HandoffManifestDigest: hex.EncodeToString(sum[:]), HandoffManifest: json.RawMessage(handoff),
			}
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			if err := enc.Encode(request); err != nil {
				return err
			}
			raw = bytes.TrimSpace(buf.Bytes())
		}
		result, err := q.RegisterProject(raw)
		if err != nil {
			return err
		}
		return output(result)
	})
	return cmd
}

func projectSubmit(state *string) *cobra.Command {
	cmd := queueCommand(state, "submit", nil)
	f := cmd.Flags()
	envelopeFile := f.String("envelope-file", "", "caller-retained version-1 project submission envelope JSON file")
	fileAlias := f.String("file", "", "alias for --envelope-file")
	cmd.RunE = queueRun(state, func(q *core.Queue, args []string) error {
		target := *envelopeFile
		if *fileAlias != "" {
			if target != "" && target != *fileAlias {
				return validation("conflicting --envelope-file and --file")
			}
			target = *fileAlias
		}
		if len(args) != 0 || target == "" {
			return validation("project submit requires --envelope-file FILE")
		}
		envelope, err := readInput(target)
		if err != nil {
			return err
		}
		result, err := q.SubmitProject(envelope)
		if err != nil {
			return err
		}
		return output(result)
	})
	return cmd
}

func projectRecordResult(state *string) *cobra.Command {
	cmd := queueCommand(state, "record-result", nil)
	f := cmd.Flags()
	project := f.String("project", "", "opaque product-project ID (proj-...)")
	projectAlt := f.String("project-id", "", "alias for --project")
	job := f.String("job", "", "attributed job ID (job-...)")
	jobAlt := f.String("job-id", "", "alias for --job")
	file := f.String("manifest-file", "", "version-1 result or review_result manifest JSON file")
	fileAlt := f.String("file", "", "alias for --manifest-file")
	supersedes := f.String("supersedes", "", "optional prior result association ID (assoc-...)")
	supersedesAlt := f.String("supersedes-id", "", "alias for --supersedes")
	cmd.RunE = queueRun(state, func(q *core.Queue, args []string) error {
		if len(args) != 0 {
			return validation("project record-result takes no positional arguments")
		}
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
		target := *file
		if *fileAlt != "" {
			if target != "" && target != *fileAlt {
				return validation("conflicting --manifest-file and --file")
			}
			target = *fileAlt
		}
		supersedesID := *supersedes
		if *supersedesAlt != "" {
			if supersedesID != "" && supersedesID != *supersedesAlt {
				return validation("conflicting --supersedes and --supersedes-id")
			}
			supersedesID = *supersedesAlt
		}
		if target == "" {
			return validation("project record-result requires --project PROJECT --job JOB --manifest-file FILE [--supersedes ASSOC_ID]")
		}
		manifest, err := readInput(target)
		if err != nil {
			return err
		}
		result, err := q.RecordResult(id, jobID, manifest, supersedesID)
		if err != nil {
			return err
		}
		return output(result)
	})
	return cmd
}

func projectMailAck(state *string) *cobra.Command {
	cmd := queueCommand(state, "mail-ack", nil)
	f := cmd.Flags()
	archive := f.Bool("archive", false, "also archive the message")
	project := f.String("project", "", "opaque product-project ID (proj-...)")
	projectAlt := f.String("project-id", "", "alias for --project")
	cmd.RunE = queueRun(state, func(q *core.Queue, args []string) error {
		id, err := projectID(*project, *projectAlt)
		if err != nil {
			return err
		}
		var messageID string
		switch rest := args; {
		case len(rest) == 2 && id == "":
			id, messageID = rest[0], rest[1]
		case len(rest) == 1 && id != "":
			messageID = rest[0]
		case len(rest) == 0:
		default:
			return validation("usage: ocq [--state-dir DIR] project mail-ack [--archive] PROJECT MSG_ID")
		}
		message, err := q.AcknowledgeMail(id, messageID, *archive)
		if err != nil {
			return err
		}
		return output(message)
	})
	return cmd
}

func projectMailSend(state *string) *cobra.Command {
	cmd := queueCommand(state, "mail-send", nil)
	f := cmd.Flags()
	project := f.String("project", "", "opaque product-project ID (proj-...)")
	projectAlt := f.String("project-id", "", "alias for --project")
	key := f.String("key", "", "idempotency key")
	from := f.String("from", "", "sender: lane:<lane_id>, job:<job_id>, or integrator:<id>")
	to := f.String("to", "", "recipient: integrator:<project> or lane:<lane_id>")
	lane := f.String("lane", "", "lane ID owning the job")
	job := f.String("job", "", "job ID owned by the lane")
	kind := f.String("kind", "", "lane.blocked|lane.assigned (automatic work/review mail is not accepted here)")
	subject := f.String("subject", "", "message subject")
	body := f.String("body", "", "message body")
	manifest := f.String("manifest", "", "optional 64-hex manifest digest attachment")
	replyTo := f.String("reply-to", "", "optional prior message ID")
	fsforkPath := f.String("fsfork-path", "", "optional fsfork path override")
	cmd.RunE = queueRun(state, func(q *core.Queue, args []string) error {
		if len(args) != 0 {
			return validation("project mail-send takes no positional arguments")
		}
		id, err := projectID(*project, *projectAlt)
		if err != nil {
			return err
		}
		message, err := q.SendMail(core.MailRequest{
			ProjectID: id, Key: *key, From: *from, To: *to, LaneID: *lane, JobID: *job,
			Kind: *kind, Subject: *subject, Body: *body, Manifest: *manifest,
			ReplyTo: *replyTo, FsforkPath: *fsforkPath,
		})
		if err != nil {
			return err
		}
		return output(message)
	})
	return cmd
}

type stringSliceFlag []string

func (s *stringSliceFlag) String() string { return strings.Join(*s, ",") }
func (s *stringSliceFlag) Type() string   { return "string" }
func (s *stringSliceFlag) Set(value string) error {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return validation("--ignored-exclusion must be non-empty UTF-8 without NUL")
	}
	*s = append(*s, value)
	return nil
}

func projectCaptureResult(state *string) *cobra.Command {
	cmd := queueCommand(state, "capture-result", nil)
	f := cmd.Flags()
	project := f.String("project", "", "opaque product-project ID (proj-...)")
	projectAlt := f.String("project-id", "", "alias for --project")
	job := f.String("job", "", "attributed implementation job ID (job-...)")
	jobAlt := f.String("job-id", "", "alias for --job")
	worktree := f.String("worktree", "", "worktree path (allowed when job agent worktree is empty, or must match it)")
	var exclusions stringSliceFlag
	f.Var(&exclusions, "ignored-exclusion", "repeatable ignored/external-input exclusion note")
	supersedes := f.String("supersedes", "", "optional prior result association ID (assoc-...)")
	supersedesAlt := f.String("supersedes-id", "", "alias for --supersedes")
	cmd.RunE = queueRun(state, func(q *core.Queue, args []string) error {
		if len(args) != 0 {
			return validation("project capture-result takes no positional arguments")
		}
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
		supersedesID := *supersedes
		if *supersedesAlt != "" {
			if supersedesID != "" && supersedesID != *supersedesAlt {
				return validation("conflicting --supersedes and --supersedes-id")
			}
			supersedesID = *supersedesAlt
		}
		result, err := q.CaptureResult(core.CaptureRequest{
			ProjectID: id, JobID: jobID, Worktree: *worktree,
			Supersedes: supersedesID, IgnoredExclusions: exclusions,
		})
		if err != nil {
			return err
		}
		return output(result)
	})
	return cmd
}
