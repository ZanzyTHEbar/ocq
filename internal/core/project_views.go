package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ZanzyTHEbar/faults-go"

	"ocq/internal/core/dbquery"
)

// RecordResult atomically associates an imported manifest and delivers mail.
func (q *Queue) RecordResult(projectID, jobID string, rawBytes []byte, supersedesID string) (_ ProjectResultAssociation, resultErr error) {
	return q.store.recordResult(projectID, jobID, rawBytes, supersedesID, nil, false)
}

// Validation and publication share the same checks. Preview uses a schema-5
// read snapshot and exits before every write; publication rechecks under the
// real write transaction, including any adapter ownership fence.
func (s *store) recordResult(projectID, jobID string, rawBytes []byte, supersedesID string, check func(sqlQueryer) error, preview bool) (_ ProjectResultAssociation, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeDatabaseWrite, resultErr) }()
	var committed ProjectResultAssociation
	if !isValidProjectID(projectID) || !isValidJobID(jobID) {
		return committed, faults.New(faults.CodeValidationFailed, "project record-result requires --project PROJECT --job JOB --manifest-file FILE [--supersedes ASSOC_ID]")
	}
	if supersedesID != "" && !isValidAssociationID(supersedesID) {
		return committed, faults.New(faults.CodeValidationFailed, fmt.Sprintf("invalid --supersedes association ID %q", supersedesID))
	}

	ctx := context.Background()
	run := func(c sqlQueryer) error {
		if check != nil {
			if err := check(c); err != nil {
				return err
			}
		}
		queries := dbquery.New(c)
		job, err := queries.ProjectJobReceiptTargets(ctx, jobID)
		if errors.Is(err, sql.ErrNoRows) {
			return faults.Wrap(faults.CodeNotFound, fmt.Sprintf("job %s is not attributed to project %s", jobID, projectID), err)
		}
		if err != nil {
			return classifyDiagnostic(faults.CodeDatabaseRead, err)
		}
		pjProjectID, pjRole, pjTargetJobID, pjTargetResultDigest, receiptCanonical, receiptDigest := job.ProjectID, job.Role, job.TargetJobID, job.TargetResultDigest, job.Canonical, job.Digest
		if pjProjectID != projectID {
			return faults.New(faults.CodeValidationFailed, fmt.Sprintf("job %s belongs to project %s, not %s", jobID, pjProjectID, projectID))
		}

		expectedKind := "result"
		if pjRole == "review" {
			expectedKind = "review_result"
		}
		m, err := parseManifestBytes(rawBytes, expectedKind, projectID)
		if err != nil {
			return faults.Wrap(faults.CodeValidationFailed, "invalid result manifest", err)
		}
		if m.Source == "machine_capture" {
			return faults.New(faults.CodeValidationFailed, "project record-result cannot import source \"machine_capture\"; use project capture-result")
		}

		_, agentReq, err := storedRequest(receiptCanonical)
		if err != nil || agentReq == nil {
			return faults.New(faults.CodeInvariantFailed, fmt.Sprintf("job %s does not have a valid canonical v2 agent request", jobID))
		}

		location, err := dbquery.New(c).AgentLocation(ctx, jobID)
		if err != nil {
			return faults.Wrap(faults.CodeDatabaseRead, fmt.Sprintf("job %s agent ownership unavailable", jobID), err)
		}
		sessionID, worktree := location.SessionID, location.Worktree

		if expectedKind == "result" {
			if m.Result.JobID != jobID {
				return faults.New(faults.CodeValidationFailed, fmt.Sprintf("result manifest job_id %q does not match --job %q", m.Result.JobID, jobID))
			}
			if m.Result.RequestDigest != receiptDigest {
				return faults.New(faults.CodeValidationFailed, fmt.Sprintf("result manifest request_digest %s does not match stored receipt digest %s", m.Result.RequestDigest, receiptDigest))
			}
			if m.Result.Base != agentReq.Base {
				return faults.New(faults.CodeValidationFailed, fmt.Sprintf("result manifest base %s does not match stored agent request base %s", m.Result.Base, agentReq.Base))
			}
			if worktree != "" && m.Result.Worktree != worktree {
				return faults.New(faults.CodeValidationFailed, fmt.Sprintf("result manifest worktree %q does not match stored job worktree %q", m.Result.Worktree, worktree))
			}
		} else {
			if m.ReviewResult.ReviewJobID != jobID {
				return faults.New(faults.CodeValidationFailed, fmt.Sprintf("review_result manifest review_job_id %q does not match --job %q", m.ReviewResult.ReviewJobID, jobID))
			}
			if !pjTargetResultDigest.Valid || m.ReviewResult.ReviewedResultDigest != pjTargetResultDigest.String {
				return faults.New(faults.CodeValidationFailed, fmt.Sprintf("review_result manifest reviewed_result_digest %s does not match stored target_result_digest %s", m.ReviewResult.ReviewedResultDigest, pjTargetResultDigest.String))
			}
			if m.ReviewResult.TargetJobID != "" {
				if !pjTargetJobID.Valid || m.ReviewResult.TargetJobID != pjTargetJobID.String {
					return faults.New(faults.CodeValidationFailed, fmt.Sprintf("review_result manifest target_job_id %q does not match stored target_job_id %q", m.ReviewResult.TargetJobID, pjTargetJobID.String))
				}
			}
			if sessionID != "" && m.ReviewResult.ReviewSessionID != sessionID {
				return faults.New(faults.CodeValidationFailed, fmt.Sprintf("review_result manifest review_session_id %q does not match stored job session_id %q", m.ReviewResult.ReviewSessionID, sessionID))
			}
		}

		var supersedesPtr *string
		if supersedesID != "" {
			supersedesPtr = &supersedesID
		}
		assocID := deterministicAssociationID(projectID, jobID, m.Kind, m.Digest, supersedesID)
		if supersedesID != "" {
			if supersedesID == assocID {
				return faults.New(faults.CodeValidationFailed, "result association cannot supersede itself")
			}
			sup, err := dbquery.New(c).AssociationIdentity(ctx, supersedesID)
			if errors.Is(err, sql.ErrNoRows) {
				return faults.Wrap(faults.CodeNotFound, fmt.Sprintf("supersedes association %s does not exist", supersedesID), err)
			}
			if err != nil {
				return classifyDiagnostic(faults.CodeDatabaseRead, err)
			}
			if sup.ProjectID != projectID || sup.JobID != jobID || sup.Kind != m.Kind {
				return faults.New(faults.CodeValidationFailed, fmt.Sprintf("supersedes association %s must belong to same project %s, job %s, and kind %s", supersedesID, projectID, jobID, m.Kind))
			}
		}

		// Idempotent publication recovery (B2 rule):
		existing, err := queries.ExistingAssociation(ctx, dbquery.ExistingAssociationParams{ProjectID: projectID, JobID: jobID, Kind: m.Kind, ManifestDigest: m.Digest})
		if err == nil {
			if err := verifyStoredManifest(ctx, c, m); err != nil {
				return err
			}
			if !nullStringEqual(existing.SupersedesID, supersedesPtr) || existing.ID != assocID {
				return faults.New(faults.CodeValidationFailed, fmt.Sprintf("result association for project %s job %s kind %s manifest %s already exists with conflicting supersedes linkage", projectID, jobID, m.Kind, m.Digest))
			}
			committed = summarizeAssociation(existing.ID, existing.Sequence, projectID, jobID, m.Kind, m.Digest, supersedesPtr, existing.RecordedAt, &m, true)
			if preview {
				return nil
			}
			return sealLaneAndDeliverMail(ctx, c.(*sql.Conn), m, projectID, jobID, existing.ID, worktree, existing.RecordedAt)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return classifyDiagnostic(faults.CodeDatabaseRead, err)
		}
		if preview {
			committed = summarizeAssociation(assocID, 0, projectID, jobID, m.Kind, m.Digest, supersedesPtr, "", &m, true)
			return nil
		}

		ts := now()
		if err := insertManifestConn(ctx, c.(*sql.Conn), m, ts); err != nil {
			return err
		}
		seq, err := queries.InsertAssociation(ctx, dbquery.InsertAssociationParams{ID: assocID, ProjectID: projectID, JobID: jobID, Kind: m.Kind, ManifestDigest: m.Digest, SupersedesID: sqlStringPtr(supersedesPtr), RecordedAt: ts})
		if err != nil {
			return err
		}
		committed = summarizeAssociation(assocID, seq, projectID, jobID, m.Kind, m.Digest, supersedesPtr, ts, &m, true)
		return sealLaneAndDeliverMail(ctx, c.(*sql.Conn), m, projectID, jobID, assocID, worktree, ts)
	}
	var err error
	if preview {
		err = s.readSchema5(run)
	} else {
		err = s.write(func(c *sql.Conn) error { return run(c) })
	}
	if err != nil {
		return committed, err
	}
	return committed, nil
}

