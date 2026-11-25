package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"ocq/internal/core/dbquery"
)

const TaskArchiveProtocol = "ocq-task-archive-v1"

type TaskArchiveView struct {
	Protocol    string              `json:"protocol"`
	Project     string              `json:"project_id"`
	Request     string              `json:"request_id"`
	Session     string              `json:"session_id"`
	State       string              `json:"state"`
	Preview     string              `json:"preview_sha256,omitempty"`
	MainHandled *MainHandledReceipt `json:"main_handled,omitempty"`
	ArchivedAt  float64             `json:"native_archived_at,omitempty"`
}

type taskArchiveRecord struct {
	Target       sessionDeleteTarget // shared read-only native observation coordinates
	Handled      MainHandledReceipt
	Service      nativeService
	Binding      string
	BeforeHash   string
	SessionHash  string
	Transcript   string
	TerminalHash string
	Viewers      string
	Preview      string
}

type taskArchiveReceipt struct {
	Preview    string
	ArchivedAt float64
}

func taskArchiveFile(session, stage string) string {
	return ".task-archive-" + sha256Hex([]byte(session)) + "." + stage + ".json"
}

func archiveDisplay(project, request, session string) TaskArchiveView {
	return TaskArchiveView{Protocol: TaskArchiveProtocol, Project: project, Request: request, Session: session, State: "unavailable"}
}

