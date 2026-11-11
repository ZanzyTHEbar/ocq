package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/ZanzyTHEbar/faults-go"
)

// NotificationAcknowledger can only acknowledge retained global events in an
// existing schema-5 queue. It cannot initialize, migrate or execute work.
type NotificationAcknowledger struct{ store *store }

func OpenExistingNotificationAcknowledger(stateDir string) (*NotificationAcknowledger, error) {
	s, err := openExistingWritableStore(stateDir)
	if err != nil {
		return nil, err
	}
	s.existingSchemaOnly = true
	return &NotificationAcknowledger{s}, nil
}

func (a *NotificationAcknowledger) Close() error {
	return classifyDiagnostic(faults.CodeDatabaseWrite, a.store.database.Close())
}

func (a *NotificationAcknowledger) Acknowledge(event, job string, sequence int64, identity string) (Notification, error) {
	if event == "" || !isValidJobID(job) || sequence <= 0 || !isValidHexLen(identity, 64) {
		return Notification{}, faults.New(faults.CodeValidationFailed, "notification acknowledgement requires exact event, job, sequence and queue identity")
	}
	return a.store.acknowledgeChecked(event, func(n Notification) error {
		current, err := notificationQueueIdentity(a.store)
		if err != nil {
			return err
		}
		if current != identity || n.JobID != job || n.Sequence != sequence {
			return faults.New(faults.CodeValidationFailed, "notification observation or queue identity changed")
		}
		return nil
	})
}

// This local identity detects accidental queue replacement/retargeting, not an
// in-place restore or hostile same-user forgery. Do not replace stores in use.
func notificationQueueIdentity(s *store) (string, error) {
	f, err := privateFile(filepath.Join(s.dir, "queue.db"), os.O_RDONLY)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Nlink != 1 {
		return "", faults.New(faults.CodeStorageFailed, "queue identity unavailable")
	}
	return sha256Hex([]byte(fmt.Sprintf("%s:%d:%d", s.dir, st.Dev, st.Ino))), nil
}

const notificationAckJournal = "herdr-notification-acks"

// NotificationAckScope is local-only. Space is an exact Herdr space, not a
// project selection. Decimal sequences never round-trip through JavaScript numbers.
type NotificationAckScope struct {
	Machine  string `json:"machine"`
	Space    string `json:"space_id"`
	Queue    string `json:"queue_identity"`
	Event    string `json:"event_id"`
	Job      string `json:"job_id"`
	Sequence string `json:"sequence"`
}

func (s NotificationAckScope) valid() bool {
	n, err := strconv.ParseInt(s.Sequence, 10, 64)
	return s.Machine == "local" && err == nil && n > 0 && strconv.FormatInt(n, 10) == s.Sequence && isValidHexLen(s.Queue, 64) &&
		isValidJobID(s.Job) && len(s.Event) > 0 && len(s.Event) <= 1024 && utf8.ValidString(s.Event) && !strings.ContainsAny(s.Event, "\r\n\x00") &&
		strings.HasPrefix(s.Space, "sp") && len(s.Space) > 2 && len(s.Space) <= 128 && utf8.ValidString(s.Space) &&
		!strings.ContainsAny(s.Space, "/\\ \t\r\n\x00")
}

// Space is deliberately not in the key: another panel/space cannot prepare a
// second attempt for this queue/event, or overwrite the original authorization.
func (s NotificationAckScope) id() string {
	return sha256Hex([]byte(s.Queue + "\n" + s.Event + "\n" + s.Job + "\n" + s.Sequence))
}

type notificationAckRecord struct {
	Version int                  `json:"version"`
	ID      string               `json:"id"`
	Scope   NotificationAckScope `json:"scope"`
}

type NotificationAckStatus struct {
	State       string                   `json:"state"`
	IntentID    string                   `json:"intent_id,omitempty"`
	Scope       NotificationAckScope     `json:"scope"`
	Observation *QueueNotificationOutput `json:"observation,omitempty"`
}

