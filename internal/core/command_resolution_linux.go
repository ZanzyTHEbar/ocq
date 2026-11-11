package core

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"ocq/internal/core/dbquery"
)

type commandResolutionRefusal struct {
	code string
	err  error
}

func (e *commandResolutionRefusal) Error() string { return e.err.Error() }
func (e *commandResolutionRefusal) Unwrap() error { return e.err }

// CommandResolver opens only an existing schema-5 store. All process and
// dispatcher checks remain in the real CLI resolver, not this adapter.
type CommandResolver struct{ store *store }

func OpenExistingCommandResolver(dir string) (*CommandResolver, error) {
	s, e := openExistingWritableStore(dir)
	if e != nil {
		return nil, e
	}
	s.existingSchemaOnly = true
	return &CommandResolver{s}, nil
}
func (w *CommandResolver) Close() error { return w.store.database.Close() }
func (w *CommandResolver) Resolve(s CommandResolutionScope, evidence string) error {
	if !s.valid() || !validResolutionEvidence(evidence) {
		return errors.New("invalid command resolution scope or empty evidence")
	}
	_, err := w.store.resolveChecked(s.Job, evidence, func(j Job) error {
		identity, e := notificationQueueIdentity(w.store)
		if e != nil {
			return e
		}
		if identity != s.Queue || strconv.FormatInt(j.Sequence, 10) != s.Sequence || j.State != "unknown" || j.agentRequest != nil || sha256Hex([]byte(j.Reason)) != s.ReasonDigest {
			return errors.New("exact unknown command observation changed; resolution refused")
		}
		return nil
	})
	return err
}

type CommandResolutionTarget struct {
	Machine  string `json:"machine"`
	Space    string `json:"space_id"`
	Queue    string `json:"queue_identity"`
	Job      string `json:"job_id"`
	Sequence string `json:"sequence"`
}

func (t CommandResolutionTarget) valid() bool {
	n, e := strconv.ParseInt(t.Sequence, 10, 64)
	return t.Machine == "local" && e == nil && n > 0 && strconv.FormatInt(n, 10) == t.Sequence && isValidHexLen(t.Queue, 64) && isValidJobID(t.Job) && strings.HasPrefix(t.Space, "sp") && len(t.Space) > 2 && len(t.Space) <= 128 && utf8.ValidString(t.Space) && !strings.ContainsAny(t.Space, "/\\ \t\r\n\x00")
}

// Exclude space, reason and evidence: another copy cannot authorize a second
// attempt for the same immutable receipt, even with different evidence.
func (t CommandResolutionTarget) id() string {
	return sha256Hex([]byte(t.Queue + "\n" + t.Job + "\n" + t.Sequence))
}
func (t CommandResolutionTarget) journal() string { return "herdr-command-resolve-" + t.id() }

type CommandResolutionScope struct {
	CommandResolutionTarget
	Kind         string `json:"displayed_kind"`
	State        string `json:"displayed_state"`
	ReasonDigest string `json:"reason_sha256"`
}

func (s CommandResolutionScope) valid() bool {
	return s.CommandResolutionTarget.valid() && s.Kind == "command" && s.State == "unknown" && isValidHexLen(s.ReasonDigest, 64)
}

// This payload is private. Never encode it into a bridge response or argv.
type CommandResolutionPayload struct {
	Scope    CommandResolutionScope `json:"scope"`
	Reason   string                 `json:"displayed_reason"`
	Evidence string                 `json:"evidence"`
}

func validResolutionEvidence(e string) bool {
	return len(e) <= 16<<10 && strings.TrimSpace(e) != "" && utf8.ValidString(e) && !strings.ContainsRune(e, 0)
}
func (p CommandResolutionPayload) valid() bool {
	return p.Scope.valid() && len(p.Reason) <= 16<<10 && utf8.ValidString(p.Reason) && sha256Hex([]byte(p.Reason)) == p.Scope.ReasonDigest && validResolutionEvidence(p.Evidence)
}

type commandResolutionRecord struct {
	Version int                     `json:"version"`
	Target  CommandResolutionTarget `json:"target"`
	Digest  string                  `json:"payload_sha256"`
	Raw     []byte                  `json:"payload_base64"`
}
type commandResolutionMarker struct {
	Version int    `json:"version"`
	Digest  string `json:"payload_sha256"`
	Refusal string `json:"refusal,omitempty"`
}
type CommandResolutionObservation struct {
	QueueIdentity  string             `json:"queue_identity"`
	Job            QueueJobSummary    `json:"job"`
	Completion     *QueueNotification `json:"completion,omitempty"`
	EvidenceDigest string             `json:"evidence_sha256"`
}
type CommandResolutionStatus struct {
	State          string                        `json:"state"`
	Target         CommandResolutionTarget       `json:"target"`
	Scope          *CommandResolutionScope       `json:"scope,omitempty"`
	IntentID       string                        `json:"intent_id,omitempty"`
	PayloadDigest  string                        `json:"payload_sha256,omitempty"`
	EvidenceDigest string                        `json:"evidence_sha256,omitempty"`
	Refusal        string                        `json:"refusal,omitempty"`
	Observation    *CommandResolutionObservation `json:"observation,omitempty"`
}

