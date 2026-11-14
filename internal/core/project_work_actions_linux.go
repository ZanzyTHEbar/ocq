package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"ocq/internal/core/dbquery"
)

// Validate only current queue/store/folder here. Peer and image are independently
// checked by the entrypoint and frozen into the authorization, not inferred from
// an unrelated executable or trusted as a bearer capability.
func validateWorkContext(r *Reader, b ProjectBinding, c WorkActionContext) error {
	if c.Protocol != WorkProtocol || c.Peer == "" || len(c.Peer) > 256 || c.Space == "" || len(c.Space) > 256 || c.Folder != b.RepoRoot || c.Action != "confirm" || (c.ImageRole != "cli" && c.ImageRole != "bridge") || !isValidHexLen(c.ImageSHA256, 64) {
		return errors.New("exact work caller context required")
	}
	identity, err := notificationQueueIdentity(r.store)
	if err != nil {
		return err
	}
	info, err := os.Lstat(filepath.Join(r.store.dir, "queue.db"))
	if err != nil {
		return err
	}
	var schema int
	if err = r.store.db.QueryRow("PRAGMA user_version").Scan(&schema); err != nil {
		return err
	}
	_, store := observationContexts(r.store.dir, info, identity, schema)
	if identity != c.QueueIdentity || store != c.StoreContext {
		return errors.New("work queue/store context changed")
	}
	return nil
}

// WorkCLIContext pins the CLI's own image. A bridge MUST construct its own
// context with image_role=bridge rather than calling this function.
func WorkCLIContext(dir, project string) (WorkActionContext, error) {
	r, err := OpenReader(dir)
	if err != nil {
		return WorkActionContext{}, err
	}
	defer r.Close()
	b, err := workBinding(r, project)
	if err != nil {
		return WorkActionContext{}, err
	}
	identity, err := notificationQueueIdentity(r.store)
	if err != nil {
		return WorkActionContext{}, err
	}
	info, err := os.Lstat(filepath.Join(r.store.dir, "queue.db"))
	if err != nil {
		return WorkActionContext{}, err
	}
	var schema int
	if err = r.store.db.QueryRow("PRAGMA user_version").Scan(&schema); err != nil {
		return WorkActionContext{}, err
	}
	_, store := observationContexts(r.store.dir, info, identity, schema)
	exe, err := os.Executable()
	if err != nil {
		return WorkActionContext{}, err
	}
	_, _, hash, err := dispatcherImage(exe)
	if err != nil {
		return WorkActionContext{}, err
	}
	return WorkActionContext{WorkProtocol, "local", "cli", b.RepoRoot, identity, store, hash, "cli", "confirm"}, nil
}

func PreviewProjectWork(dir, project, id string, c WorkActionContext) (WorkPreview, error) {
	if !isValidProjectID(project) || !workID(id) {
		return WorkPreview{}, errors.New("invalid work pointer")
	}
	j, err := openLocalJournal(dir, workJournal(project))
	if err != nil {
		return WorkPreview{}, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return WorkPreview{}, err
	}
	r, err := OpenReader(dir)
	if err != nil {
		return WorkPreview{}, err
	}
	defer r.Close()
	rec, p, _, err := workLoad(r, j.dir, project, id)
	if err != nil {
		return WorkPreview{}, err
	}
	if p.Payload == "" {
		return WorkPreview{}, errors.New("preparation unresolved; never retry capture")
	}
	if err = validateWorkContext(r, rec.Binding, c); err != nil {
		return WorkPreview{}, err
	}
	status, err := workInspect(r, j.dir, project, id)
	if err != nil {
		return WorkPreview{}, err
	}
	if status.State == "blocked" {
		return WorkPreview{}, errors.New("work intent blocked")
	}
	out := WorkPreview{Protocol: WorkProtocol, Status: status, Context: c, Binding: rec.Binding, InputJSON: rec.Input, Payload: p.Payload, PayloadSHA256: p.Digest}
	raw, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	out.PreviewSHA256 = sha256Hex(raw)
	if err = workOutputBound(out); err != nil {
		return out, err
	}
	name := id + "." + out.PreviewSHA256 + ".preview.json"
	if existing, e := workRead(j.dir, name); e == nil {
		var saved WorkPreview
		if json.Unmarshal(existing, &saved) != nil || !jsonEqual(saved, out) {
			return out, errors.New("retained preview mismatch")
		}
		return saved, nil
	} else if !errors.Is(e, syscall.ENOENT) {
		return out, e
	}
	entries, err := workEntries(j.dir)
	if err != nil {
		return out, err
	}
	if len(entries) >= workFileLimit {
		return out, errors.New("work journal preview bound reached")
	}
	if err = j.create(name, out); err != nil {
		return out, err
	}
	return out, workOutputBound(out)
}

