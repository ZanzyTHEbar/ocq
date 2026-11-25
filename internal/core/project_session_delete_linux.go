package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const SessionDeleteProtocol = "ocq-session-delete-v1"
const sessionAuditLimit = 8 << 20

type SessionDeleteView struct {
	Protocol string `json:"protocol"`
	Session  string `json:"session_id"`
	State    string `json:"state"`
	Preview  string `json:"preview_sha256,omitempty"`
}

type sessionDeleteTarget struct {
	Project, Session, Work, Setup, Job, Goal, Location, NativeProject, Owner string
	Queue, Store, Device, Inode                                              string
	Binding                                                                  int
}

type sessionDeleteRecord struct {
	Target  sessionDeleteTarget
	Service nativeService
	Preview string
}

type sessionDeleteAudit struct {
	Target     sessionDeleteTarget
	Service    nativeService
	Session    map[string]any
	Transcript []message
}

func deletionFile(session, stage string) string {
	return ".session-delete-" + sha256Hex([]byte(session)) + "." + stage + ".json"
}

// Compact journal records only. A corrupt or partial attempt never permits a
// new DELETE. The large private audit is separately retained under this key.
func deletionRecord(j *os.File, session, stage string) (sessionDeleteRecord, error) {
	var record sessionDeleteRecord
	b, err := workRead(j, deletionFile(session, stage))
	if err != nil {
		return record, err
	}
	if json.Unmarshal(b, &record) != nil || record.Target.Session != session || !isValidHexLen(record.Preview, 64) {
		return record, errors.New("invalid session deletion record")
	}
	return record, nil
}

func sessionDeletionState(j *os.File, project, session, work string) string {
	rec, err := deletionRecord(j, session, "attempt")
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil || rec.Target.Project != project || rec.Target.Work != work {
		return "uncertain"
	}
	if done, err := deletionRecord(j, session, "deleted"); err == nil && done == rec {
		return "deleted"
	}
	return "uncertain"
}

func deletionTarget(r *Reader, j *os.File, project, session, work string, c WorkActionContext) (sessionDeleteTarget, Job, error) {
	t := sessionDeleteTarget{Project: project, Session: session, Work: work}
	queue, store, err := r.WorkQueueContext()
	if err != nil || queue != c.QueueIdentity || store != c.StoreContext {
		return t, Job{}, errors.New("session queue context changed")
	}
	binding, err := workBinding(r, project)
	if err != nil || binding.RepoRoot != c.Folder {
		return t, Job{}, errors.New("session project binding changed")
	}
	t.Binding, t.Queue, t.Store = binding.Revision, queue, store
	if work == "" {
		main, _, _, err := selectedMainRecord(r, j, project)
		if err != nil || main.OwnershipMode != "ocq_main" || main.SessionID != session {
			return t, Job{}, errors.New("exact OCQ-created selected Main required")
		}
		t.Location, t.NativeProject, t.Setup = main.Repo, main.ProjectID, main.SetupID
		t.Owner, t.Device, t.Inode = nativeHash(main.Metadata), main.Device, main.Inode
		return t, Job{}, nil
	}
	status, err := workInspect(r, j, project, work)
	if err != nil || status.Receipt == nil || status.Job == nil || status.Job.SessionID != session ||
		status.Kind != "implementation" && status.Kind != "review" {
		return t, Job{}, errors.New("exact goal worker receipt required")
	}
	b, err := workRead(j, ".goal-"+work+".json")
	var link GoalWorkLink
	if err != nil || json.Unmarshal(b, &link) != nil || link.Protocol != GoalProtocol || link.ProjectID != project ||
		link.RequestID != work || link.WorkIntent != status.IntentSHA256 || !workID(link.OperationID) {
		return t, Job{}, errors.New("exact goal lineage required")
	}
	parent, payload, err := mainLoad(r, j, project, link.OperationID)
	if err != nil || payload.Mode != "send" || parent.Digest != link.MainIntent || parent.PromptID == "" || parent.PromptID != link.PromptID {
		return t, Job{}, errors.New("goal input identity changed")
	}
	job, err := r.store.get(status.Receipt.JobID)
	if err != nil || job.Agent == nil || job.agentRequest == nil || job.Agent.SessionID != session || job.Agent.ProjectID == "" || job.Agent.Worktree == "" {
		return t, Job{}, errors.New("worker does not have exact retained ownership")
	}
	t.Location, t.NativeProject, t.Job, t.Goal = job.Agent.Worktree, job.Agent.ProjectID, job.ID, link.OperationID
	t.Owner, t.Device, t.Inode = nativeHash(ownershipMetadata(job)), parent.Device, parent.Inode
	return t, job, nil
}

