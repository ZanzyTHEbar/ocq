package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/ZanzyTHEbar/faults-go"

	"ocq/internal/core/dbquery"
)

type sqlQueryer = dbquery.DBTX

func currentBindingRevision(ctx context.Context, q sqlQueryer, projectID string) (int, error) {
	value, err := dbquery.New(q).CurrentBindingRevision(ctx, projectID)
	if err != nil {
		return 0, classifyDiagnostic(faults.CodeDatabaseRead, err)
	}
	var revision sql.NullInt64
	if err := revision.Scan(value); err != nil {
		return 0, classifyDiagnostic(faults.CodeInvariantFailed, err)
	}
	if !revision.Valid {
		return 0, faults.New(faults.CodeInvariantFailed, "NULL binding revision")
	}
	return int(revision.Int64), nil
}

func sqlStringPtr(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

// RegisterProject accepts the exact retained registration bytes for idempotency.
func (q *Queue) RegisterProject(rawBytes []byte) (_ ProjectRegistrationResult, resultErr error) {
	return q.registerProject(rawBytes, verifyRegistrationLive)
}

// Main's explicit-server verifier avoids api's automatic service startup while
// retaining this SAME receipt-first registration and transactional CAS authority.
func (q *Queue) registerProject(rawBytes []byte, verifyLive func(context.Context, ProjectRegistrationRequest) (string, string, string, error)) (_ ProjectRegistrationResult, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeRuntimeFailed, resultErr) }()
	s := q.store
	var empty ProjectRegistrationResult
	req, handoff, regDigest, err := parseRegistrationPayload(rawBytes)
	if err != nil {
		return empty, faults.Wrap(faults.CodeValidationFailed, "invalid project registration", err)
	}

	// B1 rule: check project_registrations by key FIRST before any live repo/session check.
	if existing, found, err := s.lookupProjectRegistration(s.db, req.Key, rawBytes, regDigest); err != nil {
		return empty, err
	} else if found {
		return existing, nil
	}

	ctx := context.Background()
	repoRoot, gitCommonDir, verifiedAt, verifyErr := verifyLive(ctx, req)
	if verifyErr != nil {
		// Recheck key in case a concurrent identical call won while live checks ran.
		if existing, found, err := s.lookupProjectRegistration(s.db, req.Key, rawBytes, regDigest); err == nil && found {
			return existing, nil
		}
		return empty, verifyErr
	}

	err = s.write(func(c *sql.Conn) error {
		if _, found, err := s.lookupProjectRegistration(c, req.Key, rawBytes, regDigest); err != nil {
			return err
		} else if found {
			return nil
		}

		ts := now()
		storedLabel, err := dbquery.New(c).ProjectLabel(ctx, req.ProjectID)
		if *req.ExpectedRevision == 0 {
			if err == nil {
				return faults.New(faults.CodeValidationFailed, fmt.Sprintf("project %s already exists; expected_revision 0 requires absent project", req.ProjectID))
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return classifyDiagnostic(faults.CodeDatabaseRead, err)
			}
			if err := dbquery.New(c).InsertProject(ctx, dbquery.InsertProjectParams{ID: req.ProjectID, Label: req.Label, CreatedAt: ts}); err != nil {
				return classifyDiagnostic(faults.CodeDatabaseWrite, err)
			}
		} else {
			if errors.Is(err, sql.ErrNoRows) {
				return faults.Wrap(faults.CodeNotFound, fmt.Sprintf("project %s does not exist for rebind at expected_revision %d", req.ProjectID, *req.ExpectedRevision), err)
			}
			if err != nil {
				return classifyDiagnostic(faults.CodeDatabaseRead, err)
			}
			if storedLabel != req.Label {
				return faults.New(faults.CodeValidationFailed, fmt.Sprintf("project %s label %q does not match stored label %q", req.ProjectID, req.Label, storedLabel))
			}
			currentRev, err := currentBindingRevision(ctx, c, req.ProjectID)
			if err != nil {
				return err
			}
			if currentRev != *req.ExpectedRevision {
				return faults.New(faults.CodeValidationFailed, fmt.Sprintf("project %s binding revision CAS mismatch: current=%d expected=%d", req.ProjectID, currentRev, *req.ExpectedRevision))
			}
		}

		if err := insertManifestConn(ctx, c, handoff, ts); err != nil {
			return err
		}
		newRev := *req.ExpectedRevision + 1
		queries := dbquery.New(c)
		if err := queries.InsertBinding(ctx, dbquery.InsertBindingParams{ProjectID: req.ProjectID, Revision: int64(newRev), RepoRoot: repoRoot, GitCommonDir: gitCommonDir, CommitAnchor: req.CommitAnchor, ElephantSessionID: req.ElephantSessionID, OpencodeProjectID: req.ExpectedOpenCodeProjectID, OpencodeDirectory: req.ExpectedOpenCodeDirectory, HandoffManifestDigest: handoff.Digest, AuthRef: req.AuthRef, VerifiedAt: verifiedAt, RecordedAt: ts}); err != nil {
			return classifyDiagnostic(faults.CodeDatabaseWrite, err)
		}
		return classifyDiagnostic(faults.CodeDatabaseWrite, queries.InsertRegistration(ctx, dbquery.InsertRegistrationParams{Key: req.Key, Digest: regDigest, Canonical: rawBytes, ProjectID: req.ProjectID, Revision: int64(newRev), RecordedAt: ts}))
	})
	if err != nil {
		return empty, classifyDiagnostic(faults.CodeDatabaseWrite, err)
	}
	res, found, err := s.lookupProjectRegistration(s.db, req.Key, rawBytes, regDigest)
	if err != nil {
		return empty, err
	}
	if !found {
		return empty, faults.New(faults.CodeInvariantFailed, "registration receipt missing after commit")
	}
	return res, nil
}