func ConfirmProjectWork(dir, project, id, digest string, c WorkActionContext) (WorkStatus, error) {
	if !isValidProjectID(project) || !workID(id) || !isValidHexLen(digest, 64) {
		return WorkStatus{}, errors.New("invalid work confirmation pointer")
	}
	j, err := openLocalJournal(dir, workJournal(project))
	if err != nil {
		return WorkStatus{}, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return WorkStatus{}, err
	}
	r, err := OpenReader(dir)
	if err != nil {
		return WorkStatus{}, err
	}
	defer r.Close()
	rec, p, in, err := workLoad(r, j.dir, project, id)
	if err != nil {
		return WorkStatus{}, err
	}
	if err = validateWorkContext(r, rec.Binding, c); err != nil {
		return WorkStatus{}, err
	}
	b, err := workRead(j.dir, id+"."+digest+".preview.json")
	if err != nil {
		return WorkStatus{}, err
	}
	var preview WorkPreview
	if _, err = decodeStrictJSONObject(b, WorkResponseBytes, &preview); err != nil {
		return WorkStatus{}, err
	}
	if preview.PreviewSHA256 != digest || preview.Context != c || preview.InputJSON != rec.Input || preview.Payload != p.Payload || preview.PayloadSHA256 != p.Digest || preview.Binding != rec.Binding {
		return WorkStatus{}, errors.New("exact retained preview/context mismatch")
	}
	preview.PreviewSHA256 = ""
	b, err = json.Marshal(preview)
	if err != nil || sha256Hex(b) != digest {
		return WorkStatus{}, errors.New("preview digest mismatch")
	}
	status, err := workInspect(r, j.dir, project, id)
	if err != nil {
		return status, err
	}
	// Receipt first, then terminal journal state. No retry of armed/uncertain work,
	// even if the original receipt is currently absent.
	if status.State != "prepared" {
		return status, nil
	}
	current, err := workBinding(r, project)
	if err != nil {
		return status, err
	}
	if current != rec.Binding {
		return status, errors.New("binding changed since preparation")
	}
	if in.Kind == "capture" || in.Kind == "review_result" {
		parent, err := workInspect(r, j.dir, project, in.ParentRequestID)
		if err != nil || parent.Job == nil || !parent.Job.TerminalProven {
			return status, errors.New("terminal parent proof no longer available")
		}
	}
	claim := workClaim{rec.Digest, p.Digest, digest, c}
	if err = j.syncFile(id + ".started.json"); err != nil {
		return status, err
	}
	if err = j.syncFile(id + ".intent.json"); err != nil {
		return status, err
	}
	if err = j.syncFile(id + "." + digest + ".preview.json"); err != nil {
		return status, err
	}
	if err = j.create(id+".armed.json", claim); err != nil {
		return status, err
	}
	if err = j.create(id+".attempt.json", claim); err != nil {
		return status, err
	}
	// All effects below have one irreversible attempt marker. Return/errors never
	// authorize re-entry; recovery uses the exact immutable SQLite receipt only.
	if in.Kind == "implementation" || in.Kind == "review" {
		w, e := OpenExistingProjectIntake(dir)
		if e == nil {
			_, e = w.SubmitProject([]byte(p.Payload))
			e = errors.Join(e, w.Close())
		}
		err = e
	} else {
		var payload ResultPublicationPayload
		_, err = decodeStrictJSONObject([]byte(p.Payload), composeLimit, &payload)
		if err == nil {
			w, e := OpenExistingResultPublisher(dir)
			if e == nil {
				if in.Kind == "capture" {
					var m parsedManifest
					m, e = parseManifestBytes([]byte(payload.Manifest), "result", project)
					if e == nil && (m.Source != "machine_capture" || m.Result == nil) {
						e = errors.New("retained machine snapshot required")
					}
					if e == nil {
						var a ProjectResultAssociation
						e = w.store.publishCapture(preparedCapture{*m.Result, payload.Supersedes}, &a, workPublicationFence(w.store, payload, rec.Binding), false, false)
					}
				} else {
					_, e = w.store.recordResult(project, payload.Target.Job, []byte(payload.Manifest), payload.Supersedes, workPublicationFence(w.store, payload, rec.Binding), false)
				}
				e = errors.Join(e, w.Close())
			}
			err = e
		}
	}
	observed, observeErr := workInspect(r, j.dir, project, id)
	if observeErr == nil && observed.State == "recorded_observed" {
		return observed, nil
	}
	return observed, errors.Join(err, observeErr)
}

