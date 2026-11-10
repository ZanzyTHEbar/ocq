package core

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/ZanzyTHEbar/faults-go"
)

// ProjectIntake accepts exact retained requests against an existing schema-5
// queue. The caller owns durable intent and ambiguous-outcome handling; this
// boundary does not journal requests, retry, run the dispatcher or approve tools.
type ProjectIntake struct{ store *store }

// OpenExistingProjectIntake never initializes or migrates a queue.
func OpenExistingProjectIntake(stateDir string) (_ *ProjectIntake, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeRuntimeFailed, resultErr) }()
	s, err := openExistingWritableStore(stateDir)
	if err != nil {
		return nil, err
	}
	s.existingSchemaOnly = true
	return &ProjectIntake{store: s}, nil
}

func (i *ProjectIntake) Close() error {
	return classifyDiagnostic(faults.CodeDatabaseWrite, i.store.database.Close())
}

// RegisterProject retains the core's exact-byte receipt-first and binding CAS
// rules, including live repo/session verification for a new key.
func (i *ProjectIntake) RegisterProject(raw []byte) (ProjectRegistrationResult, error) {
	return (&Queue{store: i.store}).RegisterProject(raw)
}

// SubmitProject retains the core's exact-envelope receipt-first and binding CAS
// rules. A successful new submission queues work; it does not launch it itself.
func (i *ProjectIntake) SubmitProject(raw []byte) (ProjectSubmissionResult, error) {
	return (&Queue{store: i.store}).SubmitProject(raw)
}

func requireProjectSchema5(q sqlQueryer) error {
	var version int
	if err := q.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&version); err != nil {
		return classifyDiagnostic(faults.CodeDatabaseRead, err)
	}
	if version != 5 {
		return faults.New(faults.CodeConfigInvalid, fmt.Sprintf("existing project store requires schema version 5, got %d", version))
	}
	return nil
}

// The version check and lookup use the same connection and SQLite snapshot.
func (s *store) readSchema5(read func(sqlQueryer) error) error {
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return classifyDiagnostic(faults.CodeDatabaseRead, err)
	}
	defer tx.Rollback()
	if err := requireProjectSchema5(tx); err != nil {
		return err
	}
	if err := read(tx); err != nil {
		return err
	}
	return classifyDiagnostic(faults.CodeDatabaseRead, tx.Commit())
}

// ProjectSubmissionReceipt contains immutable receipt metadata only; it omits
// the private request and runtime ownership token.
type ProjectSubmissionReceipt struct {
	Key                string  `json:"key"`
	JobID              string  `json:"job_id"`
	ProjectID          string  `json:"project_id"`
	BindingRevision    int     `json:"binding_revision"`
	Role               string  `json:"role"`
	BriefDigest        string  `json:"brief_digest"`
	TargetJobID        *string `json:"target_job_id"`
	TargetResultDigest *string `json:"target_result_digest"`
	EnvelopeDigest     string  `json:"envelope_digest"`
	RecordedAt         string  `json:"recorded_at"`
}

// RegistrationReceipt matches the exact original registration bytes. A missing
// receipt is not proof that a delayed registration cannot still commit.
func (r *Reader) RegistrationReceipt(raw []byte) (_ ProjectRegistrationResult, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeDatabaseRead, resultErr) }()
	req, _, digest, err := parseRegistrationPayload(raw)
	if err != nil {
		return ProjectRegistrationResult{}, faults.Wrap(faults.CodeValidationFailed, "invalid project registration", err)
	}
	var result ProjectRegistrationResult
	var found bool
	err = r.store.readSchema5(func(tx sqlQueryer) error {
		var err error
		result, found, err = r.store.lookupProjectRegistration(tx, req.Key, raw, digest)
		return err
	})
	if err != nil {
		return ProjectRegistrationResult{}, err
	}
	if !found {
		return ProjectRegistrationResult{}, faults.New(faults.CodeNotFound, "exact registration receipt not found")
	}
	return result, nil
}

// SubmissionReceipt matches the exact original envelope and its retained
// request and manifests without consuming or changing any queue state. A missing
// receipt is not proof that a delayed submission cannot still commit.
func (r *Reader) SubmissionReceipt(raw []byte) (_ ProjectSubmissionReceipt, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeDatabaseRead, resultErr) }()
	parsed, err := parseSubmissionEnvelope(raw)
	if err != nil {
		return ProjectSubmissionReceipt{}, faults.Wrap(faults.CodeValidationFailed, "invalid project submission", err)
	}
	var meta submissionRowMetadata
	var found bool
	err = r.store.readSchema5(func(tx sqlQueryer) error {
		var err error
		meta, found, err = r.store.lookupProjectSubmissionRow(context.Background(), tx, parsed)
		return err
	})
	if err != nil {
		return ProjectSubmissionReceipt{}, err
	}
	if !found {
		return ProjectSubmissionReceipt{}, faults.New(faults.CodeNotFound, "exact project submission receipt not found")
	}
	return ProjectSubmissionReceipt{
		Key: parsed.Envelope.Key, JobID: meta.jobID, ProjectID: meta.projectID,
		BindingRevision: meta.bindingRevision, Role: meta.role, BriefDigest: meta.briefDigest,
		TargetJobID: meta.targetJobID, TargetResultDigest: meta.targetResultDigest,
		EnvelopeDigest: meta.envelopeDigest, RecordedAt: meta.recordedAt,
	}, nil
}
