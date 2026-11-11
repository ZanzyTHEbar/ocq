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
	"time"
	"unicode/utf8"

	"github.com/ZanzyTHEbar/faults-go"
	"ocq/internal/core/dbquery"
)

type AgentReconcileTarget struct {
	CommandResolutionTarget
	Session  string `json:"recorded_session_id"`
	Worktree string `json:"recorded_worktree"`
}

func (t AgentReconcileTarget) valid() bool {
	return t.CommandResolutionTarget.valid() && strings.HasPrefix(t.Session, "ses_") && len(t.Session) <= 256 && !strings.ContainsAny(t.Session, "/\\?# \t\r\n\x00") && utf8.ValidString(t.Session) && filepath.IsAbs(t.Worktree) && len(t.Worktree) <= 4096 && utf8.ValidString(t.Worktree) && !strings.ContainsAny(t.Worktree, "\r\n\x00")
}

// One retained authorization per immutable receipt; a new location or another
// space cannot create a second authorization or adopt the existing one.
func (t AgentReconcileTarget) journal() string {
	return "herdr-agent-reconcile-" + t.CommandResolutionTarget.id()
}

// Safe snapshots exclude raw requests, ownership capabilities and evidence.
// Digest fingerprints the private job/ownership rows without exposing them.
// FirstTerminal is historical and frozen, NOT proof of this operation's result.
type AgentReconcileSnapshot struct {
	QueueIdentity string             `json:"queue_identity"`
	Digest        string             `json:"snapshot_sha256"`
	Job           QueueJobSummary    `json:"job"`
	Phase         string             `json:"recorded_phase"`
	ServerOutcome string             `json:"recorded_server_outcome"`
	FirstTerminal *QueueNotification `json:"first_terminal,omitempty"`
}

func (s AgentReconcileSnapshot) matches(t AgentReconcileTarget) bool {
	return s.QueueIdentity == t.Queue && s.Job.ID == t.Job && s.Job.Sequence == t.Sequence && s.Job.Kind == "agent" && s.Job.SessionID == t.Session && s.Job.Worktree == t.Worktree && isValidHexLen(s.Digest, 64)
}
func admittedAgent(state string) bool {
	return state == "launching" || state == "running" || state == "unknown" || state == "finished"
}

const agentReconcileRecordLimit = 256 << 10

func agentReconcileSnapshot(s *store, t AgentReconcileTarget) (AgentReconcileSnapshot, error) {
	var out AgentReconcileSnapshot
	c, e := s.db.Conn(context.Background())
	if e != nil {
		return out, e
	}
	defer c.Close()
	if _, e = c.ExecContext(context.Background(), "BEGIN"); e != nil {
		return out, e
	}
	defer c.ExecContext(context.Background(), "ROLLBACK")
	identity, e := notificationQueueIdentity(s)
	if e != nil {
		return out, e
	}
	if identity != t.Queue {
		return out, errors.New("exact queue identity changed")
	}
	q := dbquery.New(c)
	row, e := q.QueueJobByID(context.Background(), t.Job)
	if e != nil {
		return out, e
	}
	private, e := q.JobByID(context.Background(), t.Job)
	if e != nil {
		return out, e
	}
	ownership, e := q.AgentOwnershipByJob(context.Background(), t.Job)
	if e != nil {
		return out, e
	}
	b, e := json.Marshal(struct {
		Job       dbquery.JobByIDRow
		Ownership dbquery.AgentOwnershipByJobRow
	}{private, ownership})
	if e != nil {
		return out, e
	}
	out = AgentReconcileSnapshot{QueueIdentity: identity, Digest: sha256Hex(b), Job: queueSummary(row), Phase: ownership.Phase, ServerOutcome: ownership.ServerOutcome}
	if !out.matches(t) {
		return AgentReconcileSnapshot{}, errors.New("exact agent receipt/session/worktree mismatch")
	}
	if out.Job.CompletionEventID != "" {
		n, e := q.NotificationByID(context.Background(), out.Job.CompletionEventID)
		if e != nil {
			return out, e
		}
		event := queueNotification(notificationFromRow(n))
		out.FirstTerminal = &event
	}
	safe, e := json.Marshal(out)
	if e != nil {
		return out, e
	}
	if len(safe) > 64<<10 {
		return AgentReconcileSnapshot{}, errors.New("agent snapshot exceeds safe review limit")
	}
	return out, nil
}