func (s *store) lookupProjectRegistration(q sqlQueryer, key string, expectedCanonical []byte, expectedDigest string) (ProjectRegistrationResult, bool, error) {
	if s.existingSchemaOnly && q == s.db {
		var result ProjectRegistrationResult
		var found bool
		err := s.readSchema5(func(tx sqlQueryer) error {
			var err error
			result, found, err = s.lookupProjectRegistration(tx, key, expectedCanonical, expectedDigest)
			return err
		})
		return result, found, err
	}
	ctx := context.Background()
	var res ProjectRegistrationResult
	queries := dbquery.New(q)
	row, err := queries.RegistrationByKey(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return res, false, nil
	}
	if err != nil {
		return res, false, classifyDiagnostic(faults.CodeDatabaseRead, err)
	}
	if row.Digest != expectedDigest || !bytes.Equal(row.Canonical, expectedCanonical) {
		return res, false, faults.New(faults.CodeValidationFailed, "registration key already belongs to a different registration payload")
	}
	res.Key = key
	res.Digest = row.Digest
	res.RegistrationDigest = row.Digest
	res.ProjectID = row.ProjectID
	res.Revision = int(row.Revision)
	res.BindingRevision = int(row.Revision)
	binding, err := queries.RegistrationBinding(ctx, dbquery.RegistrationBindingParams{ProjectID: row.ProjectID, Revision: row.Revision})
	if err != nil {
		return res, false, classifyDiagnostic(faults.CodeDatabaseRead, err)
	}
	res.Label, res.CreatedAt, res.RepoRoot, res.GitCommonDir, res.CommitAnchor = binding.Label, binding.CreatedAt, binding.RepoRoot, binding.GitCommonDir, binding.CommitAnchor
	res.ElephantSessionID, res.OpenCodeProjectID, res.OpenCodeDirectory = binding.ElephantSessionID, binding.OpencodeProjectID, binding.OpencodeDirectory
	res.HandoffManifestDigest, res.AuthRef, res.VerifiedAt, res.RecordedAt = binding.HandoffManifestDigest, binding.AuthRef, binding.VerifiedAt, binding.RecordedAt
	return res, true, nil
}

func canonicalizeGitRepo(repoPath string, commitAnchor string) (string, string, error) {
	info, err := os.Stat(repoPath)
	if err != nil || !info.IsDir() {
		return "", "", faults.New(faults.CodeValidationFailed, fmt.Sprintf("repository path is not an existing directory: %s", repoPath))
	}
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return "", "", err
	}
	eval, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", "", err
	}
	top, err := gitRead(eval, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", err
	}
	top, err = filepath.EvalSymlinks(top)
	if err != nil {
		return "", "", err
	}
	if top != repoPath {
		return "", "", faults.New(faults.CodeValidationFailed, fmt.Sprintf("supplied repo %q is not the canonical repository root %q", repoPath, top))
	}
	common, err := gitRead(top, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", "", err
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return "", "", err
	}
	pinned, err := gitRead(top, "rev-parse", "--verify", "--end-of-options", commitAnchor+"^{commit}")
	if err != nil {
		return "", "", faults.Wrap(faults.CodeValidationFailed, fmt.Sprintf("commit anchor %s verification failed", commitAnchor), err)
	}
	if pinned != commitAnchor {
		return "", "", faults.New(faults.CodeValidationFailed, fmt.Sprintf("commit anchor %s resolved to %s; exact 40-hex commit SHA required", commitAnchor, pinned))
	}
	return top, common, nil
}

