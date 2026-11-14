package core

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"strconv"

	"github.com/ZanzyTHEbar/faults-go"

	"ocq/internal/core/dbquery"
)

// QueueJobSummary deliberately excludes requests, process internals and ownership
// capabilities. Recorded agent locations are not observations of live availability.
type QueueJobSummary struct {
	ID                string `json:"id"`
	Sequence          string `json:"sequence"`
	Kind              string `json:"kind"`
	State             string `json:"state"`
	Reason            string `json:"reason"`
	Outcome           string `json:"outcome"`
	ExitCode          *int64 `json:"exit_code"`
	Validation        string `json:"validation"`
	AcceptedAt        string `json:"accepted_at"`
	SessionID         string `json:"recorded_session_id"`
	Worktree          string `json:"recorded_worktree"`
	HasStdout         bool   `json:"has_stdout"`
	HasStderr         bool   `json:"has_stderr"`
	CompletionEventID string `json:"completion_event_id,omitempty"`
}

func queueSummary(row dbquery.QueueJobByIDRow) QueueJobSummary {
	item := QueueJobSummary{ID: row.ID, Sequence: strconv.FormatInt(row.Sequence, 10), Kind: row.Kind, State: row.State, Reason: row.Reason, Outcome: row.Outcome, Validation: row.Validation, AcceptedAt: row.AcceptedAt, SessionID: row.SessionID, Worktree: row.Worktree, HasStdout: row.HasStdout, HasStderr: row.HasStderr}
	item.CompletionEventID = row.CompletionEventID
	if row.ExitCode.Valid {
		code := row.ExitCode.Int64
		item.ExitCode = &code
	}
	return item
}

type QueueJobsOutput struct {
	ReadAt  string            `json:"read_at"`
	Items   []QueueJobSummary `json:"items"`
	Cursor  string            `json:"cursor"`
	HasMore bool              `json:"has_more"`
}

// QueueJobs reads one bounded FIFO receipt page in a single SQLite statement.
func (r *Reader) QueueJobs(after int64, limit int) (_ QueueJobsOutput, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeDatabaseRead, resultErr) }()
	if after < 0 || limit < 1 || limit > 100 {
		return QueueJobsOutput{}, faults.New(faults.CodeValidationFailed, "queue page requires a non-negative cursor and limit 1..100")
	}
	readAt := now()
	rows, err := dbquery.New(r.store.db).QueueJobsPage(context.Background(), dbquery.QueueJobsPageParams{Sequence: after, Limit: int64(limit + 1)})
	if err != nil {
		return QueueJobsOutput{}, err
	}
	out := QueueJobsOutput{ReadAt: readAt, Items: []QueueJobSummary{}, Cursor: strconv.FormatInt(after, 10), HasMore: len(rows) > limit}
	if out.HasMore {
		rows = rows[:limit]
	}
	for _, row := range rows {
		item := queueSummary(dbquery.QueueJobByIDRow(row))
		out.Items = append(out.Items, item)
		out.Cursor = item.Sequence
	}
	return out, nil
}

type QueueJobOutput struct {
	QueueIdentity string          `json:"queue_identity"`
	ReadAt        string          `json:"read_at"`
	Job           QueueJobSummary `json:"job"`
}

func (r *Reader) QueueJob(id string) (_ QueueJobOutput, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeDatabaseRead, resultErr) }()
	readAt := now()
	row, err := dbquery.New(r.store.db).QueueJobByID(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return QueueJobOutput{}, faults.New(faults.CodeNotFound, "exact queue job not found")
	}
	if err != nil {
		return QueueJobOutput{}, err
	}
	identity, err := notificationQueueIdentity(r.store)
	if err != nil {
		return QueueJobOutput{}, err
	}
	return QueueJobOutput{QueueIdentity: identity, ReadAt: readAt, Job: queueSummary(row)}, nil
}

const QueueLogPageBytes = 32 * 1024

// Bytes are base64 encoded by encoding/json. EOF and byte counts describe only
// this file read, never job completion or future output availability.
type QueueLogOutput struct {
	ReadAt       string `json:"read_at"`
	JobID        string `json:"job_id"`
	Stream       string `json:"stream"`
	Offset       string `json:"offset"`
	NextOffset   string `json:"next_offset"`
	ObservedSize string `json:"observed_size"`
	Bytes        []byte `json:"bytes_base64"`
	EOF          bool   `json:"eof"`
}