type agentReconcileIntent struct {
	Version int                    `json:"version"`
	ID      string                 `json:"operation_id"`
	Target  AgentReconcileTarget   `json:"target"`
	Before  AgentReconcileSnapshot `json:"before"`
}
type agentReconcileMarker struct {
	Version      int    `json:"version"`
	ID           string `json:"operation_id"`
	IntentDigest string `json:"intent_sha256"`
}
type AgentReconcileResult struct {
	Version      int                    `json:"version"`
	ID           string                 `json:"operation_id"`
	IntentDigest string                 `json:"intent_sha256"`
	BeforeDigest string                 `json:"requested_snapshot_sha256"`
	RecordedAt   string                 `json:"recorded_at"`
	Outcome      string                 `json:"operation_outcome"`
	After        AgentReconcileSnapshot `json:"snapshot"`
}
type AgentReconcileStatus struct {
	State        string                  `json:"state"`
	Target       AgentReconcileTarget    `json:"target"`
	ID           string                  `json:"operation_id,omitempty"`
	IntentDigest string                  `json:"intent_sha256,omitempty"`
	Before       *AgentReconcileSnapshot `json:"before,omitempty"`
	Current      *AgentReconcileSnapshot `json:"current,omitempty"`
	Result       *AgentReconcileResult   `json:"operation_receipt,omitempty"`
	ResultDigest string                  `json:"operation_receipt_sha256,omitempty"`
	CurrentMatch string                  `json:"current_snapshot_match,omitempty"`
}

func readAgentReconcile(dir *os.File, name string) ([]byte, error) {
	f, e := composePrivateAt(dir, name, syscall.O_RDONLY)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, agentReconcileRecordLimit+1))
	if len(b) > agentReconcileRecordLimit {
		return nil, errors.New("agent reconciliation record exceeds bound")
	}
	return b, e
}
func createAgentReconcile(j *composeJournal, name string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if len(b) > agentReconcileRecordLimit {
		return errors.New("agent reconciliation record exceeds bound")
	}
	return j.create(name, v)
}
func agentReconcileRecord(dir *os.File, t AgentReconcileTarget) (AgentReconcileStatus, agentReconcileIntent) {
	out := AgentReconcileStatus{State: "blocked", Target: t}
	var r agentReconcileIntent
	b, e := readAgentReconcile(dir, "intent.json")
	if errors.Is(e, syscall.ENOENT) {
		for _, n := range []string{"armed.json", "attempt.json", "result.json"} {
			if _, e := readAgentReconcile(dir, n); !errors.Is(e, syscall.ENOENT) {
				return out, r
			}
		}
		out.State = "none"
		return out, r
	}
	if e != nil || json.Unmarshal(b, &r) != nil || r.Version != 1 || !isValidHexLen(r.ID, 64) || r.Target != t || !r.Before.matches(t) || !admittedAgent(r.Before.Job.State) {
		return out, r
	}
	out.State = "prepared"
	out.ID = r.ID
	out.IntentDigest = sha256Hex(b)
	out.Before = &r.Before
	for _, step := range []struct{ name, state string }{{"armed.json", "armed"}, {"attempt.json", "uncertain"}} {
		b, e := readAgentReconcile(dir, step.name)
		if errors.Is(e, syscall.ENOENT) {
			continue
		}
		var m agentReconcileMarker
		if e != nil || json.Unmarshal(b, &m) != nil || m.Version != 1 || m.ID != r.ID || m.IntentDigest != out.IntentDigest || step.state == "uncertain" && out.State != "armed" {
			out.State = "blocked"
			return out, r
		}
		out.State = step.state
	}
	b, e = readAgentReconcile(dir, "result.json")
	if errors.Is(e, syscall.ENOENT) {
		return out, r
	}
	var result AgentReconcileResult
	if e != nil || out.State != "uncertain" || json.Unmarshal(b, &result) != nil || result.Version != 1 || result.ID != r.ID || result.IntentDigest != out.IntentDigest || result.BeforeDigest != r.Before.Digest || !result.After.matches(t) {
		out.State = "blocked"
		return out, r
	}
	if _, e := time.Parse(time.RFC3339Nano, result.RecordedAt); e != nil {
		out.State = "blocked"
		return out, r
	}
	if result.Outcome != "observer_returned" && result.Outcome != "observer_error" && result.Outcome != "dispatcher_lock_unavailable" {
		out.State = "blocked"
		return out, r
	}
	out.State = "recorded"
	out.Result = &result
	out.ResultDigest = sha256Hex(b)
	return out, r
}