func verifyElephantSession(ctx context.Context, expectedDir, expectedProjectID, elephantSessionID string) error {
	info, err := os.Stat(expectedDir)
	if err != nil || !info.IsDir() {
		return faults.New(faults.CodeValidationFailed, fmt.Sprintf("expected_opencode_directory is not an existing directory: %s", expectedDir))
	}
	abs, err := filepath.Abs(expectedDir)
	if err != nil {
		return err
	}
	eval, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return err
	}
	if eval != expectedDir {
		return faults.New(faults.CodeValidationFailed, fmt.Sprintf("expected_opencode_directory %q is not canonical (%q)", expectedDir, eval))
	}
	sInfo, err := getSession(ctx, expectedDir, elephantSessionID)
	if err != nil {
		return err
	}
	if sInfo.ID != elephantSessionID {
		return faults.New(faults.CodeValidationFailed, fmt.Sprintf("Elephant session ID mismatch: got %q want %q", sInfo.ID, elephantSessionID))
	}
	if sInfo.ProjectID == "" || sInfo.ProjectID != expectedProjectID {
		return faults.New(faults.CodeValidationFailed, fmt.Sprintf("Elephant session projectID mismatch: got %q want %q", sInfo.ProjectID, expectedProjectID))
	}
	if sInfo.Location.Directory == "" || sInfo.Location.Directory != expectedDir {
		return faults.New(faults.CodeValidationFailed, fmt.Sprintf("Elephant session directory mismatch: got %q want %q", sInfo.Location.Directory, expectedDir))
	}
	return nil
}

func verifyRegistrationLive(ctx context.Context, req ProjectRegistrationRequest) (string, string, string, error) {
	repoRoot, gitCommonDir, err := canonicalizeGitRepo(req.Repo, req.CommitAnchor)
	if err != nil {
		return "", "", "", err
	}
	if err := verifyElephantSession(ctx, req.ExpectedOpenCodeDirectory, req.ExpectedOpenCodeProjectID, req.ElephantSessionID); err != nil {
		return "", "", "", err
	}
	return repoRoot, gitCommonDir, now(), nil
}