func summarizeAssociation(id string, seq int64, projectID, jobID, kind, manifestDigest string, supersedesID *string, recordedAt string, m *parsedManifest, available bool) ProjectResultAssociation {
	assoc := ProjectResultAssociation{
		ID:             id,
		AssociationID:  id,
		Sequence:       seq,
		ProjectID:      projectID,
		JobID:          jobID,
		Kind:           kind,
		ManifestDigest: manifestDigest,
		SupersedesID:   supersedesID,
		RecordedAt:     recordedAt,
		Availability:   "missing",
		Verification:   "structure_hash_linkage_only",
	}
	if !available || m == nil {
		return assoc
	}
	assoc.Availability = "available"
	assoc.Verification = effectiveVerification(m.Source)
	assoc.Source = m.Source
	assoc.ObservedAt = m.ObservedAt
	if kind == "result" && m.Result != nil {
		assoc.ClaimLabel = effectiveClaimLabel(m.Result.ClaimLabel, m.Source)
		assoc.ClaimedValidationStatus = m.Result.ClaimedValidation.Status
	} else if kind == "review_result" && m.ReviewResult != nil {
		assoc.ClaimLabel = effectiveClaimLabel(m.ReviewResult.ClaimLabel, m.Source)
		assoc.Verdict = m.ReviewResult.Verdict
	}
	return assoc
}