// intent.json wraps the payload as base64, not as raw payload bytes. Reserve
// bounded metadata in addition to the maximum base64 expansion; the private
// transport and decoded payload limit remain composeLimit (128 KiB).
const commandResolutionRecordLimit = ((composeLimit + 2) / 3 * 4) + 4096

func resolutionRead(dir *os.File, name string) ([]byte, error) {
	f, e := composePrivateAt(dir, name, syscall.O_RDONLY)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, commandResolutionRecordLimit+1))
	if len(b) > commandResolutionRecordLimit {
		return nil, errors.New("resolution record too large")
	}
	return b, e
}
func resolutionRecord(dir *os.File, t CommandResolutionTarget) (commandResolutionRecord, CommandResolutionPayload, string, string) {
	var r commandResolutionRecord
	var p CommandResolutionPayload
	b, e := resolutionRead(dir, "intent.json")
	if errors.Is(e, syscall.ENOENT) {
		for _, n := range []string{"armed.json", "attempt.json", "refused.json"} {
			if _, e := resolutionRead(dir, n); !errors.Is(e, syscall.ENOENT) {
				return r, p, "blocked", ""
			}
		}
		return r, p, "none", ""
	}
	if e != nil || json.Unmarshal(b, &r) != nil || r.Version != 1 || r.Target != t || sha256Hex(r.Raw) != r.Digest {
		return r, p, "blocked", ""
	}
	if _, e := decodeStrictJSONObject(r.Raw, composeLimit, &p); e != nil || !p.valid() || p.Scope.CommandResolutionTarget != t {
		return r, p, "blocked", ""
	}
	state := "prepared"
	for _, step := range []struct{ name, state string }{{"armed.json", "armed"}, {"attempt.json", "uncertain"}, {"refused.json", "refused"}} {
		b, e := resolutionRead(dir, step.name)
		if errors.Is(e, syscall.ENOENT) {
			continue
		}
		var m commandResolutionMarker
		if e != nil || json.Unmarshal(b, &m) != nil || m.Version != 1 || m.Digest != r.Digest || step.state == "uncertain" && state != "armed" || step.state == "refused" && state != "uncertain" {
			return r, p, "blocked", ""
		}
		if step.state == "refused" {
			if m.Refusal != "dispatcher_lock_unavailable" && m.Refusal != "process_exit_not_established" {
				return r, p, "blocked", ""
			}
			return r, p, "refused", m.Refusal
		}
		state = step.state
	}
	return r, p, state, ""
}
func resolutionObservation(r *Reader, t CommandResolutionTarget) (*CommandResolutionObservation, error) {
	c, e := r.store.db.Conn(context.Background())
	if e != nil {
		return nil, e
	}
	defer c.Close()
	if _, e = c.ExecContext(context.Background(), "BEGIN"); e != nil {
		return nil, e
	}
	defer c.ExecContext(context.Background(), "ROLLBACK")
	identity, e := notificationQueueIdentity(r.store)
	if e != nil {
		return nil, e
	}
	q := dbquery.New(c)
	row, e := q.QueueJobByID(context.Background(), t.Job)
	if e != nil {
		return nil, e
	}
	j := queueSummary(row)
	if identity != t.Queue || j.Sequence != t.Sequence {
		return nil, errors.New("exact queue or receipt mismatch")
	}
	private, e := q.JobByID(context.Background(), t.Job)
	if e != nil {
		return nil, e
	}
	out := &CommandResolutionObservation{QueueIdentity: identity, Job: j, EvidenceDigest: sha256Hex([]byte(private.Evidence))}
	if j.CompletionEventID != "" {
		n, e := q.NotificationByID(context.Background(), j.CompletionEventID)
		if e != nil {
			return nil, e
		}
		event := queueNotification(notificationFromRow(n))
		out.Completion = &event
		exitMatches := event.ExitCode == nil && j.ExitCode == nil || event.ExitCode != nil && j.ExitCode != nil && int64(*event.ExitCode) == *j.ExitCode
		if event.JobID != j.ID || event.State != j.State || event.Outcome != j.Outcome || event.Validation != j.Validation || !exitMatches {
			return nil, errors.New("job/completion mismatch")
		}
	}
	return out, nil
}
func resolutionEligible(o *CommandResolutionObservation, s CommandResolutionScope) bool {
	return o != nil && o.Job.Kind == "command" && o.Job.State == "unknown" && sha256Hex([]byte(o.Job.Reason)) == s.ReasonDigest
}