func insertManifestConn(ctx context.Context, c *sql.Conn, m parsedManifest, ts string) error {
	queries := dbquery.New(c)
	existing, err := queries.ProjectManifest(ctx, m.Digest)
	if err == nil {
		if existing.ProjectID != m.ProjectID || existing.Version != int64(m.Version) || existing.Kind != m.Kind || !bytes.Equal(existing.Canonical, m.Canonical) {
			return faults.New(faults.CodeInvariantFailed, fmt.Sprintf("manifest digest %s already exists with conflicting metadata or bytes", m.Digest))
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return classifyDiagnostic(faults.CodeDatabaseRead, err)
	}
	return classifyDiagnostic(faults.CodeDatabaseWrite, queries.InsertManifest(ctx, dbquery.InsertManifestParams{Digest: m.Digest, ProjectID: m.ProjectID, Version: int64(m.Version), Kind: m.Kind, Canonical: m.Canonical, RecordedAt: ts}))
}

func verifyStoredManifest(ctx context.Context, q sqlQueryer, m parsedManifest) error {
	existing, err := dbquery.New(q).ProjectManifest(ctx, m.Digest)
	if err != nil {
		return classifyDiagnostic(faults.CodeDatabaseRead, err)
	}
	if existing.ProjectID != m.ProjectID || existing.Version != int64(m.Version) || existing.Kind != m.Kind || !bytes.Equal(existing.Canonical, m.Canonical) {
		return faults.New(faults.CodeInvariantFailed, fmt.Sprintf("stored manifest %s does not match submission envelope", m.Digest))
	}
	return nil
}

func nullStringEqual(a sql.NullString, b *string) bool {
	if !a.Valid && b == nil {
		return true
	}
	if a.Valid && b != nil && a.String == *b {
		return true
	}
	return false
}

// SubmitProject accepts the exact retained envelope, creating a job, owned lane,
// and assignment mail atomically after receipt-first recovery checks.
func (q *Queue) SubmitProject(rawEnvelope []byte) (_ ProjectSubmissionResult, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeRuntimeFailed, resultErr) }()
	s := q.store
	var empty ProjectSubmissionResult
	parsed, err := parseSubmissionEnvelope(rawEnvelope)
	if err != nil {
		return empty, faults.Wrap(faults.CodeValidationFailed, "invalid project submission", err)
	}

	// B1 resolution: check global receipts / project_submissions key FIRST before any live repo/session/binding checks.
	if existing, found, err := s.lookupProjectSubmission(s.db, parsed); err != nil {
		return empty, err
	} else if found {
		return existing, nil
	}

	ctx := context.Background()
	if verifyErr := s.verifyProjectSubmissionLive(ctx, parsed); verifyErr != nil {
		// If external validation fails, recheck the key before returning failure so an
		// exact concurrent acceptance can still be recovered receipt-first.
		if existing, found, err := s.lookupProjectSubmission(s.db, parsed); err == nil && found {
			return existing, nil
		}
		return empty, verifyErr
	}

	err = s.write(func(c *sql.Conn) error {
		if _, found, err := s.lookupProjectSubmissionRow(ctx, c, parsed); err != nil {
			return err
		} else if found {
			return nil
		}

		currentRev, err := currentBindingRevision(ctx, c, parsed.Envelope.ProjectID)
		if err != nil {
			return err
		}
		if currentRev == 0 {
			return faults.New(faults.CodeNotFound, fmt.Sprintf("project %s is not registered", parsed.Envelope.ProjectID))
		}
		if currentRev != parsed.Envelope.BindingRevision {
			return faults.New(faults.CodeValidationFailed, fmt.Sprintf("project %s binding revision CAS mismatch: current=%d envelope=%d", parsed.Envelope.ProjectID, currentRev, parsed.Envelope.BindingRevision))
		}

		if parsed.Envelope.Role == "review" {
			if err := verifyReviewTarget(ctx, c, parsed); err != nil {
				return err
			}
		}

		ts := now()
		if err := insertManifestConn(ctx, c, parsed.BriefManifest, ts); err != nil {
			return err
		}
		for _, ref := range parsed.ReferencedManifests {
			if err := insertManifestConn(ctx, c, ref, ts); err != nil {
				return err
			}
		}

		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return classifyDiagnostic(faults.CodeRuntimeFailed, err)
		}
		jobID := "job-" + hex.EncodeToString(random[:])
		queries := dbquery.New(c)
		if err := queries.InsertReceipt(ctx, dbquery.InsertReceiptParams{
			Key: parsed.Envelope.Key, JobID: jobID, Canonical: parsed.RequestBytes,
			Digest: parsed.RequestDigest, AcceptedAt: ts,
		}); err != nil {
			return err
		}
		if err := queries.InsertQueuedJob(ctx, jobID); err != nil {
			return err
		}
		if err := insertAgentOwnership(c, jobID); err != nil {
			return err
		}
		if err := queries.InsertSubmission(ctx, dbquery.InsertSubmissionParams{Key: parsed.Envelope.Key, EnvelopeDigest: parsed.EnvelopeDigest, EnvelopeCanonical: parsed.EnvelopeBytes, JobID: jobID, RecordedAt: ts}); err != nil {
			return err
		}
		if err := queries.InsertProjectJob(ctx, dbquery.InsertProjectJobParams{JobID: jobID, ProjectID: parsed.Envelope.ProjectID, BindingRevision: int64(parsed.Envelope.BindingRevision), Role: parsed.Envelope.Role, BriefDigest: parsed.BriefManifest.Digest, TargetJobID: sqlStringPtr(parsed.Envelope.TargetJobID), TargetResultDigest: sqlStringPtr(parsed.Envelope.TargetResultDigest), RecordedAt: ts}); err != nil {
			return err
		}
		existingLaneOwner, err := queries.LaneOwner(ctx, dbquery.LaneOwnerParams{ProjectID: parsed.Envelope.ProjectID, LaneID: parsed.LaneID})
		if err == nil {
			return faults.New(faults.CodeValidationFailed, fmt.Sprintf("project lane %s is already owned by job %s", parsed.LaneID, existingLaneOwner))
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := queries.InsertLane(ctx, dbquery.InsertLaneParams{LaneID: parsed.LaneID, ProjectID: parsed.Envelope.ProjectID, OwnerJobID: jobID, Role: parsed.Envelope.Role, BaseCommit: parsed.Request.Base, FsforkDriver: parsed.FsforkDriver, FsforkPath: "", CreatedAt: ts}); err != nil {
			return err
		}
		elephantSessionID, err := queries.BindingSession(ctx, dbquery.BindingSessionParams{ProjectID: parsed.Envelope.ProjectID, Revision: int64(parsed.Envelope.BindingRevision)})
		if err != nil {
			return faults.Wrap(faults.CodeDatabaseRead, "project integrator identity unavailable", err)
		}
		briefDigest := parsed.BriefManifest.Digest
		assignedSubject := fmt.Sprintf("Assigned %s work to %s", parsed.Envelope.Role, parsed.LaneID)
		assignedBody := fmt.Sprintf("Lane %s owns job %s for role %s. Brief %s.", parsed.LaneID, jobID, parsed.Envelope.Role, briefDigest[:12])
		if _, err := insertInboxMessageConn(ctx, c, ProjectInboxMessage{
			Key:            "lane-assigned:" + jobID,
			ProjectID:      parsed.Envelope.ProjectID,
			Sender:         "integrator:" + elephantSessionID,
			Recipient:      "lane:" + parsed.LaneID,
			LaneID:         parsed.LaneID,
			JobID:          jobID,
			Kind:           "lane.assigned",
			Subject:        assignedSubject,
			Body:           assignedBody,
			ManifestDigest: &briefDigest,
			CreatedAt:      ts,
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return empty, classifyDiagnostic(faults.CodeDatabaseWrite, err)
	}

	res, found, err := s.lookupProjectSubmission(s.db, parsed)
	if err != nil {
		return empty, err
	}
	if !found {
		return empty, faults.New(faults.CodeInvariantFailed, "project submission missing after commit")
	}
	return res, nil
}

type submissionRowMetadata struct {
	jobID              string
	recordedAt         string
	projectID          string
	bindingRevision    int
	role               string
	briefDigest        string
	targetJobID        *string
	targetResultDigest *string
	envelopeDigest     string
}

func (s *store) lookupProjectSubmissionRow(ctx context.Context, q sqlQueryer, parsed parsedProjectSubmission) (_ submissionRowMetadata, _ bool, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeDatabaseRead, resultErr) }()
	var meta submissionRowMetadata
	queries := dbquery.New(q)
	receipt, err := queries.ReceiptDetailsByKey(ctx, parsed.Envelope.Key)
	if errors.Is(err, sql.ErrNoRows) {
		return meta, false, nil
	}
	if err != nil {
		return meta, false, err
	}

	submission, err := queries.SubmissionByKey(ctx, parsed.Envelope.Key)
	if errors.Is(err, sql.ErrNoRows) {
		return meta, false, faults.New(faults.CodeValidationFailed, "idempotency key belongs to an unattributed legacy job; project adoption refused")
	}
	if err != nil {
		return meta, false, err
	}

	if submission.JobID != receipt.JobID ||
		receipt.Digest != parsed.RequestDigest ||
		!bytes.Equal(receipt.Canonical, parsed.RequestBytes) ||
		submission.EnvelopeDigest != parsed.EnvelopeDigest ||
		!bytes.Equal(submission.EnvelopeCanonical, parsed.EnvelopeBytes) ||
		submission.ProjectID != parsed.Envelope.ProjectID ||
		submission.BindingRevision != int64(parsed.Envelope.BindingRevision) ||
		submission.Role != parsed.Envelope.Role ||
		submission.BriefDigest != parsed.BriefManifest.Digest ||
		!nullStringEqual(submission.TargetJobID, parsed.Envelope.TargetJobID) ||
		!nullStringEqual(submission.TargetResultDigest, parsed.Envelope.TargetResultDigest) {
		return meta, false, faults.New(faults.CodeValidationFailed, "idempotency key already belongs to a different project submission envelope or canonical request")
	}

	if err := verifyStoredManifest(ctx, q, parsed.BriefManifest); err != nil {
		return meta, false, err
	}
	for _, ref := range parsed.ReferencedManifests {
		if err := verifyStoredManifest(ctx, q, ref); err != nil {
			return meta, false, err
		}
	}

	meta = submissionRowMetadata{
		jobID:              receipt.JobID,
		recordedAt:         submission.RecordedAt,
		projectID:          submission.ProjectID,
		bindingRevision:    int(submission.BindingRevision),
		role:               submission.Role,
		briefDigest:        submission.BriefDigest,
		targetJobID:        parsed.Envelope.TargetJobID,
		targetResultDigest: parsed.Envelope.TargetResultDigest,
		envelopeDigest:     submission.EnvelopeDigest,
	}
	return meta, true, nil
}