func workPublicationFence(s *store, p ResultPublicationPayload, binding ProjectBinding) func(sqlQueryer) error {
	return func(q sqlQueryer) error {
		current, err := s.currentProjectBinding(context.Background(), q, p.Target.Project)
		if err != nil {
			return err
		}
		if current != binding {
			return errors.New("binding changed before publication")
		}
		if err := publicationFence(s, p)(q); err != nil {
			return err
		}
		queries := dbquery.New(q)
		j, err := queries.QueueJobByID(context.Background(), p.Target.Job)
		if err != nil {
			return err
		}
		a, err := queries.AgentOwnershipByJob(context.Background(), p.Target.Job)
		if err != nil {
			return err
		}
		if j.State != "finished" || a.Phase != "terminal" || a.TerminalEvidence == "" || j.Outcome != "agent_"+a.ServerOutcome {
			return errors.New("terminal execution changed before publication")
		}
		return nil
	}
}

func ProjectWorkHandoff(dir, project, id string) (WorkHandoff, error) {
	out := WorkHandoff{Protocol: WorkProtocol, Validation: "not_run", Readiness: "not_ready"}
	if !isValidProjectID(project) || !workID(id) {
		return out, errors.New("invalid handoff pointer")
	}
	r, err := OpenReader(dir)
	if err != nil {
		return out, err
	}
	defer r.Close()
	j, err := workReadDir(r, project)
	if err != nil {
		return out, err
	}
	defer j.Close()
	current := id
	kinds := []string{"review_result", "review", "capture", "implementation"}
	states := make([]WorkStatus, 4)
	for n, kind := range kinds {
		s, e := workInspect(r, j, project, current)
		if e != nil || s.Kind != kind || s.State != "recorded_observed" {
			return out, errors.New("handoff requires exact confirmed implementation/capture/review/result chain")
		}
		states[n] = s
		current = s.ParentRequestID
	}
	out.ReviewResult, out.Review, out.Capture, out.Implementation = states[0], states[1], states[2], states[3]
	if report, e := workFinalReport(r, out.Review); e == nil {
		out.ReviewOutput = &report
	}
	if out.Capture.Result == nil || out.ReviewResult.Result == nil || out.Review.Receipt == nil || out.Review.Receipt.TargetResultDigest == nil || *out.Review.Receipt.TargetResultDigest != out.Capture.Result.ManifestDigest {
		return out, errors.New("handoff snapshot linkage mismatch")
	}
	for _, pair := range []struct {
		digest string
		dest   *string
	}{{out.Capture.Result.ManifestDigest, &out.ImplementationManifest}, {out.ReviewResult.Result.ManifestDigest, &out.ReviewManifest}} {
		raw, e := r.Manifest(project, pair.digest)
		if e != nil {
			return out, e
		}
		*pair.dest = string(raw)
	}
	// Imported structure/hash/linkage claims never become verified approval.
	// Readiness additionally requires an exact strict final reviewer report.
	reportMatches := false
	if out.ReviewOutput != nil {
		var report workReviewReport
		m, e := parseManifestBytes([]byte(out.ReviewManifest), "review_result", project)
		if _, decodeErr := decodeStrictJSONObject([]byte(out.ReviewOutput.Text), maxManifestBytes, &report); e == nil && decodeErr == nil && m.ReviewResult != nil {
			reportMatches = jsonEqual(report, workReviewReport{m.ReviewResult.Verdict, m.ReviewResult.Findings, m.ReviewResult.Limitations})
		}
	}
	check, e := r.VerifyWorktree(WorktreeCheckRequest{ProjectID: project, JobID: out.Capture.Result.JobID, ManifestDigest: out.Capture.Result.ManifestDigest})
	if e == nil {
		out.Worktree = &check
	}
	if reportMatches && out.Implementation.Job != nil && out.Review.Job != nil && out.Implementation.Job.TerminalProven && out.Review.Job.TerminalProven && out.Implementation.Job.Outcome == "agent_succeeded" && out.Review.Job.Outcome == "agent_succeeded" && out.ReviewResult.Result.Verdict == "approved" && out.Worktree != nil && out.Worktree.WorktreeMatch == "matches_snapshot" {
		out.Readiness = "ready_for_integrator_review"
	}
	return out, workOutputBound(out)
}