type projectJobRow struct {
	jobID              string
	projectID          string
	bindingRevision    int
	role               string
	briefDigest        string
	targetJobID        *string
	targetResultDigest *string
	recordedAt         string
}

func projectJobFromRow(row dbquery.ProjectJob) projectJobRow {
	return projectJobRow{jobID: row.JobID, projectID: row.ProjectID, bindingRevision: int(row.BindingRevision),
		role: row.Role, briefDigest: row.BriefDigest, targetJobID: nullableSQLString(row.TargetJobID),
		targetResultDigest: nullableSQLString(row.TargetResultDigest), recordedAt: row.RecordedAt}
}

func isTerminalState(state string) bool {
	return state == "finished" || state == "cancelled" || state == "resolved"
}

func getJobTx(ctx context.Context, tx sqlQueryer, id string) (Job, error) {
	queries := dbquery.New(tx)
	row, err := queries.JobByID(ctx, id)
	if err != nil {
		return Job{}, err
	}
	job, err := jobFromRow(row)
	if err == nil && job.State == "queued" {
		var head string
		head, err = queries.QueuedHead(ctx)
		if err == nil && head != id {
			job.Reason = "FIFO behind " + head
		} else if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
	}
	if err == nil && job.agentRequest != nil {
		var a dbquery.AgentOwnershipByJobRow
		a, err = queries.AgentOwnershipByJob(ctx, id)
		if err == nil {
			job.Agent = &AgentOwnership{SessionID: a.SessionID, PromptID: a.PromptID, Token: a.Token, Phase: a.Phase, Worktree: a.Worktree, ProjectID: a.ProjectID, ServerOutcome: a.ServerOutcome, TerminalEvidence: a.TerminalEvidence}
		}
	}
	return job, err
}

type rawAssocRow struct {
	id             string
	sequence       int64
	projectID      string
	jobID          string
	kind           string
	manifestDigest string
	supersedesID   *string
	recordedAt     string
}

