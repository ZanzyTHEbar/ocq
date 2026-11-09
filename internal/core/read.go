package core

import (
	"context"

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