func workFinalReport(r *Reader, status WorkStatus) (WorkOutput, error) {
	out := WorkOutput{Protocol: WorkProtocol, Status: status, Provenance: "terminal_evidence_final_assistant_text; not test validation"}
	if status.Job == nil || !status.Job.TerminalProven {
		return out, errors.New("proven terminal worker required")
	}
	j, err := r.store.get(status.Job.JobID)
	if err != nil {
		return out, err
	}
	if !workTerminal(j) {
		return out, errors.New("terminal proof unavailable")
	}
	f, err := os.Open(j.Agent.TerminalEvidence)
	if err != nil {
		return out, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 8<<20+1))
	if err != nil || len(b) > 8<<20 {
		return out, errors.New("terminal evidence exceeds bound or unavailable")
	}
	var proof struct {
		Messages []message `json:"messages"`
	}
	if err = json.Unmarshal(b, &proof); err != nil {
		return out, err
	}
	for n := len(proof.Messages) - 1; n >= 0; n-- {
		m := proof.Messages[n]
		if m.Type != "assistant" {
			continue
		}
		if m.Time.Completed <= 0 {
			return out, errors.New("final assistant report incomplete")
		}
		var text strings.Builder
		for _, p := range m.Content {
			if p.Type == "tool" {
				return out, errors.New("final assistant report contains tool execution")
			}
			if p.Type == "text" {
				text.WriteString(p.Text)
			}
		}
		out.Text = text.String()
		if len(out.Text) > maxManifestBytes {
			return out, errors.New("final report exceeds 16 KiB bound")
		}
		out.SHA256 = sha256Hex([]byte(out.Text))
		return out, nil
	}
	return out, errors.New("no terminal assistant report")
}

// ReadProjectWorkOutput reads only retained terminal evidence, never a live
// session or an interpretation of prose as approval.
func ReadProjectWorkOutput(dir, project, id string) (WorkOutput, error) {
	status, err := InspectProjectWork(dir, project, id)
	if err != nil {
		return WorkOutput{}, err
	}
	r, err := OpenReader(dir)
	if err != nil {
		return WorkOutput{}, err
	}
	defer r.Close()
	return workFinalReport(r, status)
}

// The queue identity and file metadata remain separate from SQL observation.
func workObservationPath(dir string, c WorkObservationCursor) string {
	return filepath.Join(dir, workJournal(c.ProjectID))
}

func (c WorkObservationCursor) validate(requireFingerprint bool) error {
	if !isValidProjectID(c.ProjectID) || c.Fingerprint != "" && !isValidHexLen(c.Fingerprint, 64) || requireFingerprint && c.Fingerprint == "" {
		return errors.New("invalid project work observation cursor")
	}
	return nil
}

// For server-side callers needing the pinned binding without session guessing.
func (r *Reader) WorkBinding(project string) (ProjectBinding, error) {
	return workBinding(r, project)
}

// WorkQueueContext is the observation identity for existing schema 5 or 6.
func (r *Reader) WorkQueueContext() (string, string, error) {
	identity, err := notificationQueueIdentity(r.store)
	if err != nil {
		return "", "", err
	}
	info, err := os.Lstat(filepath.Join(r.store.dir, "queue.db"))
	if err != nil {
		return "", "", err
	}
	var schema int
	err = r.store.db.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&schema)
	if err != nil {
		return "", "", err
	}
	_, store := observationContexts(r.store.dir, info, identity, schema)
	return identity, store, nil
}