func (s *store) lookupProjectSubmission(q sqlQueryer, parsed parsedProjectSubmission) (ProjectSubmissionResult, bool, error) {
	if s.existingSchemaOnly && q == s.db {
		var result ProjectSubmissionResult
		var found bool
		err := s.readSchema5(func(tx sqlQueryer) error {
			var err error
			result, found, err = s.lookupProjectSubmission(tx, parsed)
			return err
		})
		return result, found, err
	}
	var out ProjectSubmissionResult
	meta, found, err := s.lookupProjectSubmissionRow(context.Background(), q, parsed)
	if err != nil || !found {
		return out, false, err
	}
	job, err := s.getFrom(q, meta.jobID)
	if err != nil {
		return out, false, err
	}
	return ProjectSubmissionResult{
		Job:                job,
		JobID:              job.ID,
		ProjectID:          meta.projectID,
		BindingRevision:    meta.bindingRevision,
		Role:               meta.role,
		BriefDigest:        meta.briefDigest,
		TargetJobID:        meta.targetJobID,
		TargetResultDigest: meta.targetResultDigest,
		EnvelopeDigest:     meta.envelopeDigest,
		RecordedAt:         meta.recordedAt,
	}, true, nil
}

func (s *store) currentProjectBinding(ctx context.Context, q sqlQueryer, projectID string) (ProjectBinding, error) {
	var b ProjectBinding
	row, err := dbquery.New(q).LatestBinding(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return b, faults.Wrap(faults.CodeNotFound, fmt.Sprintf("project %s is not registered", projectID), err)
	}
	if err != nil {
		return b, classifyDiagnostic(faults.CodeDatabaseRead, err)
	}
	b = ProjectBinding{ProjectID: row.ProjectID, Revision: int(row.Revision), RepoRoot: row.RepoRoot, GitCommonDir: row.GitCommonDir, CommitAnchor: row.CommitAnchor, ElephantSessionID: row.ElephantSessionID, OpenCodeProjectID: row.OpencodeProjectID, OpenCodeDirectory: row.OpencodeDirectory, HandoffManifestDigest: row.HandoffManifestDigest, AuthRef: row.AuthRef, VerifiedAt: row.VerifiedAt, RecordedAt: row.RecordedAt}
	return b, nil
}