// Status is SQLite/filesystem-only. In particular a terminal current job NEVER
// substitutes for the operation receipt, and no missing receipt triggers GETs.
func InspectAgentReconciliation(dir string, t AgentReconcileTarget) (AgentReconcileStatus, error) {
	out := AgentReconcileStatus{State: "blocked", Target: t}
	if !t.valid() {
		return out, errors.New("invalid local agent reconciliation target")
	}
	r, e := OpenReader(dir)
	if e != nil {
		return out, e
	}
	defer r.Close()
	fd, e := syscall.Open(r.store.dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if e != nil {
		return out, e
	}
	root := os.NewFile(uintptr(fd), r.store.dir)
	defer root.Close()
	journal, e := composeOpenPrivateDirAt(root, t.journal())
	if errors.Is(e, syscall.ENOENT) {
		out.State = "none"
	} else if e != nil {
		return out, e
	} else {
		defer journal.Close()
		out, _ = agentReconcileRecord(journal, t)
	}
	if out.State == "blocked" {
		return out, nil
	}
	current, e := agentReconcileSnapshot(r.store, t)
	if e != nil {
		out.CurrentMatch = "unavailable"
		if out.State == "none" {
			return out, e
		}
		if out.State != "recorded" {
			out.State = "uncertain"
		}
		return out, nil
	}
	out.Current = &current
	if out.Result != nil {
		out.CurrentMatch = "modified_since_operation"
		if current.Digest == out.Result.After.Digest {
			out.CurrentMatch = "matches_operation_snapshot"
		}
	} else if out.State == "prepared" || out.State == "armed" {
		if current.Digest != out.Before.Digest {
			out.State = "changed"
		}
	}
	if out.State == "none" && !admittedAgent(current.Job.State) {
		out.State = "ineligible"
	}
	return out, nil
}

func PrepareAgentReconciliation(dir string, t AgentReconcileTarget, before string) (AgentReconcileStatus, error) {
	initial, e := InspectAgentReconciliation(dir, t)
	if e != nil || initial.State != "none" {
		return initial, e
	}
	if !isValidHexLen(before, 64) || initial.Current == nil || initial.Current.Digest != before {
		return initial, errors.New("displayed agent snapshot changed; review exact stored job")
	}
	j, e := openLocalJournal(dir, t.journal())
	if e != nil {
		return initial, e
	}
	defer j.close()
	if e = j.acquire(); e != nil {
		return initial, e
	}
	status, _ := agentReconcileRecord(j.dir, t)
	if status.State != "none" {
		return status, nil
	}
	r, e := OpenReader(dir)
	if e != nil {
		return initial, e
	}
	current, e := agentReconcileSnapshot(r.store, t)
	e = errors.Join(e, r.Close())
	if e != nil {
		return initial, e
	}
	if current.Digest != before || !admittedAgent(current.Job.State) {
		return initial, errors.New("displayed agent observation changed; nothing prepared")
	}
	id, e := composeRandom()
	if e != nil {
		return initial, e
	}
	if e = createAgentReconcile(j, "intent.json", agentReconcileIntent{1, id, t, current}); e != nil {
		return initial, e
	}
	return InspectAgentReconciliation(dir, t)
}
func ArmAgentReconciliation(dir string, t AgentReconcileTarget, id, digest string) (AgentReconcileStatus, error) {
	return sendAgentReconciliation(dir, t, id, digest, false)
}
func AttemptAgentReconciliation(dir string, t AgentReconcileTarget, id, digest string) (AgentReconcileStatus, error) {
	return sendAgentReconciliation(dir, t, id, digest, true)
}
func sendAgentReconciliation(dir string, t AgentReconcileTarget, id, digest string, attempt bool) (AgentReconcileStatus, error) {
	out := AgentReconcileStatus{State: "uncertain", Target: t, ID: id, IntentDigest: digest}
	if !t.valid() || !isValidHexLen(id, 64) || !isValidHexLen(digest, 64) {
		return out, errors.New("invalid agent reconciliation claim")
	}
	j, e := openLocalJournal(dir, t.journal())
	if e != nil {
		return out, e
	}
	defer j.close()
	if e = j.acquire(); e != nil {
		return out, e
	}
	status, intent := agentReconcileRecord(j.dir, t)
	expected, file := "prepared", "armed.json"
	if attempt {
		expected, file = "armed", "attempt.json"
	}
	if status.State != expected || status.ID != id || status.IntentDigest != digest {
		return out, errors.New("agent reconciliation claim consumed or mismatched; check only, never resend")
	}
	r, e := OpenReader(dir)
	if e != nil {
		return out, e
	}
	current, e := agentReconcileSnapshot(r.store, t)
	e = errors.Join(e, r.Close())
	if e != nil {
		return out, e
	}
	if current.Digest != intent.Before.Digest || !admittedAgent(current.Job.State) {
		return InspectAgentReconciliation(dir, t)
	}
	if e = j.syncFile("intent.json"); e != nil {
		return out, e
	}
	if attempt {
		if e = j.syncFile("armed.json"); e != nil {
			return out, e
		}
	}
	if e = createAgentReconcile(j, file, agentReconcileMarker{1, id, digest}); e != nil {
		return out, e
	}
	if !attempt {
		return InspectAgentReconciliation(dir, t)
	}
	// Never initialize/migrate a queue or run a dispatcher. The real observer
	// alone owns completion decisions and issues only observational API calls.
	s, e := openExistingWritableStore(dir)
	if e != nil {
		return InspectAgentReconciliation(dir, t)
	}
	s.existingSchemaOnly = true
	started := false
	_, observeErr := s.reconcileChecked(t.Job, func(_ Job) error {
		snapshot, e := agentReconcileSnapshot(s, t)
		if e != nil {
			return e
		}
		if snapshot.Digest != intent.Before.Digest || !admittedAgent(snapshot.Job.State) {
			return errors.New("exact agent observation changed before API observation")
		}
		started = true
		return nil
	}, func(observerErr error) error {
		after, e := agentReconcileSnapshot(s, t)
		if e != nil {
			return e
		}
		outcome := "observer_returned"
		if observerErr != nil {
			outcome = "observer_error"
		}
		// Persist before releasing the dispatcher lock. A crash before this point
		// stays uncertain even if other stored job data subsequently looks terminal.
		return createAgentReconcile(j, "result.json", AgentReconcileResult{1, id, digest, intent.Before.Digest, now(), outcome, after})
	})
	if !started && faults.IsCode(observeErr, faults.CodeResourceLimit) {
		after, e := agentReconcileSnapshot(s, t)
		if e == nil {
			e = createAgentReconcile(j, "result.json", AgentReconcileResult{1, id, digest, intent.Before.Digest, now(), "dispatcher_lock_unavailable", after})
		}
		if e != nil {
			_ = s.database.Close()
			return out, e
		}
	}
	closeErr := s.database.Close()
	if closeErr != nil {
		return out, nil
	}
	return InspectAgentReconciliation(dir, t)
}