func taskArchiveTarget(r *Reader, j *os.File, out TaskArchiveView) (taskArchiveRecord, Job, error) {
	a := taskArchiveRecord{}
	rec, payload, work, err := workLoad(r, j, out.Project, out.Request)
	if err != nil {
		return a, Job{}, err
	}
	if _, err = loadStandaloneTaskGoal(j, rec, work); err != nil {
		return a, Job{}, err
	}
	parsed, err := standaloneTaskSubmission(rec, work, payload)
	if err != nil {
		return a, Job{}, err
	}
	queue, store, err := r.WorkQueueContext()
	if err != nil {
		return a, Job{}, err
	}
	ctx := context.Background()
	var job Job
	err = r.store.readSchema5(func(q sqlQueryer) error {
		if ready, err := mainHandledSchemaReady(q); err != nil || !ready {
			return errors.New("standalone archive requires the existing Main-handled schema")
		}
		if err := validateMainHandledSchema(ctx, q); err != nil {
			return err
		}
		binding, err := r.store.currentProjectBinding(ctx, q, out.Project)
		if err != nil || binding != rec.Binding || binding.ElephantSessionID == out.Session {
			return errors.New("exact task admission binding no longer selected")
		}
		var main bool
		if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM project_bindings WHERE elephant_session_id=?)`, out.Session).Scan(&main); err != nil || main {
			return errors.New("archive never targets a current or historical Main")
		}
		sub, err := r.store.submissionReceiptFrom(q, parsed)
		if err != nil {
			return err
		}
		h, err := taskHandoffFrom(ctx, q, out.Project, out.Request)
		if err != nil || h.State != "handled" || h.job != sub.JobID || h.intent != rec.Digest ||
			h.lane != parsed.LaneID || h.MainRevision != binding.Revision || h.MainSessionID != binding.ElephantSessionID {
			return errors.New("original standalone task has no matching Main-handled receipt")
		}
		ack, err := mainHandledReceiptFrom(q, out.Project, out.Request)
		if err != nil || ack.IntentDigest != rec.Digest || ack.SubmissionKey != sub.Key || ack.EnvelopeDigest != sub.EnvelopeDigest ||
			ack.AdmissionRevision != sub.BindingRevision || ack.JobID != sub.JobID || ack.LaneID != h.lane ||
			ack.InboxID != h.InboxID || ack.InboxSequence != h.sequence || ack.AssociationID != h.AssociationID ||
			ack.ResultDigest != h.ResultDigest || ack.MainRevision != h.MainRevision || ack.MainSetupID != h.MainSetupID ||
			ack.MainSessionID != h.MainSessionID || !isValidInboxMessageID(ack.InboxID) ||
			!isValidHexLen(ack.ResultDigest, 64) || !isValidHexLen(ack.MainRoleDigest, 64) ||
			!validMainHandledActor(ack.MainSessionID, ack.NativeMessageID, ack.NativeCallID) || ack.RecordedAt == "" {
			return errors.New("immutable Main receipt does not match original task envelope")
		}
		job, err = r.store.getFrom(q, sub.JobID)
		if err != nil || !taskHandoffTerminal(job, ack.MainSessionID) || job.Agent.SessionID != out.Session ||
			job.Agent.Worktree == "" || job.Agent.ProjectID == "" || job.Agent.Token == "" || job.Agent.PromptID == "" ||
			job.Digest != parsed.RequestDigest {
			return errors.New("exact finished task and retained terminal ownership unavailable")
		}
		d := dbquery.New(q)
		lane, err := d.LaneForJob(ctx, dbquery.LaneForJobParams{ProjectID: out.Project, OwnerJobID: job.ID})
		if err != nil || lane.LaneID != ack.LaneID || lane.Role != "implementation" || lane.BaseCommit != rec.BaseCommit ||
			lane.FsforkDriver != parsed.FsforkDriver || lane.FsforkPath != job.Agent.Worktree ||
			lane.SealedManifestDigest.String != ack.ResultDigest {
			return errors.New("exact currently sealed task lane unavailable")
		}
		// The set-once handoff pointer is constrained at publication to the
		// original envelope, its association and seal (schema8.sql).
		a.Target = sessionDeleteTarget{Project: out.Project, Session: out.Session, Work: out.Request, Job: job.ID,
			Location: job.Agent.Worktree, NativeProject: job.Agent.ProjectID, Owner: nativeHash(ownershipMetadata(job)),
			Binding: binding.Revision, Queue: queue, Store: store, Device: rec.Device, Inode: rec.Inode}
		a.Binding, a.Handled = nativeHash(binding), ack
		return nil
	})
	return a, job, err
}

// Inspect retained navigation, not a live terminal listing: absence from an
// external UI list cannot resolve an attempted viewer. The local CLI does not
// own an HC binding, so it cannot authorize any task with such history.
func taskArchiveViewers(r *Reader, j *os.File, t sessionDeleteTarget) (string, error) {
	if _, err := j.Seek(0, 0); err != nil {
		return "", err
	}
	entries, err := workEntries(j)
	if err != nil {
		return "", err
	}
	pointers := []string{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ".client-") {
			continue
		}
		digest, stage, _ := strings.Cut(strings.TrimPrefix(name, ".client-"), ".")
		if strings.HasPrefix(digest, "nav-") && workID(strings.TrimPrefix(digest, "nav-")) && stage == "intent.json" {
			continue // No launch path consumes a navigation intent alone.
		}
		if !isValidHexLen(digest, 64) {
			return "", errors.New("unresolved native viewer journal")
		}
		s, err := inspectClient(r, j, t.Project, digest)
		if err != nil {
			return "", errors.New("unresolved native viewer journal")
		}
		p := s.Pointer
		if p.SessionID != t.Session && p.JobID != t.Job && p.WorkRequestID != t.Work {
			continue
		}
		if s.State != "ready_to_open" || name != clientFile(digest, "pointer") {
			return "", errors.New("historical viewer closure not proven in the local archive path")
		}
		pointers = append(pointers, digest)
	}
	return nativeHash(pointers), nil
}

// Bind archive to the same full history that froze the first terminal
// outcome, not just a later idle observation after a different turn.
func archiveTerminal(job Job, messages []message) (string, error) {
	f, err := os.Open(job.Agent.TerminalEvidence)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, sessionAuditLimit+1))
	var proof struct {
		Messages []message `json:"messages"`
	}
	if err != nil || len(b) > sessionAuditLimit || json.Unmarshal(b, &proof) != nil || len(messages) == 0 ||
		messages[len(messages)-1].Outcome != job.Agent.ServerOutcome || !jsonEqual(proof.Messages, messages) {
		return "", errors.New("native transcript no longer matches retained first-terminal history")
	}
	anchors := 0
	for _, m := range messages {
		if m.Type == "user" && m.ID == job.Agent.PromptID && matchesObservedUser(job, m, false, false) {
			anchors++
		}
	}
	if anchors != 1 {
		return "", errors.New("original task input not observed exactly once in first-terminal history")
	}
	return sha256Hex(b), nil
}

func taskArchiveSnapshot(ctx context.Context, r *Reader, j *os.File, out TaskArchiveView) (a taskArchiveRecord, state string, err error) {
	a, job, err := taskArchiveTarget(r, j, out)
	if err != nil {
		return a, "held", err
	}
	a.Viewers, err = taskArchiveViewers(r, j, a.Target)
	if err != nil {
		return a, "viewer_unverified", err
	}
	audit, state, err := sessionIdleSnapshot(ctx, r, a.Target, job)
	if err != nil || state != "ready" {
		return a, state, err
	}
	when, ok := nativeArchiveTime(audit.Session)
	if !ok || when != 0 {
		return a, "held", errors.New("session archive state unavailable or already archived outside this operation")
	}
	var session sessionInfo
	raw, err := json.Marshal(audit.Session)
	if err != nil || json.Unmarshal(raw, &session) != nil || validateSession(job, session) != nil {
		return a, "held", errors.New("live task profile no longer matches retained ownership")
	}
	if err = noPersistentSessionTerminals(ctx, a.Target, audit.Service); err != nil {
		return a, "resources", err
	}
	a.TerminalHash, err = archiveTerminal(job, audit.Transcript)
	if err != nil {
		return a, "held", err
	}
	a.Service, a.BeforeHash, a.Transcript = audit.Service, nativeHash(audit.Session), nativeHash(audit.Transcript)
	a.SessionHash = archiveSessionHash(audit.Session)
	a.Preview = nativeHash(a)
	return a, "ready", nil
}

func noPersistentSessionTerminals(ctx context.Context, t sessionDeleteTarget, svc nativeService) error {
	b, err := nativeCallContext(ctx, t.Location, svc, "get", "/api/experimental/session/"+url.PathEscape(t.Session)+"/terminal", nil)
	var persistent []json.RawMessage
	if err != nil || decodeData(b, &persistent) != nil || persistent == nil || len(persistent) != 0 {
		return errors.New("persistent terminal inventory nonempty or unobservable")
	}
	return nil
}

func nativeArchiveTime(s map[string]any) (float64, bool) {
	t, ok := s["time"].(map[string]any)
	if !ok {
		return 0, false
	}
	if t["archived"] == nil {
		return 0, true
	}
	n, ok := t["archived"].(float64)
	return n, ok && n > 0
}

func archiveSessionHash(s map[string]any) string {
	t, ok := s["time"].(map[string]any)
	if !ok {
		return ""
	}
	// Archive itself may update the native modification time.
	delete(t, "archived")
	delete(t, "updated")
	return nativeHash(s)
}

func readTaskArchive[T any](j *os.File, session, stage string) (value T, err error) {
	return readTaskJournal[T](j, taskArchiveFile(session, stage))
}

func taskArchiveAttempt(r *Reader, j *os.File, out TaskArchiveView) (taskArchiveRecord, error) {
	a, err := readTaskArchive[taskArchiveRecord](j, out.Session, "attempt")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if _, receiptErr := workRead(j, taskArchiveFile(out.Session, "archived")); !errors.Is(receiptErr, os.ErrNotExist) {
				return a, errors.New("orphan archive receipt; never resend")
			}
		}
		return a, err
	}
	copy := a
	copy.Preview = ""
	t := a.Target
	queue, store, err := r.WorkQueueContext()
	if err != nil || t.Queue != queue || t.Store != store || t.Project != out.Project || t.Work != out.Request ||
		t.Session != out.Session || a.Preview != nativeHash(copy) {
		return a, errors.New("archive attempt scope is unresolved; never resend")
	}
	return a, nil
}

func taskArchiveResult(j *os.File, a taskArchiveRecord, out *TaskArchiveView) error {
	out.State, out.Preview, out.MainHandled = "uncertain", a.Preview, &a.Handled
	receipt, err := readTaskArchive[taskArchiveReceipt](j, a.Target.Session, "archived")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || receipt.Preview != a.Preview || receipt.ArchivedAt <= 0 {
		return errors.New("archive receipt unresolved; never resend")
	}
	out.State, out.ArchivedAt = "archived", receipt.ArchivedAt
	return nil
}

// Check is read-only. Its preview identifies one cooperative attempt, not a
// native lock or a claim that unrelated clients have closed their viewers.
func CheckProjectTaskArchive(dir, project, request, session string) (TaskArchiveView, error) {
	out := archiveDisplay(project, request, session)
	if request == "" || !validDeletionTarget(project, session, request) {
		return out, errors.New("invalid exact standalone archive target")
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
	a, err := taskArchiveAttempt(r, j, out)
	if !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return out, err
		}
		err = taskArchiveResult(j, a, &out)
		return out, err
	}
	a, out.State, err = taskArchiveSnapshot(context.Background(), r, j, out)
	if out.State == "ready" {
		out.Preview, out.MainHandled = a.Preview, &a.Handled
	}
	return out, err
}

// RunProjectTaskArchive is local-only. The caller must explicitly confirm an
// exclusive operator window for native writers, viewer opens and OCQ changes
// affecting the task. The project journal serializes only OCQ journal writers.
func RunProjectTaskArchive(ctx context.Context, dir, project, request, session, preview string, confirmed, recovery bool) (TaskArchiveView, error) {
	out := archiveDisplay(project, request, session)
	if request == "" || !validDeletionTarget(project, session, request) || !isValidHexLen(preview, 64) || !recovery && !confirmed {
		return out, errors.New("exact preview and explicit exclusive-window confirmation required")
	}
	return runTaskArchive(ctx, dir, out, preview, recovery, false)
}

// Only the opted-in dispatcher invokes the cooperative path. Manual and
// automatic attempts share the same lock, attempt marker and GET-only recovery.
func runTaskArchive(ctx context.Context, dir string, out TaskArchiveView, preview string, recovery, automatic bool) (TaskArchiveView, error) {
	j, err := openLocalJournal(dir, workJournal(out.Project))
	if err != nil {
		return out, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return out, err
	}
	r, err := OpenReader(dir)
	if err != nil {
		return out, err
	}
	defer r.Close()
	session := out.Session
	a, err := taskArchiveAttempt(r, j.dir, out)
	if !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return out, err
		}
		if !automatic && a.Preview != preview {
			return out, errors.New("archive preview differs from retained attempt")
		}
		if err = taskArchiveResult(j.dir, a, &out); err != nil {
			return out, err
		}
		if out.State == "archived" {
			if err = j.syncFile(taskArchiveFile(session, "archived")); err != nil {
				out.State, out.ArchivedAt = "uncertain", 0
			}
			return out, err
		}
		if !recovery && !automatic {
			return out, errors.New("archive already attempted; GET-only reconciliation")
		}
		err = observeTaskArchive(ctx, j, a, &out)
		return out, err
	}
	if recovery {
		return out, errors.New("no exact archive attempt to reconcile")
	}
	a, out.State, err = taskArchiveSnapshot(ctx, r, j.dir, out)
	if err != nil {
		return out, err
	}
	if out.State != "ready" {
		return out, errors.New("task no longer eligible; archive not attempted")
	}
	if !automatic && a.Preview != preview {
		out.State = "changed"
		return out, errors.New("archive preview changed; review a fresh preview")
	}
	if err = nativeJournalRoom(j.dir, 2); err != nil {
		return out, err
	}
	auth, cancel := context.WithTimeout(ctx, 15*time.Second)
	password, err := nativePassword(auth, a.Target.Location, a.Service)
	cancel()
	if err != nil {
		return out, err
	}
	if automatic {
		// Recheck after auth and before arming so observed busy/changed state
		// doesn't needlessly consume the automatic attempt.
		final, state, e := taskArchiveSnapshot(ctx, r, j.dir, out)
		if e != nil || state != "ready" || final != a || ctx.Err() != nil {
			out.State = "changed"
			return out, errors.New("automatic archive preflight changed; not attempted")
		}
	}
	out.State, out.Preview, out.MainHandled = "uncertain", a.Preview, &a.Handled
	if err = j.create(taskArchiveFile(session, "attempt"), a); err != nil {
		return out, err
	}
	if !automatic {
		final, state, err := taskArchiveSnapshot(ctx, r, j.dir, out)
		if err != nil || state != "ready" || final != a {
			return out, errors.New("pre-effect recheck failed; attempt consumed, GET-only recovery")
		}
	}
	device, inode, err := j.queueIdentity()
	current, _, targetErr := taskArchiveTarget(r, j.dir, out)
	if err != nil || device != a.Target.Device || inode != a.Target.Inode || ctx.Err() != nil ||
		targetErr != nil || current.Target != a.Target || current.Binding != a.Binding || current.Handled != a.Handled {
		return out, errors.New("archive scope changed before effect; attempt consumed, GET-only recovery")
	}
	// The native operation is not conditional. Send exactly once, and require
	// a separate exact GET even when PATCH reports success.
	patch, cancel := context.WithTimeout(ctx, 15*time.Second)
	_, _, _ = nativePrivateRequest(patch, a.Service, "patch", deleteSessionPath(a.Target), []byte(`{"archived":true}`), password, 64<<10)
	cancel()
	err = observeTaskArchive(ctx, j, a, &out)
	return out, err
}

func observeTaskArchive(ctx context.Context, j *composeJournal, a taskArchiveRecord, out *TaskArchiveView) error {
	t := a.Target
	current, err := discoverNativeServiceContext(ctx, t.Location)
	if err != nil || current != a.Service {
		return errors.New("archive read-back cannot verify the same native service; never resend")
	}
	s, err := nativeSessionContext(ctx, t.Location, current, t.Session)
	when, ok := nativeArchiveTime(s)
	if err != nil || !ok || when == 0 || nativeIdentity(s, t.Session, t.Location, t.NativeProject, t.Owner) != nil ||
		archiveSessionHash(s) != a.SessionHash {
		return errors.New("exact native archived state unproven; never resend")
	}
	messages, err := readSessionAuditTranscript(ctx, t, current)
	if err != nil || nativeHash(messages) != a.Transcript {
		return errors.New("archived transcript identity unproven; never resend")
	}
	final, err := nativeSessionContext(ctx, t.Location, current, t.Session)
	stamp, ok := nativeArchiveTime(final)
	if err != nil || !ok || stamp != when || archiveSessionHash(final) != a.SessionHash {
		return errors.New("native archive state changed during read-back; never resend")
	}
	if current, err = discoverNativeServiceContext(ctx, t.Location); err != nil || current != a.Service {
		return errors.New("native service changed during archive read-back; never resend")
	}
	if err = j.create(taskArchiveFile(t.Session, "archived"), taskArchiveReceipt{Preview: a.Preview, ArchivedAt: when}); err != nil {
		return err
	}
	out.State, out.ArchivedAt = "archived", when
	return nil
}