func verifyReviewTarget(ctx context.Context, q sqlQueryer, parsed parsedProjectSubmission) error {
	targetJobID := *parsed.Envelope.TargetJobID
	targetResultDigest := *parsed.Envelope.TargetResultDigest

	queries := dbquery.New(q)
	target, err := queries.ProjectJobReceipt(ctx, targetJobID)
	if errors.Is(err, sql.ErrNoRows) {
		return faults.Wrap(faults.CodeNotFound, fmt.Sprintf("review target_job_id %s is not a project job", targetJobID), err)
	}
	if err != nil {
		return err
	}
	if target.ProjectID != parsed.Envelope.ProjectID || target.Role != "implementation" {
		return faults.New(faults.CodeValidationFailed, fmt.Sprintf("review target_job_id %s must belong to project %s with role implementation (got project=%s role=%s)", targetJobID, parsed.Envelope.ProjectID, target.ProjectID, target.Role))
	}
	_, targetAgentReq, err := storedRequest(target.Canonical)
	if err != nil || targetAgentReq == nil {
		return faults.New(faults.CodeInvariantFailed, fmt.Sprintf("review target_job_id %s is not a valid agent v2 job", targetJobID))
	}
	if targetAgentReq.Base != parsed.Request.Base {
		return faults.New(faults.CodeValidationFailed, fmt.Sprintf("review base %s does not match implementation target job base %s", parsed.Request.Base, targetAgentReq.Base))
	}

	_, err = queries.ResultAssociationID(ctx, dbquery.ResultAssociationIDParams{ProjectID: parsed.Envelope.ProjectID, JobID: targetJobID, ManifestDigest: targetResultDigest})
	if errors.Is(err, sql.ErrNoRows) {
		return faults.Wrap(faults.CodeNotFound, fmt.Sprintf("review target_result_digest %s is not a recorded result association for job %s in project %s", targetResultDigest, targetJobID, parsed.Envelope.ProjectID), err)
	}
	if err != nil {
		return err
	}

	resultBytes, err := queries.ManifestBytesByKind(ctx, dbquery.ManifestBytesByKindParams{Digest: targetResultDigest, ProjectID: parsed.Envelope.ProjectID, Kind: "result"})
	if err != nil {
		return faults.Wrap(faults.CodeDatabaseRead, fmt.Sprintf("recorded result manifest %s unavailable", targetResultDigest), err)
	}
	pm, err := parseManifestBytes(resultBytes, "result", parsed.Envelope.ProjectID)
	if err != nil {
		return faults.Wrap(faults.CodeInvariantFailed, fmt.Sprintf("recorded result manifest %s invalid", targetResultDigest), err)
	}
	if pm.Result.JobID != targetJobID || pm.Result.RequestDigest != target.Digest || pm.Result.Base != parsed.Request.Base {
		return faults.New(faults.CodeInvariantFailed, "recorded result manifest job_id/request_digest/base linkage mismatch")
	}
	rb := parsed.BriefManifest.ReviewBrief
	if pm.Result.Diff != rb.Diff || !reflect.DeepEqual(pm.Result.ChangedFiles, rb.ChangedFiles) {
		return faults.New(faults.CodeValidationFailed, "review_brief diff or changed_files do not match recorded implementation result manifest")
	}
	return nil
}