func loadJobAssociationsTx(ctx context.Context, tx sqlQueryer, projectID, jobID string) ([]ProjectResultAssociation, []*parsedManifest, string, error) {
	queries := dbquery.New(tx)
	rows, err := queries.JobAssociations(ctx, dbquery.JobAssociationsParams{ProjectID: projectID, JobID: jobID})
	if err != nil {
		return nil, nil, "", err
	}
	var rawRows []rawAssocRow
	for _, row := range rows {
		rawRows = append(rawRows, rawAssocRow{id: row.ID, sequence: row.Sequence, projectID: row.ProjectID, jobID: row.JobID, kind: row.Kind, manifestDigest: row.ManifestDigest, supersedesID: nullableSQLString(row.SupersedesID), recordedAt: row.RecordedAt})
	}

	if len(rawRows) == 0 {
		return []ProjectResultAssociation{}, nil, "not_recorded", nil
	}

	assocs := make([]ProjectResultAssociation, 0, len(rawRows))
	manifests := make([]*parsedManifest, 0, len(rawRows))
	anyMissing := false
	for _, r := range rawRows {
		manifest, err := queries.ProjectManifest(ctx, r.manifestDigest)
		if err != nil || manifest.ProjectID != projectID || manifest.Version != 1 || manifest.Kind != r.kind || sha256Hex(manifest.Canonical) != r.manifestDigest {
			assocs = append(assocs, summarizeAssociation(r.id, r.sequence, r.projectID, r.jobID, r.kind, r.manifestDigest, r.supersedesID, r.recordedAt, nil, false))
			manifests = append(manifests, nil)
			anyMissing = true
			continue
		}
		pm, err := parseManifestBytes(manifest.Canonical, r.kind, projectID)
		if err != nil {
			assocs = append(assocs, summarizeAssociation(r.id, r.sequence, r.projectID, r.jobID, r.kind, r.manifestDigest, r.supersedesID, r.recordedAt, nil, false))
			manifests = append(manifests, nil)
			anyMissing = true
			continue
		}
		if (r.kind == "result" && (pm.Result == nil || pm.Result.JobID != jobID)) ||
			(r.kind == "review_result" && (pm.ReviewResult == nil || pm.ReviewResult.ReviewJobID != jobID)) {
			assocs = append(assocs, summarizeAssociation(r.id, r.sequence, r.projectID, r.jobID, r.kind, r.manifestDigest, r.supersedesID, r.recordedAt, nil, false))
			manifests = append(manifests, nil)
			anyMissing = true
			continue
		}
		pmCopy := pm
		assocs = append(assocs, summarizeAssociation(r.id, r.sequence, r.projectID, r.jobID, r.kind, r.manifestDigest, r.supersedesID, r.recordedAt, &pmCopy, true))
		manifests = append(manifests, &pmCopy)
	}
	status := "available"
	if anyMissing {
		status = "missing"
	}
	return assocs, manifests, status, nil
}

