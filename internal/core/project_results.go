package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ZanzyTHEbar/faults-go"

	"ocq/internal/core/dbquery"
)

// projectResultsData returns stored terminal snapshots, including acknowledged
// events. Reading it neither acknowledges events nor changes capacity.
func (s *store) projectResultsData(projectID string, afterSeq int64, limit int) (_ ProjectResultsOutput, resultErr error) {
	return s.projectResultsPage(projectID, afterSeq, limit, false)
}

func (s *store) projectResultsPage(projectID string, afterSeq int64, limit int, recent bool) (_ ProjectResultsOutput, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeDatabaseRead, resultErr) }()
	var out ProjectResultsOutput
	if !isValidProjectID(projectID) {
		return out, faults.New(faults.CodeValidationFailed, "project results requires a valid PROJECT ID (proj-...)")
	}
	if afterSeq < 0 || limit < 1 || limit > 100 {
		return out, faults.New(faults.CodeValidationFailed, "invalid after_sequence or limit")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()

	_, err = dbquery.New(tx).ProjectByID(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return out, faults.Wrap(faults.CodeNotFound, fmt.Sprintf("project %s does not exist", projectID), err)
	}
	if err != nil {
		return out, err
	}
	if recent {
		afterSeq, err = dbquery.New(tx).ProjectResultsRecentAnchor(ctx, dbquery.ProjectResultsRecentAnchorParams{ProjectID: projectID, PageLimit: int64(limit)})
		if err != nil {
			return out, err
		}
	}
	rows, err := dbquery.New(tx).ProjectResultEvents(ctx, dbquery.ProjectResultEventsParams{ProjectID: projectID, Sequence: afterSeq, Limit: int64(limit)})
	if err != nil {
		return out, err
	}
	type notifProjectRow struct {
		event           Notification
		projectID       string
		role            string
		bindingRevision int
	}
	var rawItems []notifProjectRow
	for _, row := range rows {
		event := notificationFromRow(dbquery.NotificationByIDRow{ID: row.ID, Sequence: row.Sequence, JobID: row.JobID,
			State: row.State, Outcome: row.Outcome, ExitCode: row.ExitCode, Validation: row.Validation,
			Origin: row.Origin, RecordedAt: row.RecordedAt, AcknowledgedAt: row.AcknowledgedAt})
		rawItems = append(rawItems, notifProjectRow{event: event, projectID: row.ProjectID, role: row.Role, bindingRevision: int(row.BindingRevision)})
	}

	nextSeq := afterSeq
	items := make([]ProjectResultItem, 0, len(rawItems))
	for _, r := range rawItems {
		j, err := getJobTx(ctx, tx, r.event.JobID)
		if err != nil {
			return out, err
		}
		assocs, _, resultStatus, err := loadJobAssociationsTx(ctx, tx, projectID, r.event.JobID)
		if err != nil {
			return out, err
		}
		var latestAssocID, latestManifestDigest *string
		if len(assocs) > 0 {
			last := assocs[len(assocs)-1]
			aid := last.ID
			md := last.ManifestDigest
			latestAssocID = &aid
			latestManifestDigest = &md
		}
		items = append(items, ProjectResultItem{
			ProjectID:       r.projectID,
			JobID:           r.event.JobID,
			Role:            r.role,
			BindingRevision: r.bindingRevision,
			Event:           r.event,
			CurrentJob: ProjectCurrentJobSummary{
				ID:           j.ID,
				JobID:        j.ID,
				State:        j.State,
				Outcome:      j.Outcome,
				Reason:       j.Reason,
				Validation:   j.Validation,
				Evidence:     j.Evidence,
				OutputStatus: j.OutputStatus,
				Agent:        j.Agent,
				Freshness:    "stored_db_snapshot",
			},
			ResultSummary: ProjectResultSummary{
				Status:               resultStatus,
				AssociationCount:     len(assocs),
				LatestAssociationID:  latestAssocID,
				LatestManifestDigest: latestManifestDigest,
				Associations:         assocs,
			},
		})
		nextSeq = r.event.Sequence
	}
	pendingIDs, err := pendingResultJobIDsTx(ctx, tx, projectID)
	if err != nil {
		return out, err
	}
	out = ProjectResultsOutput{
		ProjectID:           projectID,
		AfterSequence:       afterSeq,
		NextSequence:        nextSeq,
		ReadAt:              now(),
		Freshness:           "stored_db_snapshot",
		PendingResultJobIDs: pendingIDs,
		Items:               items,
	}
	return out, nil
}

// projectManifestBytes verifies the exact stored manifest before returning it.
func (s *store) projectManifestBytes(projectID, digest string) (_ []byte, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeDatabaseRead, resultErr) }()
	if !isValidProjectID(projectID) || !isValidHexLen(digest, 64) {
		return nil, faults.New(faults.CodeValidationFailed, "project manifest requires valid PROJECT (proj-...) and 64-hex DIGEST")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	manifest, err := dbquery.New(tx).ProjectManifest(ctx, digest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, faults.Wrap(faults.CodeNotFound, fmt.Sprintf("manifest %s does not exist in project %s", digest, projectID), err)
	}
	if err != nil {
		return nil, err
	}
	if manifest.ProjectID != projectID {
		return nil, faults.New(faults.CodeNotFound, fmt.Sprintf("manifest %s does not belong to project %s", digest, projectID))
	}
	if len(manifest.Canonical) == 0 || manifest.Version != 1 || sha256Hex(manifest.Canonical) != digest {
		return nil, faults.New(faults.CodeInvariantFailed, fmt.Sprintf("manifest %s failed stored integrity check", digest))
	}
	if _, err := parseManifestBytes(manifest.Canonical, manifest.Kind, projectID); err != nil {
		return nil, faults.Wrap(faults.CodeInvariantFailed, fmt.Sprintf("manifest %s failed schema verification", digest), err)
	}
	return manifest.Canonical, nil
}