func (r *Reader) QueueLog(id, stream string, offset int64) (_ QueueLogOutput, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeStorageFailed, resultErr) }()
	if offset < 0 || (stream != "stdout" && stream != "stderr") {
		return QueueLogOutput{}, faults.New(faults.CodeValidationFailed, "log requires stdout|stderr and non-negative byte offset")
	}
	readAt := now()
	f, err := r.store.openLog(id, stream == "stderr")
	if err != nil {
		return QueueLogOutput{}, err
	}
	defer f.Close()
	return readQueueLog(f, id, stream, offset, readAt)
}

func readQueueLog(f *os.File, id, stream string, offset int64, readAt string) (QueueLogOutput, error) {
	info, err := f.Stat()
	if err != nil {
		return QueueLogOutput{}, err
	}
	// Limit to the size observed before the read, even if a producer appends.
	count := int64(QueueLogPageBytes)
	if offset > info.Size() {
		count = 0
	} else if count > info.Size()-offset {
		count = info.Size() - offset
	}
	data := make([]byte, int(count))
	n, err := f.ReadAt(data, offset)
	if err != nil && err != io.EOF {
		return QueueLogOutput{}, err
	}
	next := offset + int64(n)
	return QueueLogOutput{ReadAt: readAt, JobID: id, Stream: stream, Offset: strconv.FormatInt(offset, 10), NextOffset: strconv.FormatInt(next, 10), ObservedSize: strconv.FormatInt(info.Size(), 10), Bytes: data[:n], EOF: next >= info.Size() || err == io.EOF}, nil
}

type QueueNotification struct {
	ID             string  `json:"id"`
	Sequence       string  `json:"sequence"`
	JobID          string  `json:"job_id"`
	State          string  `json:"state"`
	Outcome        string  `json:"outcome"`
	ExitCode       *int    `json:"exit_code"`
	Validation     string  `json:"validation"`
	Origin         string  `json:"origin"`
	RecordedAt     string  `json:"recorded_at"`
	AcknowledgedAt *string `json:"acknowledged_at"`
}

func queueNotification(n Notification) QueueNotification {
	return QueueNotification{n.ID, strconv.FormatInt(n.Sequence, 10), n.JobID, n.State, n.Outcome, n.ExitCode, n.Validation, n.Origin, n.RecordedAt, n.AcknowledgedAt}
}

type QueueNotificationsOutput struct {
	ReadAt  string              `json:"read_at"`
	Items   []QueueNotification `json:"items"`
	HasMore bool                `json:"has_more"`
}

// PendingNotifications is non-consuming. Exact lookup also includes acknowledged events.
func (r *Reader) PendingNotifications(limit int) (QueueNotificationsOutput, error) {
	if limit < 1 || limit > 100 {
		return QueueNotificationsOutput{}, faults.New(faults.CodeValidationFailed, "notification limit must be 1..100")
	}
	readAt := now()
	rows, err := dbquery.New(r.store.db).PendingNotifications(context.Background(), int64(limit+1))
	if err != nil {
		return QueueNotificationsOutput{}, classifyDiagnostic(faults.CodeDatabaseRead, err)
	}
	out := QueueNotificationsOutput{ReadAt: readAt, Items: []QueueNotification{}, HasMore: len(rows) > limit}
	if out.HasMore {
		rows = rows[:limit]
	}
	for _, row := range rows {
		out.Items = append(out.Items, queueNotification(notificationFromRow(dbquery.NotificationByIDRow(row))))
	}
	return out, nil
}

type QueueNotificationOutput struct {
	ReadAt        string            `json:"read_at"`
	QueueIdentity string            `json:"queue_identity"`
	Event         QueueNotification `json:"event"`
}

func (r *Reader) Notification(id string) (QueueNotificationOutput, error) {
	readAt := now()
	identity, err := notificationQueueIdentity(r.store)
	if err != nil {
		return QueueNotificationOutput{}, err
	}
	row, err := dbquery.New(r.store.db).NotificationByID(context.Background(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return QueueNotificationOutput{}, faults.New(faults.CodeNotFound, "exact notification event not found")
	}
	if err != nil {
		return QueueNotificationOutput{}, classifyDiagnostic(faults.CodeDatabaseRead, err)
	}
	after, err := notificationQueueIdentity(r.store)
	if err != nil || after != identity {
		return QueueNotificationOutput{}, faults.New(faults.CodeStorageFailed, "queue identity changed during notification read")
	}
	return QueueNotificationOutput{ReadAt: readAt, QueueIdentity: identity, Event: queueNotification(notificationFromRow(row))}, nil
}
