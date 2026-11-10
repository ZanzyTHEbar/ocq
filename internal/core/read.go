package core

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ZanzyTHEbar/faults-go"

	"ocq/internal/core/dbquery"
)

// Reader provides non-initializing, non-consuming stored project views.
// It never starts, reconciles, or acknowledges work.
type Reader struct{ store *store }

// OpenReader requires an existing private schema-5 store on supported local storage.
func OpenReader(stateDir string) (_ *Reader, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeRuntimeFailed, resultErr) }()
	s, err := openReadOnlyStore(stateDir)
	if err != nil {
		return nil, err
	}
	return &Reader{store: s}, nil
}

func (r *Reader) Close() error {
	return classifyDiagnostic(faults.CodeDatabaseRead, r.store.database.Close())
}

type ProjectSummary struct {
	ProjectID                 string `json:"project_id"`
	Label                     string `json:"label"`
	BindingRevision           int    `json:"binding_revision"`
	RepoRoot                  string `json:"repo_root"`
	IntegratorUnreadMailCount int    `json:"integrator_unread_mail_count"`
}

type ProjectList struct {
	ReadAt    string           `json:"read_at"`
	Freshness string           `json:"freshness"`
	Projects  []ProjectSummary `json:"projects"`
}

// Projects returns the stored list; repository and session availability are not checked.
func (r *Reader) Projects() (ProjectList, error) { return r.store.projects() }

// Project, Results, Job and Manifest inspect retained data without consuming it.
func (r *Reader) Project(id string) (ProjectShowOutput, error) {
	return r.store.projectShowData(id)
}

func (r *Reader) Results(id string, afterSequence int64, limit int) (ProjectResultsOutput, error) {
	return r.store.projectResultsData(id, afterSequence, limit)
}

// RecentResults returns the newest retained events and their current stored details.
func (r *Reader) RecentResults(id string, limit int) (ProjectResultsOutput, error) {
	return r.store.projectResultsPage(id, 0, limit, true)
}

// MailByKey returns retained mail only when both the project and globally
// unique idempotency key match. It does not acknowledge or change the message.
// A missing key or a key owned by another project is classified as not found.
func (r *Reader) MailByKey(projectID, key string) (_ ProjectInboxMessage, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeDatabaseRead, resultErr) }()
	if !isValidProjectID(projectID) || !isValidInboxKey(key) {
		return ProjectInboxMessage{}, faults.New(faults.CodeValidationFailed, "mail lookup requires a valid project ID and idempotency key")
	}
	row, err := dbquery.New(r.store.db).InboxMessageByKey(context.Background(), key)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && row.ProjectID != projectID) {
		return ProjectInboxMessage{}, faults.Wrap(faults.CodeNotFound, "exact project mail key not found", sql.ErrNoRows)
	}
	if err != nil {
		return ProjectInboxMessage{}, err
	}
	return inboxMessageFromRow(dbquery.InboxPageRow(row)), nil
}

func (r *Reader) Job(projectID, jobID string) (ProjectJobDetailOutput, error) {
	return r.store.projectJobData(projectID, jobID)
}

func (r *Reader) Manifest(projectID, digest string) ([]byte, error) {
	return r.store.projectManifestBytes(projectID, digest)
}

// Board contains separately timed project and inbox reads, not a live snapshot.
type Board struct {
	Project    ProjectShowOutput     `json:"project"`
	MailReadAt string                `json:"mail_read_at"`
	Messages   []ProjectInboxMessage `json:"messages"`
}

// Board returns the project and first 20 unread integrator messages.
func (r *Reader) Board(projectID string) (Board, error) { return r.store.board(projectID) }

func (s *store) board(projectID string) (Board, error) {
	var out Board
	show, err := s.projectShowData(projectID)
	if err != nil {
		return out, err
	}
	rows, err := dbquery.New(s.db).BoardUnreadMessages(context.Background(), dbquery.BoardUnreadMessagesParams{ProjectID: projectID, Recipient: "integrator:" + projectID})
	if err != nil {
		return out, classifyDiagnostic(faults.CodeDatabaseRead, err)
	}
	messages := []ProjectInboxMessage{}
	for _, row := range rows {
		messages = append(messages, inboxMessageFromRow(dbquery.InboxPageRow(row)))
	}
	return Board{Project: show, MailReadAt: now(), Messages: messages}, nil
}
