package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"ocq/internal/core/dbquery"
)

// QueuedCanceller cannot initialize, migrate, dispatch or stop running work.
type QueuedCanceller struct{ store *store }

func OpenExistingQueuedCanceller(dir string) (*QueuedCanceller, error) {
	s, err := openExistingWritableStore(dir)
	if err != nil {
		return nil, err
	}
	s.existingSchemaOnly = true
	return &QueuedCanceller{s}, nil
}
func (c *QueuedCanceller) Close() error { return c.store.database.Close() }
func (c *QueuedCanceller) Cancel(s QueuedCancelScope) error {
	if !s.valid() {
		return errors.New("invalid queued cancellation scope")
	}
	return c.store.cancelChecked(s.Job, func(j Job) error {
		identity, err := notificationQueueIdentity(c.store)
		if err != nil {
			return err
		}
		if identity != s.Queue || strconv.FormatInt(j.Sequence, 10) != s.Sequence || j.State != s.DisplayedState {
			return errors.New("queue, receipt or queued precondition changed; cancellation refused")
		}
		return nil
	})
}

const queuedCancelJournal = "herdr-queued-cancels"

type QueuedCancelScope struct {
	Machine        string `json:"machine"`
	Space          string `json:"space_id"`
	Queue          string `json:"queue_identity"`
	Job            string `json:"job_id"`
	Sequence       string `json:"sequence"`
	DisplayedState string `json:"displayed_state"`
}

func (s QueuedCancelScope) valid() bool {
	n, err := strconv.ParseInt(s.Sequence, 10, 64)
	return s.Machine == "local" && s.DisplayedState == "queued" && err == nil && n > 0 && strconv.FormatInt(n, 10) == s.Sequence &&
		isValidHexLen(s.Queue, 64) && isValidJobID(s.Job) && strings.HasPrefix(s.Space, "sp") && len(s.Space) > 2 && len(s.Space) <= 128 &&
		utf8.ValidString(s.Space) && !strings.ContainsAny(s.Space, "/\\ \t\r\n\x00")
}
func (s QueuedCancelScope) id() string {
	return sha256Hex([]byte(s.Queue + "\n" + s.Job + "\n" + s.Sequence + "\n" + s.DisplayedState))
}

type queuedCancelRecord struct {
	Version int               `json:"version"`
	ID      string            `json:"id"`
	Scope   QueuedCancelScope `json:"scope"`
}
type QueuedCancelObservation struct {
	QueueIdentity string             `json:"queue_identity"`
	Job           QueueJobSummary    `json:"job"`
	Completion    *QueueNotification `json:"completion,omitempty"`
}
type QueuedCancelStatus struct {
	State       string                   `json:"state"`
	IntentID    string                   `json:"intent_id,omitempty"`
	Scope       QueuedCancelScope        `json:"scope"`
	Observation *QueuedCancelObservation `json:"observation,omitempty"`
}

// A consistent read snapshot is essential: a cancelled job without its frozen
// completion is never proof of cancellation. Observation does not prove causality.
func queuedCancelObservation(r *Reader, s QueuedCancelScope) (*QueuedCancelObservation, error) {
	c, err := r.store.db.Conn(context.Background())
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if _, err = c.ExecContext(context.Background(), "BEGIN"); err != nil {
		return nil, err
	}
	defer c.ExecContext(context.Background(), "ROLLBACK")
	identity, err := notificationQueueIdentity(r.store)
	if err != nil {
		return nil, err
	}
	row, err := dbquery.New(c).QueueJobByID(context.Background(), s.Job)
	if err != nil {
		return nil, err
	}
	j := queueSummary(row)
	if identity != s.Queue || j.Sequence != s.Sequence {
		return nil, errors.New("exact queue or receipt changed")
	}
	out := &QueuedCancelObservation{QueueIdentity: identity, Job: j}
	if j.CompletionEventID != "" {
		n, err := dbquery.New(c).NotificationByID(context.Background(), j.CompletionEventID)
		if err != nil {
			return nil, err
		}
		summary := queueNotification(notificationFromRow(n))
		out.Completion = &summary
		exitMatches := summary.ExitCode == nil && j.ExitCode == nil || summary.ExitCode != nil && j.ExitCode != nil && int64(*summary.ExitCode) == *j.ExitCode
		if summary.JobID != j.ID || summary.State != j.State || summary.Outcome != j.Outcome || summary.Validation != j.Validation || !exitMatches {
			return nil, errors.New("job/completion mismatch")
		}
	}
	return out, nil
}

// Journal states are cancellation-specific; only the secure immutable file and
// lock/fsync mechanics are shared with other local journals.
func queuedCancelState(dir *os.File, s QueuedCancelScope) (string, error) {
	matches := func(b []byte) bool {
		var r queuedCancelRecord
		return json.Unmarshal(b, &r) == nil && r.Version == 1 && r.ID == s.id() && r.Scope == s
	}
	b, err := readNotificationAckFile(dir, s.id()+".json")
	if errors.Is(err, syscall.ENOENT) {
		for _, suffix := range []string{".armed", ".attempt"} {
			if _, e := readNotificationAckFile(dir, s.id()+suffix); !errors.Is(e, syscall.ENOENT) {
				return "blocked", nil
			}
		}
		return "none", nil
	}
	if err != nil || !matches(b) {
		return "blocked", nil
	}
	b, err = readNotificationAckFile(dir, s.id()+".armed")
	armed := !errors.Is(err, syscall.ENOENT)
	if armed && (err != nil || !matches(b)) {
		return "blocked", nil
	}
	b, err = readNotificationAckFile(dir, s.id()+".attempt")
	if errors.Is(err, syscall.ENOENT) {
		if armed {
			return "armed", nil
		}
		return "prepared", nil
	}
	if err != nil || !armed || !matches(b) {
		return "blocked", nil
	}
	return "uncertain", nil
}

