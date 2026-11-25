package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const taskWakeProtocol = "ocq-task-handoff-v1"

type taskWakeAttempt struct {
	Target    sessionDeleteTarget
	Service   nativeService
	Envelope  string
	MessageID string
}

type taskWakeReceipt struct {
	Attempt string
	State   string
	Status  int // Set only for a documented native rejection response.
}

func taskWakeFile(request, stage string) string {
	return ".task-wake-" + request + "." + stage + ".json"
}

func taskWakeEnvelope(h TaskHandoff) string {
	h.State, h.Reason = "", ""
	return nativeHash(h)
}

func readTaskJournal[T any](j *os.File, name string) (value T, err error) {
	b, err := workRead(j, name)
	if err == nil {
		_, err = decodeStrictJSONObject(b, WorkResponseBytes, &value)
	}
	return value, err
}

// An attempt is authoritative for no-replay even if its receipt is missing.
// These are historical native observations, not the Main-handled receipt.
func taskWakeJournal(j *os.File, rec workRecord, h TaskHandoff) (a taskWakeAttempt, state string) {
	a, err := readTaskJournal[taskWakeAttempt](j, taskWakeFile(h.RequestID, "attempt"))
	receipt, resultErr := readTaskJournal[taskWakeReceipt](j, taskWakeFile(h.RequestID, "result"))
	if errors.Is(err, os.ErrNotExist) && errors.Is(resultErr, os.ErrNotExist) {
		return a, ""
	}
	t := a.Target
	if err != nil || t.Project != rec.Binding.ProjectID || t.Work != h.RequestID || t.Queue != rec.Queue ||
		t.Device != rec.Device || t.Inode != rec.Inode || t.Session != h.MainSessionID ||
		t.Binding != h.MainRevision || t.Setup != h.MainSetupID || t.Location != rec.Binding.OpenCodeDirectory ||
		t.NativeProject != rec.Binding.OpenCodeProjectID || !isValidHexLen(t.Owner, 64) ||
		a.Envelope != taskWakeEnvelope(h) || !strings.HasPrefix(a.MessageID, "msg_ocqwake_") ||
		!isValidHexLen(strings.TrimPrefix(a.MessageID, "msg_ocqwake_"), 64) {
		return a, "unresolved"
	}
	if errors.Is(resultErr, os.ErrNotExist) {
		return a, "uncertain"
	}
	if resultErr != nil || receipt.Attempt != nativeHash(a) {
		return a, "unresolved"
	}
	switch receipt.State {
	case "native_message_observed", "not_sent":
		if receipt.Status == 0 {
			return a, receipt.State
		}
	case "rejected":
		if taskWakeRejection(receipt.Status) {
			return a, receipt.State
		}
	}
	return a, "unresolved"
}

func taskWakeRejection(status int) bool {
	switch status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound, http.StatusConflict:
		return true // Documented prompt admission rejections; never resend.
	}
	return false
}

func wakeText(h TaskHandoff) string {
	b, _ := json.Marshal(MainHandledPointer{RequestID: h.RequestID, InboxID: h.InboxID})
	return taskWakeProtocol + "\n" + string(b)
}

// Observe the exact consumed pointer, not a POST acknowledgement or queued
// inbox item. A missing/different ID or service cannot resolve uncertainty.
func observeTaskWake(ctx context.Context, a taskWakeAttempt, h TaskHandoff) bool {
	t := a.Target
	svc, err := discoverNativeServiceContext(ctx, t.Location)
	if err != nil || svc != a.Service {
		return false
	}
	session, err := nativeSessionContext(ctx, t.Location, svc, t.Session)
	if err != nil || nativeIdentity(session, t.Session, t.Location, t.NativeProject, t.Owner) != nil {
		return false
	}
	b, err := nativeCallContext(ctx, t.Location, svc, "get", deleteSessionPath(t)+"/message/"+url.PathEscape(a.MessageID), nil)
	var m message
	return err == nil && decodeData(b, &m) == nil && m.Type == "user" && m.ID == a.MessageID &&
		m.Text == wakeText(h) && len(m.Files)+len(m.Agents)+len(m.Skills) == 0 &&
		jsonEqual(m.Metadata, map[string]string{"protocol": taskWakeProtocol})
}

// The SQL snapshot is closed before native I/O. Only a still-pending original
// envelope on the exact retained binding can arm a wake.
func pendingTaskWake(ctx context.Context, r *Reader, rec workRecord, want taskHandoffRow) error {
	return r.store.readSchema5(func(q sqlQueryer) error {
		binding, err := r.store.currentProjectBinding(ctx, q, rec.Binding.ProjectID)
		if err != nil || binding != rec.Binding {
			return errors.New("task wake: Main binding changed")
		}
		h, err := taskHandoffFrom(ctx, q, binding.ProjectID, want.RequestID)
		if err != nil || h != want || h.State != "pending_main" || h.intent != rec.Digest || !isValidInboxMessageID(h.InboxID) {
			return errors.New("task wake: original handoff no longer pending")
		}
		job, err := r.store.getFrom(q, h.job)
		if err != nil || !taskHandoffTerminal(job, h.MainSessionID) {
			return errors.New("task wake: retained finished task proof unavailable")
		}
		return nil
	})
}

