package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ZanzyTHEbar/faults-go"

	"ocq/internal/core/dbquery"
)

func (s *store) projects() (_ ProjectList, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeDatabaseRead, resultErr) }()
	var out ProjectList
	rows, err := dbquery.New(s.db).ProjectSummaries(context.Background())
	if err != nil {
		return out, err
	}
	projects := []ProjectSummary{}
	for _, row := range rows {
		projects = append(projects, ProjectSummary{ProjectID: row.ID, Label: row.Label, BindingRevision: int(row.Revision), RepoRoot: row.RepoRoot, IntegratorUnreadMailCount: int(row.Count)})
	}
	return ProjectList{ReadAt: now(), Freshness: "stored_db_snapshot", Projects: projects}, nil
}

// projectShowData is shared by the CLI and terminal views. It observes the
// database only; it does not inspect or reconcile any execution resources.
func (s *store) projectShowData(projectID string) (_ ProjectShowOutput, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeDatabaseRead, resultErr) }()
	var out ProjectShowOutput
	if !isValidProjectID(projectID) {
		return out, faults.New(faults.CodeValidationFailed, "project show requires a valid PROJECT ID (proj-...)")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()

	details, err := dbquery.New(tx).ProjectDetails(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return out, faults.Wrap(faults.CodeNotFound, fmt.Sprintf("project %s does not exist", projectID), err)
	}
	if err != nil {
		return out, err
	}
	label, createdAt := details.Label, details.CreatedAt

	currentBinding, err := s.currentProjectBinding(ctx, tx, projectID)
	if err != nil {
		return out, err
	}
	bindingCount64, err := dbquery.New(tx).ProjectBindingCount(ctx, projectID)
	if err != nil {
		return out, err
	}
	bindingCount := int(bindingCount64)

	rows, err := dbquery.New(tx).ProjectJobs(ctx, projectID)
	if err != nil {
		return out, err
	}
	var pjRows []projectJobRow
	for _, row := range rows {
		pjRows = append(pjRows, projectJobFromRow(row))
	}

	lanes, err := loadProjectLanesTx(ctx, tx, projectID)
	if err != nil {
		return out, err
	}
	lanesByJob := make(map[string]ProjectLane, len(lanes))
	for _, lane := range lanes {
		lanesByJob[lane.OwnerJobID] = lane
	}
	integratorUnread, err := integratorUnreadCountTx(ctx, tx, projectID)
	if err != nil {
		return out, err
	}

	var counts ProjectJobCounts
	jobViews := make([]ProjectJobView, 0, len(pjRows))
	pendingIDs := []string{}
	for _, pj := range pjRows {
		j, err := getJobTx(ctx, tx, pj.jobID)
		if err != nil {
			return out, err
		}
		assocs, manifests, resultStatus, err := loadJobAssociationsTx(ctx, tx, projectID, pj.jobID)
		if err != nil {
			return out, err
		}
		execDim, valDim, revDim, err := computeJobDimensionsTx(ctx, tx, pj, j, assocs, manifests)
		if err != nil {
			return out, err
		}

		counts.Total++
		switch j.State {
		case "queued":
			counts.Queued++
			counts.Active++
		case "launching":
			counts.Launching++
			counts.Active++
		case "running":
			counts.Running++
			counts.Active++
		case "unknown":
			counts.Unknown++
		case "finished":
			counts.Finished++
		case "cancelled":
			counts.Cancelled++
		case "resolved":
			counts.Resolved++
		}
		if pj.role == "review" {
			counts.Review++
		}
		if j.Outcome == "blocked" {
			counts.Blocked++
		}
		if isTerminalState(j.State) && len(assocs) == 0 {
			counts.PendingResults++
			pendingIDs = append(pendingIDs, j.ID)
		}

		view := ProjectJobView{
			JobID:              j.ID,
			ID:                 j.ID,
			Sequence:           j.Sequence,
			ProjectID:          pj.projectID,
			Role:               pj.role,
			BindingRevision:    pj.bindingRevision,
			BriefDigest:        pj.briefDigest,
			TargetJobID:        pj.targetJobID,
			TargetResultDigest: pj.targetResultDigest,
			RecordedAt:         pj.recordedAt,
			State:              j.State,
			Outcome:            j.Outcome,
			Reason:             j.Reason,
			ResultStatus:       resultStatus,
			AssociationCount:   len(assocs),
			Execution:          execDim,
			Validation:         valDim,
			Review:             revDim,
			Integration:        "not_authorized",
			WorktreeMatch:      "unknown",
			Freshness:          "stored_db_snapshot",
		}
		if lane, ok := lanesByJob[j.ID]; ok {
			view.LaneID = lane.LaneID
			view.FsforkDriver = lane.FsforkDriver
			view.FsforkPath = lane.FsforkPath
			view.SealedManifestDigest = lane.SealedManifestDigest
		}
		jobViews = append(jobViews, view)
	}

	out = ProjectShowOutput{
		ProjectID:      projectID,
		Label:          label,
		CreatedAt:      createdAt,
		ReadAt:         now(),
		Freshness:      "stored_db_snapshot",
		CurrentBinding: currentBinding,
		BindingAvailability: map[string]any{
			"status":      "last_observed",
			"verified_at": currentBinding.VerifiedAt,
			"freshness":   "stored_db_snapshot",
		},
		BindingCount:              bindingCount,
		JobCounts:                 counts,
		Jobs:                      jobViews,
		Lanes:                     lanes,
		IntegratorUnreadMailCount: integratorUnread,
		PendingResultJobIDs:       pendingIDs,
	}
	return out, nil
}