func readNotificationAckFile(dir *os.File, name string) ([]byte, error) {
	f, err := composePrivateAt(dir, name, syscall.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if err == nil && len(b) > 4096 {
		err = errors.New("notification acknowledgement record too large")
	}
	return b, err
}

func notificationAckRecordMatches(b []byte, s NotificationAckScope) bool {
	var r notificationAckRecord
	return json.Unmarshal(b, &r) == nil && r.Version == 1 && r.ID == s.id() && r.Scope == s
}

func notificationAckState(dir *os.File, s NotificationAckScope) (string, error) {
	b, err := readNotificationAckFile(dir, s.id()+".json")
	if errors.Is(err, syscall.ENOENT) {
		for _, suffix := range []string{".armed", ".attempt"} {
			if _, markerErr := readNotificationAckFile(dir, s.id()+suffix); !errors.Is(markerErr, syscall.ENOENT) {
				return "blocked", nil
			}
		}
		return "none", nil
	}
	if err != nil || !notificationAckRecordMatches(b, s) {
		return "blocked", nil
	}
	arm, armErr := readNotificationAckFile(dir, s.id()+".armed")
	armed := !errors.Is(armErr, syscall.ENOENT)
	if armed && (armErr != nil || !notificationAckRecordMatches(arm, s)) {
		return "blocked", nil
	}
	b, err = readNotificationAckFile(dir, s.id()+".attempt")
	if errors.Is(err, syscall.ENOENT) {
		if armed {
			return "armed", nil
		}
		return "prepared", nil
	}
	if err != nil || !armed || !notificationAckRecordMatches(b, s) {
		return "blocked", nil
	}
	return "uncertain", nil
}

func notificationAckObservation(r *Reader, s NotificationAckScope) (QueueNotificationOutput, error) {
	o, err := r.Notification(s.Event)
	if err != nil {
		return o, err
	}
	if o.QueueIdentity != s.Queue || o.Event.JobID != s.Job || o.Event.Sequence != s.Sequence {
		return QueueNotificationOutput{}, faults.New(faults.CodeValidationFailed, "exact notification or queue identity mismatch")
	}
	return o, nil
}

// InspectNotificationAcknowledgement is read-only, including when no journal
// exists. Absent/unavailable observations after intent remain uncertain; it
// neither consumes a claim nor retries an acknowledgement.
func InspectNotificationAcknowledgement(stateDir string, s NotificationAckScope) (NotificationAckStatus, error) {
	out := NotificationAckStatus{State: "blocked", Scope: s}
	if !s.valid() {
		return out, faults.New(faults.CodeValidationFailed, "invalid local notification acknowledgement scope")
	}
	r, err := OpenReader(stateDir)
	if err != nil {
		return out, err
	}
	defer r.Close()
	fd, err := syscall.Open(r.store.dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return out, err
	}
	state := os.NewFile(uintptr(fd), r.store.dir)
	defer state.Close()
	dir, err := composeOpenPrivateDirAt(state, notificationAckJournal)
	if err == nil {
		defer dir.Close()
		out.State, err = notificationAckState(dir, s)
		if err != nil {
			return out, err
		}
	} else if errors.Is(err, syscall.ENOENT) {
		out.State = "none"
	} else {
		return out, err
	}
	if out.State != "none" {
		out.IntentID = s.id()
	}
	if out.State == "blocked" {
		return out, nil
	}
	observation, err := notificationAckObservation(r, s)
	if err != nil {
		if out.State == "none" {
			return out, err
		}
		out.State = "uncertain"
		return out, nil
	}
	out.Observation = &observation
	if observation.Event.AcknowledgedAt != nil {
		out.State = "observed"
	}
	return out, nil
}

// Prepare only persists a human-reviewable intent. It does not authorize an
// automatic later send. The event key and original space are immutable.
func PrepareNotificationAcknowledgement(stateDir string, s NotificationAckScope) (NotificationAckStatus, error) {
	if !s.valid() {
		return NotificationAckStatus{}, faults.New(faults.CodeValidationFailed, "invalid local notification acknowledgement scope")
	}
	initial, err := InspectNotificationAcknowledgement(stateDir, s)
	if err != nil || initial.State != "none" {
		return initial, err
	}
	j, err := openLocalJournal(stateDir, notificationAckJournal)
	if err != nil {
		return NotificationAckStatus{}, err
	}
	defer j.close()
	if err := j.acquire(); err != nil {
		return NotificationAckStatus{}, err
	}
	state, err := notificationAckState(j.dir, s)
	if err != nil {
		return NotificationAckStatus{}, err
	}
	if state == "none" {
		// Recheck under the stable queue-wide journal lock before creating intent.
		r, err := OpenReader(stateDir)
		if err != nil {
			return NotificationAckStatus{}, err
		}
		observation, readErr := notificationAckObservation(r, s)
		err = errors.Join(readErr, r.Close())
		if err != nil {
			return NotificationAckStatus{}, err
		}
		initial.Observation = &observation
		if observation.Event.AcknowledgedAt != nil {
			return NotificationAckStatus{State: "observed", Scope: s, Observation: &observation}, nil
		}
		if err := j.create(s.id()+".json", notificationAckRecord{1, s.id(), s}); err != nil {
			return NotificationAckStatus{}, err
		}
	} else if state != "prepared" {
		return NotificationAckStatus{State: state, IntentID: s.id(), Scope: s}, nil
	}
	return NotificationAckStatus{State: "prepared", IntentID: s.id(), Scope: s, Observation: initial.Observation}, nil
}

// Arm is a set-once durable claim, separate from the SQL mutation process. A lost
// claim response is never permission to send, and recovery never dispatches it.
func ArmNotificationAcknowledgement(stateDir string, s NotificationAckScope, id string) (NotificationAckStatus, error) {
	if !s.valid() || id != s.id() {
		return NotificationAckStatus{}, faults.New(faults.CodeValidationFailed, "notification acknowledgement intent mismatch")
	}
	j, err := openLocalJournal(stateDir, notificationAckJournal)
	if err != nil {
		return NotificationAckStatus{}, err
	}
	defer j.close()
	if err := j.acquire(); err != nil {
		return NotificationAckStatus{}, err
	}
	state, err := notificationAckState(j.dir, s)
	if err != nil {
		return NotificationAckStatus{}, err
	}
	if state != "prepared" {
		return NotificationAckStatus{}, faults.New(faults.CodeValidationFailed, "notification intent already claimed or blocked; check only, never resend")
	}
	r, err := OpenReader(stateDir)
	if err != nil {
		return NotificationAckStatus{}, err
	}
	o, err := notificationAckObservation(r, s)
	err = errors.Join(err, r.Close())
	if err != nil {
		return NotificationAckStatus{}, err
	}
	if o.Event.AcknowledgedAt != nil {
		return NotificationAckStatus{State: "observed", IntentID: id, Scope: s, Observation: &o}, nil
	}
	if err := j.syncFile(id + ".json"); err != nil {
		return NotificationAckStatus{}, err
	}
	if err := j.create(id+".armed", notificationAckRecord{1, id, s}); err != nil {
		return NotificationAckStatus{}, err
	}
	return NotificationAckStatus{State: "armed", IntentID: id, Scope: s}, nil
}

// Attempt persists the single-attempt marker and synchronizes it BEFORE opening
// the writer. A crash, duplicate call, delayed panel or missing receipt cannot
// authorize a second SQL acknowledgement. Once confirmed, this call may finish
// after the person leaves the originating space; it targets only that intent.
func AttemptNotificationAcknowledgement(stateDir string, s NotificationAckScope, id string) (NotificationAckStatus, error) {
	if !s.valid() || id != s.id() {
		return NotificationAckStatus{}, faults.New(faults.CodeValidationFailed, "notification acknowledgement intent mismatch")
	}
	j, err := openLocalJournal(stateDir, notificationAckJournal)
	if err != nil {
		return NotificationAckStatus{}, err
	}
	defer j.close()
	if err := j.acquire(); err != nil {
		return NotificationAckStatus{}, err
	}
	state, err := notificationAckState(j.dir, s)
	if err != nil {
		return NotificationAckStatus{}, err
	}
	if state != "armed" {
		return NotificationAckStatus{State: state, IntentID: id, Scope: s}, faults.New(faults.CodeValidationFailed, "notification acknowledgement is not an unattempted claim; check exact event, never resend")
	}
	// Refuse changed identity/context before consuming an otherwise valid claim.
	r, err := OpenReader(stateDir)
	if err != nil {
		return NotificationAckStatus{}, err
	}
	o, err := notificationAckObservation(r, s)
	err = errors.Join(err, r.Close())
	if err != nil {
		return NotificationAckStatus{}, err
	}
	if o.Event.AcknowledgedAt != nil {
		return NotificationAckStatus{State: "observed", IntentID: id, Scope: s, Observation: &o}, nil
	}
	if err := j.syncFile(id + ".json"); err != nil {
		return NotificationAckStatus{}, err
	}
	if err := j.syncFile(id + ".armed"); err != nil {
		return NotificationAckStatus{}, err
	}
	if err := j.create(id+".attempt", notificationAckRecord{1, id, s}); err != nil {
		return NotificationAckStatus{}, err
	}
	out := NotificationAckStatus{State: "uncertain", IntentID: id, Scope: s}
	a, err := OpenExistingNotificationAcknowledger(stateDir)
	if err != nil {
		return out, nil
	}
	n, _ := strconv.ParseInt(s.Sequence, 10, 64)
	_, ackErr := a.Acknowledge(s.Event, s.Job, n, s.Queue)
	closeErr := a.Close()
	if ackErr != nil || closeErr != nil {
		return out, nil
	}
	// Only a separate exact read, not the write/client return, establishes observation.
	return InspectNotificationAcknowledgement(stateDir, s)
}