func advanceTaskWake(ctx context.Context, r *Reader, project string, h taskHandoffRow, allowWake bool) {
	j, err := openLocalJournal(r.store.dir, workJournal(project))
	if err != nil {
		return
	}
	defer j.close()
	rec, payload, work, err := workLoad(r, j.dir, project, h.RequestID)
	if err != nil {
		return
	}
	if _, err = loadStandaloneTaskGoal(j.dir, rec, work); err != nil {
		return
	}
	a, state := taskWakeJournal(j.dir, rec, h.TaskHandoff)
	if state != "" {
		if state == "uncertain" && observeTaskWake(ctx, a, h.TaskHandoff) {
			_ = j.create(taskWakeFile(h.RequestID, "result"), taskWakeReceipt{Attempt: nativeHash(a), State: "native_message_observed"})
		}
		return
	}
	if !allowWake || h.State != "pending_main" || pendingTaskWake(ctx, r, rec, h) != nil {
		return
	}
	parsed, err := standaloneTaskSubmission(rec, work, payload)
	if err != nil || parsed.LaneID != h.lane {
		return
	}
	sub, err := r.store.submissionReceiptFrom(r.store.db, parsed)
	if err != nil || sub.JobID != h.job {
		return
	}
	main, _, binding, err := selectedMain(r, j.dir, project)
	if err != nil || binding != rec.Binding || main.SetupID != h.MainSetupID || main.SessionID != h.MainSessionID {
		return
	}
	t := sessionDeleteTarget{Project: project, Work: h.RequestID, Setup: main.SetupID, Session: main.SessionID,
		Location: main.Repo, NativeProject: main.ProjectID, Owner: nativeHash(main.Metadata),
		Queue: rec.Queue, Device: rec.Device, Inode: rec.Inode, Binding: binding.Revision}
	audit, observed, err := sessionIdleSnapshot(ctx, r, t, Job{})
	when, archiveKnown := nativeArchiveTime(audit.Session)
	if err != nil || observed != "ready" || !archiveKnown || when != 0 || nativeProfile(audit.Session) != main.ProfileSHA ||
		noPersistentSessionTerminals(ctx, t, audit.Service) != nil {
		return // Including busy or pristine-but-unobservable. No attempt used.
	}
	main.Server = audit.Service
	if roleMatches, _, err := observeMainRoleContext(ctx, main); err != nil || !roleMatches {
		return
	}
	password, err := nativePassword(ctx, t.Location, audit.Service)
	if err != nil || deletionIdle(ctx, t, audit.Service) != "ready" {
		return
	}
	final, err := nativeSessionContext(ctx, t.Location, audit.Service, t.Session)
	if err != nil || !jsonEqual(final, audit.Session) {
		return
	}
	id, err := composeRandom()
	if err != nil {
		return
	}
	a = taskWakeAttempt{Target: t, Service: audit.Service, Envelope: taskWakeEnvelope(h.TaskHandoff), MessageID: "msg_ocqwake_" + id}
	body, err := json.Marshal(map[string]any{"id": a.MessageID, "text": wakeText(h.TaskHandoff),
		"metadata": map[string]string{"protocol": taskWakeProtocol}, "delivery": "queue", "resume": true})
	if err != nil || !armTaskWake(ctx, r, j, rec, main, h, a) {
		return
	}
	outcome := taskWakeReceipt{Attempt: nativeHash(a)}
	if ctx.Err() != nil || pendingTaskWake(ctx, r, rec, h) != nil {
		outcome.State = "not_sent"
	} else {
		_, status, err := nativePrivateRequest(ctx, a.Service, "post", deleteSessionPath(t)+"/prompt"+locationQuery(t.Location), body, password, 64<<10)
		if err == nil && taskWakeRejection(status) {
			outcome.State, outcome.Status = "rejected", status
		} else if observeTaskWake(ctx, a, h.TaskHandoff) {
			outcome.State = "native_message_observed"
		}
	}
	if outcome.State != "" {
		_ = j.create(taskWakeFile(h.RequestID, "result"), outcome)
	}
}

func armTaskWake(ctx context.Context, r *Reader, j *composeJournal, rec workRecord, main mainRecord, h taskHandoffRow, a taskWakeAttempt) bool {
	// This second handle holds the project lock for the short, local arm
	// only. The running Main tool must be able to use the journal immediately
	// when the native server starts the turn, even before the POST returns.
	locked, err := openLocalJournal(r.store.dir, workJournal(a.Target.Project))
	if err != nil {
		return false
	}
	defer locked.close()
	if err = locked.acquire(); err != nil {
		return false
	}
	curDir, curErr := locked.dir.Stat()
	openedDir, openedErr := j.dir.Stat()
	if curErr != nil || openedErr != nil || !os.SameFile(curDir, openedDir) {
		return false
	}
	current, _, binding, err := selectedMain(r, j.dir, a.Target.Project)
	dev, ino, identityErr := j.queueIdentity()
	if ctx.Err() != nil || identityErr != nil || dev != rec.Device || ino != rec.Inode ||
		err != nil || binding != rec.Binding || current.SessionID != main.SessionID || current.SetupID != main.SetupID ||
		current.Role.SHA256 != main.Role.SHA256 || pendingTaskWake(ctx, r, rec, h) != nil {
		return false
	}
	if _, state := taskWakeJournal(j.dir, rec, h.TaskHandoff); state != "" || nativeJournalRoom(j.dir, 2) != nil {
		return false
	}
	return j.create(taskWakeFile(h.RequestID, "attempt"), a) == nil
}