// projectJobData reads a coherent stored view without observing live resources.
func (s *store) projectJobData(projectID, jobID string) (_ ProjectJobDetailOutput, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeDatabaseRead, resultErr) }()
	var out ProjectJobDetailOutput
	if !isValidProjectID(projectID) || !isValidJobID(jobID) {
		return out, faults.New(faults.CodeValidationFailed, "project job requires valid PROJECT (proj-...) and JOB (job-...)")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()

	projectJob, err := dbquery.New(tx).ProjectJobByID(ctx, dbquery.ProjectJobByIDParams{ProjectID: projectID, JobID: jobID})
	if errors.Is(err, sql.ErrNoRows) {
		return out, faults.Wrap(faults.CodeNotFound, fmt.Sprintf("job %s is not attributed to project %s", jobID, projectID), err)
	}
	if err != nil {
		return out, err
	}
	pj := projectJobFromRow(projectJob)
	j, err := getJobTx(ctx, tx, jobID)
	if err != nil {
		return out, err
	}
	var completionEvent *Notification
	row, err := dbquery.New(tx).NotificationByJob(ctx, jobID)
	if err == nil {
		n := notificationFromRow(dbquery.NotificationByIDRow(row))
		completionEvent = &n
	} else if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	assocs, manifests, resultStatus, err := loadJobAssociationsTx(ctx, tx, projectID, jobID)
	if err != nil {
		return out, err
	}
	execDim, valDim, revDim, err := computeJobDimensionsTx(ctx, tx, pj, j, assocs, manifests)
	if err != nil {
		return out, err
	}
	var laneSealed *string
	lane, err := dbquery.New(tx).LaneDetailForJob(ctx, dbquery.LaneDetailForJobParams{ProjectID: projectID, OwnerJobID: jobID})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if err == nil {
		laneSealed = nullableSQLString(lane.SealedManifestDigest)
	}
	integratorUnread, err := integratorUnreadCountTx(ctx, tx, projectID)
	if err != nil {
		return out, err
	}
	out = ProjectJobDetailOutput{
		ProjectID:                 pj.projectID,
		JobID:                     pj.jobID,
		Role:                      pj.role,
		BindingRevision:           pj.bindingRevision,
		BriefDigest:               pj.briefDigest,
		TargetJobID:               pj.targetJobID,
		TargetResultDigest:        pj.targetResultDigest,
		RecordedAt:                pj.recordedAt,
		LaneID:                    lane.LaneID,
		FsforkDriver:              lane.FsforkDriver,
		FsforkPath:                lane.FsforkPath,
		SealedManifestDigest:      laneSealed,
		ReadAt:                    now(),
		Freshness:                 "stored_db_snapshot",
		Job:                       j,
		CompletionEvent:           completionEvent,
		ResultStatus:              resultStatus,
		Associations:              assocs,
		Execution:                 execDim,
		Validation:                valDim,
		Review:                    revDim,
		Integration:               "not_authorized",
		WorktreeMatch:             "unknown",
		IntegratorUnreadMailCount: integratorUnread,
	}
	return out, nil
}