func deleteSessionPath(t sessionDeleteTarget) string {
	return "/api/session/" + url.PathEscape(t.Session)
}

func readDeletionTranscript(t sessionDeleteTarget, svc nativeService) ([]message, error) {
	all := []message{}
	cursor := ""
	seen := map[string]bool{}
	size := 0
	for i := 0; i < 1000; i++ {
		query := url.Values{"limit": {"24"}}
		if cursor == "" {
			query.Set("order", "asc")
		} else {
			query.Set("cursor", cursor)
		}
		body, err := nativeCall(t.Location, svc, "get", deleteSessionPath(t)+"/message?"+query.Encode(), nil)
		size += len(body)
		var page struct {
			Data   []message
			Cursor *struct{ Next *string }
		}
		if err != nil || size > sessionAuditLimit || json.Unmarshal(body, &page) != nil || page.Data == nil || page.Cursor == nil {
			return nil, errors.New("complete native transcript unavailable within audit bound")
		}
		all = append(all, page.Data...)
		if page.Cursor.Next == nil {
			return all, nil
		}
		cursor = *page.Cursor.Next
		if cursor == "" || seen[cursor] {
			break
		}
		seen[cursor] = true
	}
	return nil, errors.New("native transcript pagination unavailable")
}

// This checks resources without persisting peer data, and includes known shell
// handles from both the retained job and the current native transcript.
func deletionResources(r *Reader, t sessionDeleteTarget, svc nativeService, messages []message) error {
	read := func(path string, out any, location bool) error {
		body, err := nativeCall(t.Location, svc, "get", path, nil)
		if err == nil && location {
			var envelope struct{ Location struct{ Directory string } }
			if json.Unmarshal(body, &envelope) != nil || envelope.Location.Directory != t.Location {
				return errors.New("resource location changed")
			}
		}
		if err != nil {
			return err
		}
		return decodeData(body, out)
	}
	var children []json.RawMessage
	if read("/api/session?"+url.Values{"parentID": {t.Session}, "limit": {"1"}}.Encode(), &children, false) != nil || children == nil || len(children) > 0 {
		return errors.New("child sessions present or unobservable")
	}
	var jobs []struct{ SessionID string }
	if read("/api/experimental/job", &jobs, false) != nil || jobs == nil {
		return errors.New("session resource inventory unavailable")
	}
	for _, job := range jobs {
		if job.SessionID == t.Session {
			return errors.New("owned background work remains")
		}
	}
	var ptys []struct{ Status string }
	if read("/api/pty"+locationQuery(t.Location), &ptys, true) != nil || ptys == nil {
		return errors.New("location terminal inventory unavailable")
	}
	for _, pty := range ptys {
		if pty.Status != "exited" {
			return errors.New("location has an active or unobservable PTY")
		}
	}
	var shells []shellInfo
	if read("/api/shell"+locationQuery(t.Location), &shells, true) != nil || shells == nil {
		return errors.New("location shell inventory unavailable")
	}
	for _, shell := range shells {
		if shell.Metadata.SessionID == t.Session {
			return errors.New("owned shell is running")
		}
	}
	var known []string
	if t.Job != "" {
		p := filepath.Join(r.store.dir, "jobs", t.Job)
		if _, err := os.Stat(filepath.Join(p, "unsupported-runtime-resources.json")); !errors.Is(err, os.ErrNotExist) {
			return errors.New("previously owned resources remain unobservable")
		}
		if body, err := os.ReadFile(filepath.Join(p, "known-shell-ids.json")); err == nil {
			if json.Unmarshal(body, &known) != nil {
				return errors.New("known shell evidence unavailable")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, m := range messages {
		for _, part := range m.Content {
			if part.Type != "tool" {
				continue
			}
			id, _, err := terminalTool(part)
			if err != nil {
				return err
			}
			if id != "" {
				known = append(known, id)
			}
		}
	}
	seen := map[string]bool{}
	for _, id := range known {
		if seen[id] {
			continue
		}
		seen[id] = true
		var shell shellInfo
		if len(seen) > 64 || !strings.HasPrefix(id, "sh_") || strings.ContainsAny(id, "/?#") ||
			read("/api/shell/"+url.PathEscape(id)+locationQuery(t.Location), &shell, true) != nil ||
			shell.ID != id || shell.Metadata.SessionID != t.Session || shell.Time.Started <= 0 || shell.Time.Completed < shell.Time.Started ||
			(shell.Status != "exited" && shell.Status != "timeout" && shell.Status != "killed") {
			return errors.New("exact known shell exit unobservable")
		}
	}
	return nil
}

func deletionIdle(t sessionDeleteTarget, svc nativeService) string {
	b, err := nativeCall(t.Location, svc, "get", "/api/session/active", nil)
	var active map[string]json.RawMessage
	if err != nil || decodeData(b, &active) != nil || active == nil {
		return "unavailable"
	}
	if _, present := active[t.Session]; present {
		return "active"
	}
	for _, suffix := range []string{"/inbox", "/permission", "/form"} {
		b, err = nativeCall(t.Location, svc, "get", deleteSessionPath(t)+suffix, nil)
		var items []json.RawMessage
		if err != nil || decodeData(b, &items) != nil || items == nil {
			return "unavailable"
		}
		if len(items) != 0 {
			return "pending"
		}
	}
	return "ready"
}

func deletionSnapshot(r *Reader, t sessionDeleteTarget, job Job) (sessionDeleteAudit, string, error) {
	a := sessionDeleteAudit{Target: t}
	svc, err := discoverNativeService(t.Location)
	if err != nil {
		return a, "unavailable", err
	}
	a.Service = svc
	a.Session, err = nativeSession(t.Location, svc, t.Session)
	if err != nil || nativeIdentity(a.Session, t.Session, t.Location, t.NativeProject, t.Owner) != nil {
		return a, "unavailable", errors.New("exact live session ownership unavailable")
	}
	if state := deletionIdle(t, svc); state != "ready" {
		return a, state, nil
	}
	if t.Job != "" && !workTerminal(job) {
		return a, "job_unsettled", nil
	}
	a.Transcript, err = readDeletionTranscript(t, svc)
	if err != nil {
		return a, "unavailable", err
	}
	if len(a.Transcript) != 0 {
		last := a.Transcript[len(a.Transcript)-1]
		if last.Type != "idle" || (last.Outcome != "succeeded" && last.Outcome != "failed" && last.Outcome != "interrupted") {
			return a, "unavailable", errors.New("explicit idle outcome unavailable")
		}
	}
	if err = deletionResources(r, t, svc, a.Transcript); err != nil {
		return a, "resources", err
	}
	repeated, err := readDeletionTranscript(t, svc)
	if err != nil || !jsonEqual(repeated, a.Transcript) {
		return a, "changed", nil
	}
	if state := deletionIdle(t, svc); state != "ready" {
		return a, state, nil
	}
	final, err := nativeSession(t.Location, svc, t.Session)
	if err != nil || !jsonEqual(final, a.Session) {
		return a, "changed", nil
	}
	return a, "ready", nil
}

func validDeletionTarget(project, session, work string) bool {
	return isValidProjectID(project) && strings.HasPrefix(session, "ses_") && workID(session) && (work == "" || workID(work))
}

// A check is read-only; the digest binds the confirmation to the native session,
// its full transcript, the verified service and current project ownership.
func CheckProjectSessionDelete(dir, project, session, work string, c WorkActionContext) (SessionDeleteView, error) {
	out := SessionDeleteView{Protocol: SessionDeleteProtocol, Session: session, State: "unavailable"}
	if !validDeletionTarget(project, session, work) {
		return out, errors.New("invalid exact session deletion target")
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
	t, job, err := deletionTarget(r, j, project, session, work, c)
	if err != nil {
		return out, nil
	}
	if state := sessionDeletionState(j, project, session, work); state != "" {
		out.State = state
		return out, nil
	}
	audit, state, _ := deletionSnapshot(r, t, job)
	out.State = state
	if state == "ready" {
		data, err := json.Marshal(audit)
		if err != nil || len(data) > sessionAuditLimit {
			out.State = "unavailable"
		} else {
			out.Preview = sha256Hex(data)
		}
	}
	return out, nil
}

func DeleteProjectSession(dir, project, session, work, preview string, c WorkActionContext) (SessionDeleteView, error) {
	out := SessionDeleteView{Protocol: SessionDeleteProtocol, Session: session, State: "unavailable"}
	if !validDeletionTarget(project, session, work) || !isValidHexLen(preview, 64) {
		return out, errors.New("invalid exact session deletion confirmation")
	}
	j, err := openLocalJournal(dir, workJournal(project))
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
	t, job, err := deletionTarget(r, j.dir, project, session, work, c)
	if err != nil {
		return out, nil
	}
	if state := sessionDeletionState(j.dir, project, session, work); state != "" {
		out.State = state
		return out, nil
	}
	audit, state, _ := deletionSnapshot(r, t, job)
	out.State = state
	if state != "ready" {
		return out, nil
	}
	auditBytes, err := json.Marshal(audit)
	if err != nil || len(auditBytes) > sessionAuditLimit || sha256Hex(auditBytes) != preview {
		out.State = "changed"
		return out, nil
	}
	svc, err := discoverNativeService(t.Location)
	if err != nil || svc != audit.Service {
		out.State = "changed"
		return out, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	password, err := nativePassword(ctx, t.Location, svc)
	if err != nil {
		return out, err
	}
	if err = nativeJournalRoom(j.dir, 3); err != nil {
		return out, err
	}
	// File names include the confirmation digest: an interrupted pre-attempt
	// audit cannot block a later fresh check of a different snapshot. Never
	// overwrite an audit that an earlier operation may have used.
	auditFile := deletionFile(session, "audit-"+preview)
	if err = j.create(auditFile, audit); errors.Is(err, os.ErrExist) {
		f, openErr := composePrivateAt(j.dir, auditFile, os.O_RDONLY)
		if openErr != nil {
			return out, openErr
		}
		prior, readErr := io.ReadAll(io.LimitReader(f, sessionAuditLimit+1))
		if err = errors.Join(readErr, f.Close()); err != nil || len(prior) > sessionAuditLimit || sha256Hex(prior) != preview {
			return out, errors.New("previous audit incomplete or changed")
		}
		err = j.syncFile(auditFile)
	}
	if err != nil {
		return out, err
	}
	final, err := nativeSession(t.Location, svc, t.Session)
	if err != nil || !jsonEqual(final, audit.Session) || deletionIdle(t, svc) != "ready" || ctx.Err() != nil {
		out.State = "changed"
		return out, nil
	}
	rec := sessionDeleteRecord{Target: t, Service: svc, Preview: preview}
	out.State = "uncertain"
	if err = j.create(deletionFile(session, "attempt"), rec); err != nil {
		return out, err
	}
	body, status, err := nativePrivateRequest(ctx, svc, "delete", deleteSessionPath(t), nil, password, 16<<10)
	if err != nil || status != http.StatusNoContent || len(body) != 0 {
		return out, nil // Never retry, including an initial 404 or permission denial.
	}
	if err = j.create(deletionFile(session, "ack"), rec); err != nil {
		return out, err
	}
	readCtx, readCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer readCancel()
	body, status, err = nativePrivateRequest(readCtx, svc, "get", deleteSessionPath(t), nil, password, 16<<10)
	var missing struct {
		Tag     string `json:"_tag"`
		Session string `json:"sessionID"`
	}
	if err != nil || status != http.StatusNotFound || json.Unmarshal(body, &missing) != nil ||
		missing.Tag != "SessionNotFoundError" || missing.Session != session {
		return out, nil
	}
	if current, err := discoverNativeService(t.Location); err != nil || current != svc {
		return out, nil
	}
	if err = j.create(deletionFile(session, "deleted"), rec); err != nil {
		return out, fmt.Errorf("session absence observed; receipt not durable: %w", err)
	}
	out.State = "deleted"
	return out, nil
}