// Inspection never creates, synchronizes, claims or retries a journal.
func InspectCommandResolution(dir string, t CommandResolutionTarget) (CommandResolutionStatus, error) {
	out := CommandResolutionStatus{State: "blocked", Target: t}
	if !t.valid() {
		return out, errors.New("invalid local command resolution target")
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
	j, e := composeOpenPrivateDirAt(root, t.journal())
	var record commandResolutionRecord
	var payload CommandResolutionPayload
	if errors.Is(e, syscall.ENOENT) {
		out.State = "none"
	} else if e != nil {
		return out, e
	} else {
		defer j.Close()
		record, payload, out.State, out.Refusal = resolutionRecord(j, t)
	}
	if out.State == "blocked" {
		return out, nil
	}
	if out.State != "none" {
		out.IntentID = t.id()
		out.PayloadDigest = record.Digest
		out.EvidenceDigest = sha256Hex([]byte(payload.Evidence))
		out.Scope = &payload.Scope
	}
	out.Observation, e = resolutionObservation(r, t)
	if e != nil {
		if out.State == "none" {
			return out, e
		}
		out.State = "uncertain"
		return out, nil
	}
	o := out.Observation
	if out.Scope != nil {
		if o.Job.Kind == "command" && o.Job.State == "resolved" && o.Job.Outcome == "operator_reconciled" && o.Completion != nil && o.EvidenceDigest == out.EvidenceDigest {
			out.State = "resolved_observed"
		} else if !resolutionEligible(o, *out.Scope) {
			out.State = "changed"
		}
	} else if o.Job.Kind != "command" || o.Job.State != "unknown" {
		out.State = "ineligible"
	}
	return out, nil
}

// Preparation receives private exact bytes over the proven mutual-auth loopback
// protocol. It records intent only; stdout never contains reason/evidence bytes.
func ReceiveCommandResolution(dir string, t CommandResolutionTarget, launch string) (CommandResolutionStatus, error) {
	initial, e := InspectCommandResolution(dir, t)
	if e != nil || initial.State != "none" {
		return initial, e
	}
	if !isValidHexLen(launch, 64) {
		return initial, errors.New("invalid resolution launch")
	}
	j, e := openLocalJournal(dir, t.journal())
	if e != nil {
		return initial, e
	}
	defer j.close()
	if e = j.acquire(); e != nil {
		return initial, e
	}
	_, _, state, _ := resolutionRecord(j.dir, t)
	if state != "none" {
		return initial, errors.New("retained resolution exists; check only")
	}
	e = j.withComposeConnection(launch, func(conn net.Conn) error {
		var digits strings.Builder
		var one [1]byte
		for {
			if _, e := io.ReadFull(conn, one[:]); e != nil {
				return errors.New("incomplete resolution frame")
			}
			if one[0] == '\n' {
				break
			}
			if one[0] < '0' || one[0] > '9' || digits.Len() >= 6 {
				return errors.New("invalid resolution frame")
			}
			digits.WriteByte(one[0])
		}
		n, e := strconv.Atoi(digits.String())
		if e != nil || n < 1 || n > composeLimit || strconv.Itoa(n) != digits.String() {
			return errors.New("invalid resolution frame")
		}
		raw := make([]byte, n)
		if _, e = io.ReadFull(conn, raw); e != nil {
			return errors.New("incomplete resolution payload")
		}
		var p CommandResolutionPayload
		if _, e = decodeStrictJSONObject(raw, composeLimit, &p); e != nil || !p.valid() || p.Scope.CommandResolutionTarget != t {
			return errors.New("invalid private resolution payload")
		}
		r, e := OpenReader(dir)
		if e != nil {
			return e
		}
		o, e := resolutionObservation(r, t)
		e = errors.Join(e, r.Close())
		if e != nil {
			return e
		}
		if !resolutionEligible(o, p.Scope) {
			return errors.New("exact unknown command observation changed")
		}
		record := commandResolutionRecord{1, t, sha256Hex(raw), raw}
		encoded, e := json.Marshal(record)
		if e != nil || len(encoded) > commandResolutionRecordLimit {
			return errors.New("resolution journal record exceeds encoded limit")
		}
		if e = j.create("intent.json", record); e != nil {
			return e
		}
		_, _ = fmt.Fprintln(conn, "PREPARED "+t.id())
		return nil
	})
	if e != nil {
		return initial, e
	}
	return InspectCommandResolution(dir, t)
}
func CommandResolutionRendezvous(dir string, t CommandResolutionTarget, launch string) (ComposeRendezvous, error) {
	if !t.valid() || !isValidHexLen(launch, 64) {
		return ComposeRendezvous{}, errors.New("invalid resolution rendezvous")
	}
	r, e := OpenReader(dir)
	if e != nil {
		return ComposeRendezvous{}, e
	}
	defer r.Close()
	root, e := os.Open(r.store.dir)
	if e != nil {
		return ComposeRendezvous{}, e
	}
	defer root.Close()
	j, e := composeOpenPrivateDirAt(root, t.journal())
	if e != nil {
		return ComposeRendezvous{}, e
	}
	defer j.Close()
	b, e := resolutionRead(j, "rendezvous.json")
	var entry ComposeRendezvous
	if e != nil || json.Unmarshal(b, &entry) != nil || entry.Launch != launch || entry.Port < 1 || entry.Port > 65535 || !isValidHexLen(entry.Capability, 64) {
		return ComposeRendezvous{}, errors.New("exact resolution rendezvous unavailable")
	}
	return entry, nil
}
func PreviewCommandResolution(dir string, t CommandResolutionTarget, digest, launch string) (CommandResolutionStatus, error) {
	if !t.valid() || !isValidHexLen(digest, 64) || !isValidHexLen(launch, 64) {
		return CommandResolutionStatus{}, errors.New("invalid resolution preview")
	}
	j, e := openLocalJournal(dir, t.journal())
	if e != nil {
		return CommandResolutionStatus{}, e
	}
	defer j.close()
	if e = j.acquire(); e != nil {
		return CommandResolutionStatus{}, e
	}
	rec, _, state, _ := resolutionRecord(j.dir, t)
	if state == "none" || state == "blocked" || rec.Digest != digest {
		return CommandResolutionStatus{}, errors.New("exact private resolution unavailable")
	}
	e = j.withComposeConnection(launch, func(conn net.Conn) error {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], uint32(len(rec.Raw)))
		if _, e := conn.Write(header[:]); e != nil {
			return e
		}
		_, e := conn.Write(rec.Raw)
		return e
	})
	if e != nil {
		return CommandResolutionStatus{}, e
	}
	return InspectCommandResolution(dir, t)
}
func ArmCommandResolution(dir string, t CommandResolutionTarget, digest string) (CommandResolutionStatus, error) {
	return sendCommandResolution(dir, t, digest, false)
}
func AttemptCommandResolution(dir string, t CommandResolutionTarget, digest string) (CommandResolutionStatus, error) {
	return sendCommandResolution(dir, t, digest, true)
}
func sendCommandResolution(dir string, t CommandResolutionTarget, digest string, attempt bool) (CommandResolutionStatus, error) {
	out := CommandResolutionStatus{State: "uncertain", Target: t}
	if !t.valid() || !isValidHexLen(digest, 64) {
		return out, errors.New("invalid resolution claim")
	}
	j, e := openLocalJournal(dir, t.journal())
	if e != nil {
		return out, e
	}
	defer j.close()
	if e = j.acquire(); e != nil {
		return out, e
	}
	rec, p, state, _ := resolutionRecord(j.dir, t)
	expected, file := "prepared", "armed.json"
	if attempt {
		expected, file = "armed", "attempt.json"
	}
	if state != expected || rec.Digest != digest {
		return out, errors.New("resolution claim already consumed or mismatched; check only, never retry")
	}
	r, e := OpenReader(dir)
	if e != nil {
		return out, e
	}
	o, e := resolutionObservation(r, t)
	e = errors.Join(e, r.Close())
	if e != nil {
		return out, e
	}
	if !resolutionEligible(o, p.Scope) {
		return InspectCommandResolution(dir, t)
	}
	if e = j.syncFile("intent.json"); e != nil {
		return out, e
	}
	if attempt {
		if e = j.syncFile("armed.json"); e != nil {
			return out, e
		}
	}
	if e = j.create(file, commandResolutionMarker{Version: 1, Digest: digest}); e != nil {
		return out, e
	}
	if !attempt {
		return InspectCommandResolution(dir, t)
	}
	// The attempt is durable before opening SQLite or acquiring the dispatcher
	// lock. Failure, timeout or a lost response is never permission for a resend.
	w, e := OpenExistingCommandResolver(dir)
	if e != nil {
		return InspectCommandResolution(dir, t)
	}
	writeErr := errors.Join(w.Resolve(p.Scope, p.Evidence), w.Close())
	var refusal *commandResolutionRefusal
	if errors.As(writeErr, &refusal) {
		if e = j.create("refused.json", commandResolutionMarker{1, digest, refusal.code}); e != nil {
			return out, e
		}
	}
	return InspectCommandResolution(dir, t)
}