func InspectQueuedCancellation(dir string, s QueuedCancelScope) (QueuedCancelStatus, error) {
	out := QueuedCancelStatus{State: "blocked", Scope: s}
	if !s.valid() {
		return out, errors.New("invalid local queued cancellation scope")
	}
	r, err := OpenReader(dir)
	if err != nil {
		return out, err
	}
	defer r.Close()
	fd, err := syscall.Open(r.store.dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return out, err
	}
	root := os.NewFile(uintptr(fd), r.store.dir)
	defer root.Close()
	journal, err := composeOpenPrivateDirAt(root, queuedCancelJournal)
	if errors.Is(err, syscall.ENOENT) {
		out.State = "none"
	} else if err != nil {
		return out, err
	} else {
		defer journal.Close()
		out.State, err = queuedCancelState(journal, s)
		if err != nil {
			return out, err
		}
	}
	if out.State != "none" {
		out.IntentID = s.id()
	}
	if out.State == "blocked" {
		return out, nil
	}
	out.Observation, err = queuedCancelObservation(r, s)
	if err != nil {
		if out.State == "none" {
			return out, err
		}
		out.State = "uncertain"
		return out, nil
	}
	j := out.Observation.Job
	if j.State == "cancelled" && j.Outcome == "cancelled_before_launch" && out.Observation.Completion != nil {
		out.State = "cancelled_observed"
	} else if j.State != "queued" {
		out.State = "not_queued"
	}
	return out, nil
}

func PrepareQueuedCancellation(dir string, s QueuedCancelScope) (QueuedCancelStatus, error) {
	initial, err := InspectQueuedCancellation(dir, s)
	if err != nil || initial.State != "none" {
		return initial, err
	}
	j, err := openLocalJournal(dir, queuedCancelJournal)
	if err != nil {
		return initial, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return initial, err
	}
	state, err := queuedCancelState(j.dir, s)
	if err != nil {
		return initial, err
	}
	if state != "none" {
		return QueuedCancelStatus{State: state, IntentID: s.id(), Scope: s}, nil
	}
	r, err := OpenReader(dir)
	if err != nil {
		return initial, err
	}
	o, err := queuedCancelObservation(r, s)
	err = errors.Join(err, r.Close())
	if err != nil {
		return initial, err
	}
	if o.Job.State != "queued" {
		return QueuedCancelStatus{State: "not_queued", Scope: s, Observation: o}, nil
	}
	err = j.create(s.id()+".json", queuedCancelRecord{1, s.id(), s})
	return QueuedCancelStatus{State: "prepared", IntentID: s.id(), Scope: s, Observation: o}, err
}

func ArmQueuedCancellation(dir string, s QueuedCancelScope, id string) (QueuedCancelStatus, error) {
	return queuedCancellationSend(dir, s, id, false)
}
func AttemptQueuedCancellation(dir string, s QueuedCancelScope, id string) (QueuedCancelStatus, error) {
	return queuedCancellationSend(dir, s, id, true)
}
func queuedCancellationSend(dir string, s QueuedCancelScope, id string, attempt bool) (QueuedCancelStatus, error) {
	out := QueuedCancelStatus{State: "uncertain", IntentID: id, Scope: s}
	if !s.valid() || id != s.id() {
		return out, errors.New("cancellation intent mismatch")
	}
	j, err := openLocalJournal(dir, queuedCancelJournal)
	if err != nil {
		return out, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return out, err
	}
	state, err := queuedCancelState(j.dir, s)
	if err != nil {
		return out, err
	}
	expected, suffix := "prepared", ".armed"
	if attempt {
		expected, suffix = "armed", ".attempt"
	}
	if state != expected {
		return out, fmt.Errorf("cancellation claim %s: check only, never resend", state)
	}
	r, err := OpenReader(dir)
	if err != nil {
		return out, err
	}
	o, err := queuedCancelObservation(r, s)
	err = errors.Join(err, r.Close())
	if err != nil {
		return out, err
	}
	if o.Job.State != "queued" {
		return InspectQueuedCancellation(dir, s)
	}
	if err = j.syncFile(id + ".json"); err != nil {
		return out, err
	}
	if attempt {
		if err = j.syncFile(id + ".armed"); err != nil {
			return out, err
		}
	}
	// A lost arm response cannot authorize a send. The attempt marker is also
	// immutable and synced before opening the writer: ambiguous failures consume
	// this authorization, even when the exact job still appears queued.
	if err = j.create(id+suffix, queuedCancelRecord{1, id, s}); err != nil {
		return out, err
	}
	if !attempt {
		out.State = "armed"
		return out, nil
	}
	writer, err := OpenExistingQueuedCanceller(dir)
	if err != nil {
		return out, nil
	}
	writeErr := writer.Cancel(s)
	closeErr := writer.Close()
	if writeErr != nil || closeErr != nil {
		return out, nil
	}
	return InspectQueuedCancellation(dir, s)
}