func computeJobDimensionsTx(ctx context.Context, tx sqlQueryer, pj projectJobRow, j Job, assocs []ProjectResultAssociation, manifests []*parsedManifest) (ProjectExecutionDimension, ProjectValidationDimension, ProjectReviewDimension, error) {
	execDim := ProjectExecutionDimension{
		State:     j.State,
		Outcome:   j.Outcome,
		Reason:    j.Reason,
		Freshness: "stored_db_snapshot",
	}
	if j.Agent != nil {
		execDim.Phase = j.Agent.Phase
		execDim.ServerOutcome = j.Agent.ServerOutcome
	}

	valDim := ProjectValidationDimension{
		Status:       j.Validation,
		Verified:     false,
		Verification: "not_run",
	}
	for i := len(assocs) - 1; i >= 0; i-- {
		if assocs[i].Kind == "result" && assocs[i].Availability == "available" && manifests[i] != nil && manifests[i].Result != nil {
			res := manifests[i].Result
			valDim.Verification = effectiveVerification(manifests[i].Source)
			assertionLabel := "unverified_assertion"
			if manifests[i].Source == "machine_capture" {
				assertionLabel = "machine_verified_snapshot"
			}
			valDim.ClaimedValidation = &ProjectClaimedValidationSummary{
				AssociationID:  assocs[i].ID,
				ManifestDigest: assocs[i].ManifestDigest,
				Source:         manifests[i].Source,
				ClaimLabel:     effectiveClaimLabel(res.ClaimLabel, manifests[i].Source),
				Assertion:      assertionLabel,
				Verified:       false,
				Status:         res.ClaimedValidation.Status,
				Commands:       res.ClaimedValidation.Commands,
				Results:        res.ClaimedValidation.Results,
				Provenance:     res.ClaimedValidation.Provenance,
				Limitations:    res.ClaimedValidation.Limitations,
			}
			break
		}
	}

	revDim := ProjectReviewDimension{
		Status:               "not_reviewed",
		Verdict:              "not_reviewed",
		StaleForLatestResult: false,
	}

	if pj.role == "implementation" {
		latestResultDigest := ""
		var latestResultSeq int64
		resultMaxSeqByDigest := map[string]int64{}
		for _, a := range assocs {
			if a.Kind == "result" {
				latestResultDigest = a.ManifestDigest
				latestResultSeq = a.Sequence
				resultMaxSeqByDigest[a.ManifestDigest] = a.Sequence
			}
		}
		revDim.LatestResultDigest = latestResultDigest

		rows, err := dbquery.New(tx).ReviewAssociations(ctx, dbquery.ReviewAssociationsParams{ProjectID: pj.projectID, TargetJobID: sql.NullString{String: pj.jobID, Valid: true}})
		if err != nil {
			return execDim, valDim, revDim, err
		}
		type revAssocRef struct {
			id             string
			sequence       int64
			reviewJobID    string
			manifestDigest string
		}
		var revRefs []revAssocRef
		for _, row := range rows {
			revRefs = append(revRefs, revAssocRef{id: row.ID, sequence: row.Sequence, reviewJobID: row.JobID, manifestDigest: row.ManifestDigest})
		}

		if len(revRefs) > 0 {
			lastRev := revRefs[len(revRefs)-1]
			revDim.ReviewJobID = lastRev.reviewJobID
			revDim.ReviewAssociationID = lastRev.id
			revDim.ReviewManifestDigest = lastRev.manifestDigest

			canonical, err := dbquery.New(tx).ManifestBytesByKind(ctx, dbquery.ManifestBytesByKindParams{Digest: lastRev.manifestDigest, ProjectID: pj.projectID, Kind: "review_result"})
			if err != nil || sha256Hex(canonical) != lastRev.manifestDigest {
				revDim.Status = "missing"
				revDim.Verdict = "missing"
			} else if pm, err := parseManifestBytes(canonical, "review_result", pj.projectID); err != nil || pm.ReviewResult == nil {
				revDim.Status = "missing"
				revDim.Verdict = "missing"
			} else {
				revDim.Status = pm.ReviewResult.Verdict
				revDim.Verdict = pm.ReviewResult.Verdict
				revDim.ReviewSessionID = pm.ReviewResult.ReviewSessionID
				revDim.ReviewedResultDigest = pm.ReviewResult.ReviewedResultDigest
				if latestResultDigest != "" {
					revSeq, ok := resultMaxSeqByDigest[pm.ReviewResult.ReviewedResultDigest]
					if !ok || latestResultDigest != pm.ReviewResult.ReviewedResultDigest || latestResultSeq > revSeq {
						revDim.StaleForLatestResult = true
					}
				}
			}
		}
	} else {
		revDim.ReviewJobID = pj.jobID
		if pj.targetResultDigest != nil {
			revDim.ReviewedResultDigest = *pj.targetResultDigest
		}
		latestTargetResultDigest := ""
		var latestTargetResultSeq int64
		targetResultMaxSeqByDigest := map[string]int64{}
		if pj.targetJobID != nil {
			targetAssocs, _, _, err := loadJobAssociationsTx(ctx, tx, pj.projectID, *pj.targetJobID)
			if err != nil {
				return execDim, valDim, revDim, err
			}
			for _, a := range targetAssocs {
				if a.Kind == "result" {
					latestTargetResultDigest = a.ManifestDigest
					latestTargetResultSeq = a.Sequence
					targetResultMaxSeqByDigest[a.ManifestDigest] = a.Sequence
				}
			}
		}
		revDim.LatestResultDigest = latestTargetResultDigest

		if len(assocs) > 0 {
			lastAssoc := assocs[len(assocs)-1]
			lastManifest := manifests[len(manifests)-1]
			revDim.ReviewAssociationID = lastAssoc.ID
			revDim.ReviewManifestDigest = lastAssoc.ManifestDigest
			if lastAssoc.Availability != "available" || lastManifest == nil || lastManifest.ReviewResult == nil {
				revDim.Status = "missing"
				revDim.Verdict = "missing"
			} else {
				revDim.Status = lastManifest.ReviewResult.Verdict
				revDim.Verdict = lastManifest.ReviewResult.Verdict
				revDim.ReviewSessionID = lastManifest.ReviewResult.ReviewSessionID
				revDim.ReviewedResultDigest = lastManifest.ReviewResult.ReviewedResultDigest
			}
		}
		if latestTargetResultDigest != "" && revDim.ReviewedResultDigest != "" {
			revSeq, ok := targetResultMaxSeqByDigest[revDim.ReviewedResultDigest]
			if !ok || latestTargetResultDigest != revDim.ReviewedResultDigest || latestTargetResultSeq > revSeq {
				revDim.StaleForLatestResult = true
			}
		}
	}

	return execDim, valDim, revDim, nil
}

func pendingResultJobIDsTx(ctx context.Context, tx sqlQueryer, projectID string) ([]string, error) {
	return dbquery.New(tx).PendingResultJobs(ctx, projectID)
}