func (s *store) verifyProjectSubmissionLive(ctx context.Context, parsed parsedProjectSubmission) error {
	binding, err := s.currentProjectBinding(ctx, s.db, parsed.Envelope.ProjectID)
	if err != nil {
		return err
	}
	if binding.Revision != parsed.Envelope.BindingRevision {
		return faults.New(faults.CodeValidationFailed, fmt.Sprintf("project %s binding revision mismatch: current=%d envelope=%d", parsed.Envelope.ProjectID, binding.Revision, parsed.Envelope.BindingRevision))
	}
	if parsed.Request.Repo != binding.RepoRoot {
		return faults.New(faults.CodeValidationFailed, fmt.Sprintf("agent request repo %q does not match bound project repo_root %q", parsed.Request.Repo, binding.RepoRoot))
	}
	repoRoot, gitCommonDir, err := canonicalizeGitRepo(binding.RepoRoot, parsed.Request.Base)
	if err != nil {
		return err
	}
	if repoRoot != binding.RepoRoot || gitCommonDir != binding.GitCommonDir {
		return faults.New(faults.CodeInvariantFailed, "live repository root or git common directory no longer matches project binding")
	}
	if err := verifyElephantSession(ctx, binding.OpenCodeDirectory, binding.OpenCodeProjectID, binding.ElephantSessionID); err != nil {
		return err
	}
	if parsed.Envelope.Role == "review" {
		if err := verifyReviewTarget(ctx, s.db, parsed); err != nil {
			return err
		}
		if err := verifyReviewerRuntime(binding.RepoRoot, parsed.Request.Agent, parsed.Request.Permissions); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) isProjectReviewJob(ctx context.Context, jobID string) (bool, error) {
	role, err := dbquery.New(s.db).ProjectJobRole(ctx, jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return role == "review", nil
}

func (s *store) verifyProjectSessionPolicy(ctx context.Context, j Job, info sessionInfo) error {
	policy, err := dbquery.New(s.db).ProjectJobPolicy(ctx, j.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	projectID, bindingRev, role, briefDigest := policy.ProjectID, int(policy.BindingRevision), policy.Role, policy.BriefDigest
	targetJobID, targetResultDigest := policy.TargetJobID, policy.TargetResultDigest
	if j.Agent == nil || j.agentRequest == nil {
		return diagnostic(faults.CodeInvariantFailed, "project job missing agent ownership or canonical agent request")
	}
	if info.ParentID != "" {
		return diagnostic(faults.CodeRuntimeFailed, "project worker session must be unparented")
	}
	if !reflect.DeepEqual(info.Permissions, j.agentRequest.Permissions) || !jsonEqual(info.Permissions, j.agentRequest.Permissions) {
		return diagnostic(faults.CodeRuntimeFailed, "project worker session ordered permission rules do not match canonical request")
	}

	elephantSessionID, err := dbquery.New(s.db).BindingSession(ctx, dbquery.BindingSessionParams{ProjectID: projectID, Revision: int64(bindingRev)})
	if err != nil {
		return diagnosticWrap(faults.CodeDatabaseRead, fmt.Sprintf("project binding %s@%d unavailable", projectID, bindingRev), err)
	}
	if j.Agent.SessionID == elephantSessionID || info.ID == elephantSessionID {
		return diagnostic(faults.CodeRuntimeFailed, "project worker session must be distinct from bound Elephant session")
	}
	if curBinding, err := s.currentProjectBinding(ctx, s.db, projectID); err == nil {
		if j.Agent.SessionID == curBinding.ElephantSessionID || info.ID == curBinding.ElephantSessionID {
			return diagnostic(faults.CodeRuntimeFailed, "project worker session must be distinct from current Elephant session")
		}
	}

	if role != "review" {
		return nil
	}
	if err := validateReviewerPermissions(j.agentRequest.Permissions); err != nil {
		return err
	}
	if err := validateReviewerPermissions(info.Permissions); err != nil {
		return err
	}
	if !targetJobID.Valid || !isValidJobID(targetJobID.String) || !targetResultDigest.Valid || !isValidHexLen(targetResultDigest.String, 64) {
		return diagnostic(faults.CodeInvariantFailed, "review job missing valid target_job_id or target_result_digest")
	}
	if j.ID == targetJobID.String {
		return diagnostic(faults.CodeRuntimeFailed, "reviewer job_id must be distinct from target implementation job_id")
	}
	targetLocation, err := dbquery.New(s.db).AgentLocation(ctx, targetJobID.String)
	if err != nil {
		return diagnosticWrap(faults.CodeDatabaseRead, fmt.Sprintf("review target implementation job %s ownership unavailable", targetJobID.String), err)
	}
	if j.Agent.SessionID == targetLocation.SessionID || info.ID == targetLocation.SessionID {
		return diagnostic(faults.CodeRuntimeFailed, "reviewer session_id must be distinct from target implementation session_id")
	}
	if j.Agent.Worktree != "" && targetLocation.Worktree != "" && j.Agent.Worktree == targetLocation.Worktree {
		return diagnostic(faults.CodeRuntimeFailed, "reviewer worktree must be distinct from target implementation worktree")
	}
	briefBytes, err := dbquery.New(s.db).ManifestBytesByKind(ctx, dbquery.ManifestBytesByKindParams{Digest: briefDigest, ProjectID: projectID, Kind: "review_brief"})
	if err != nil {
		return diagnosticWrap(faults.CodeDatabaseRead, fmt.Sprintf("review_brief manifest %s unavailable", briefDigest), err)
	}
	pm, err := parseManifestBytes(briefBytes, "review_brief", projectID)
	if err != nil {
		return err
	}
	expectedPrompt, err := buildReviewPrompt(*pm.ReviewBrief)
	if err != nil {
		return err
	}
	if j.agentRequest.Prompt != expectedPrompt {
		return diagnostic(faults.CodeInvariantFailed, "review job prompt does not match deterministic inline review prompt")
	}
	return nil
}

func (s *store) verifyProjectLaunchGate(ctx context.Context, j Job, info sessionInfo) error {
	if err := s.verifyProjectSessionPolicy(ctx, j, info); err != nil {
		return err
	}
	isReview, err := s.isProjectReviewJob(ctx, j.ID)
	if err != nil || !isReview {
		return err
	}
	if err := verifyReviewerRuntime(j.Agent.Worktree, j.agentRequest.Agent, info.Permissions); err != nil {
		return err
	}
	msgs, err := getMessages(ctx, j.Agent.Worktree, j.Agent.SessionID)
	if err != nil {
		return err
	}
	if len(msgs) != 0 {
		return diagnostic(faults.CodeRuntimeFailed, "fresh review session already has prior messages before sole review prompt")
	}
	var perms, forms []json.RawMessage
	if err := getSessionList(ctx, j.Agent.Worktree, j.Agent.SessionID, "permission", &perms); err != nil {
		return err
	}
	if err := getSessionList(ctx, j.Agent.Worktree, j.Agent.SessionID, "form", &forms); err != nil {
		return err
	}
	var inbox []inboxItem
	if err := getSessionList(ctx, j.Agent.Worktree, j.Agent.SessionID, "inbox", &inbox); err != nil {
		return err
	}
	if len(perms) != 0 || len(forms) != 0 || len(inbox) != 0 {
		return diagnostic(faults.CodeRuntimeFailed, "fresh review session has pending permission, form, or inbox requests before sole review prompt")
	}
	return nil
}
