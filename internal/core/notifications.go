package core

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ZanzyTHEbar/faults-go"

	"ocq/internal/core/dbquery"
)

type Notification struct {
	ID             string  `json:"id"`
	Sequence       int64   `json:"sequence"`
	JobID          string  `json:"job_id"`
	State          string  `json:"state"`
	Outcome        string  `json:"outcome"`
	ExitCode       *int    `json:"exit_code"`
	Validation     string  `json:"validation"`
	Origin         string  `json:"origin"`
	RecordedAt     string  `json:"recorded_at"`
	AcknowledgedAt *string `json:"acknowledged_at"`
}

func notificationFromRow(row dbquery.NotificationByIDRow) Notification {
	n := Notification{ID: row.ID, Sequence: row.Sequence, JobID: row.JobID, State: row.State, Outcome: row.Outcome, Validation: row.Validation, Origin: row.Origin, RecordedAt: row.RecordedAt}
	if row.ExitCode.Valid {
		code := int(row.ExitCode.Int64)
		n.ExitCode = &code
	}
	if row.AcknowledgedAt.Valid {
		n.AcknowledgedAt = &row.AcknowledgedAt.String
	}
	return n
}

func (s *store) notifications(limit int) ([]Notification, error) {
	if limit < 1 || limit > 100 {
		return nil, faults.New(faults.CodeValidationFailed, "notifications --limit must be between 1 and 100")
	}
	rows, err := dbquery.New(s.db).PendingNotifications(context.Background(), int64(limit))
	if err != nil {
		return nil, classifyDiagnostic(faults.CodeDatabaseRead, err)
	}
	items := make([]Notification, 0, len(rows))
	for _, row := range rows {
		items = append(items, notificationFromRow(dbquery.NotificationByIDRow(row)))
	}
	return items, nil
}

func (s *store) acknowledge(id string) (Notification, error) {
	return s.acknowledgeChecked(id, nil)
}

// Both the CLI and the existing-store bridge writer use this same monotone SQL.
// The bridge additionally verifies its exact retained observation in the write.
func (s *store) acknowledgeChecked(id string, check func(Notification) error) (Notification, error) {
	if id == "" {
		return Notification{}, faults.New(faults.CodeValidationFailed, "ack requires one exact notification event ID")
	}
	var committed Notification
	err := s.write(func(c *sql.Conn) error {
		queries := dbquery.New(c)
		if check != nil {
			row, err := queries.NotificationByID(context.Background(), id)
			if errors.Is(err, sql.ErrNoRows) {
				return faults.New(faults.CodeNotFound, "notification not found; use its exact event ID, not a job ID")
			}
			if err != nil {
				return classifyDiagnostic(faults.CodeDatabaseRead, err)
			}
			if err := check(notificationFromRow(row)); err != nil {
				return err
			}
		}
		count, err := queries.AcknowledgeNotification(context.Background(), id)
		if err != nil {
			return classifyDiagnostic(faults.CodeDatabaseWrite, err)
		}
		if count != 1 {
			return faults.New(faults.CodeNotFound, "notification not found; use its exact event ID, not a job ID")
		}
		row, err := queries.NotificationByID(context.Background(), id)
		if err != nil {
			return classifyDiagnostic(faults.CodeDatabaseRead, err)
		}
		committed = notificationFromRow(row)
		return nil
	})
	if err != nil {
		return Notification{}, err
	}
	return committed, nil
}
