package main

import (
	"strconv"

	"ocq/internal/core"
)

// Bridge projections are allowlists, not raw core jobs with fields redacted.
// Ownership capabilities, consumed input and evidence bodies stay in the core.
// Exact retained manifest content remains available through explicit inspection.
type recordedAgent struct {
	SessionID     string `json:"session_id"`
	Worktree      string `json:"worktree"`
	ProjectID     string `json:"project_id"`
	Phase         string `json:"phase"`
	ServerOutcome string `json:"server_outcome"`
}

func projectAgent(a *core.AgentOwnership) *recordedAgent {
	if a == nil {
		return nil
	}
	return &recordedAgent{a.SessionID, a.Worktree, a.ProjectID, a.Phase, a.ServerOutcome}
}

type storedJob struct {
	ID           string         `json:"id"`
	JobID        string         `json:"job_id,omitempty"`
	Sequence     string         `json:"sequence,omitempty"`
	State        string         `json:"state"`
	Reason       string         `json:"reason"`
	Outcome      string         `json:"outcome"`
	ExitCode     *int           `json:"exit_code,omitempty"`
	Validation   string         `json:"validation"`
	OutputStatus string         `json:"output_status"`
	AcceptedAt   string         `json:"accepted_at,omitempty"`
	Freshness    string         `json:"freshness,omitempty"`
	Agent        *recordedAgent `json:"agent,omitempty"`
}

type claimedValidation struct {
	AssociationID  string `json:"association_id"`
	ManifestDigest string `json:"manifest_digest"`
	Source         string `json:"source"`
	ClaimLabel     string `json:"claim_label"`
	Assertion      string `json:"assertion"`
	Verified       bool   `json:"verified"`
	Status         string `json:"status"`
}

type validationSummary struct {
	Status            string             `json:"status"`
	Verified          bool               `json:"verified"`
	Verification      string             `json:"verification"`
	ClaimedValidation *claimedValidation `json:"claimed_validation,omitempty"`
}

func projectValidation(v core.ProjectValidationDimension) validationSummary {
	out := validationSummary{Status: v.Status, Verified: v.Verified, Verification: v.Verification}
	if c := v.ClaimedValidation; c != nil {
		out.ClaimedValidation = &claimedValidation{c.AssociationID, c.ManifestDigest, c.Source, c.ClaimLabel, c.Assertion, c.Verified, c.Status}
	}
	return out
}

type projectJobDetail struct {
	ProjectID                 string                          `json:"project_id"`
	JobID                     string                          `json:"job_id"`
	Role                      string                          `json:"role"`
	BindingRevision           int                             `json:"binding_revision"`
	BriefDigest               string                          `json:"brief_digest"`
	TargetJobID               *string                         `json:"target_job_id"`
	TargetResultDigest        *string                         `json:"target_result_digest"`
	RecordedAt                string                          `json:"recorded_at"`
	LaneID                    string                          `json:"lane_id,omitempty"`
	FsforkDriver              string                          `json:"fsfork_driver,omitempty"`
	FsforkPath                string                          `json:"fsfork_path,omitempty"`
	SealedManifestDigest      *string                         `json:"sealed_manifest_digest,omitempty"`
	ReadAt                    string                          `json:"read_at"`
	Freshness                 string                          `json:"freshness"`
	Job                       storedJob                       `json:"job"`
	CompletionEvent           *core.Notification              `json:"completion_event"`
	ResultStatus              string                          `json:"result_status"`
	Associations              []core.ProjectResultAssociation `json:"associations"`
	Execution                 core.ProjectExecutionDimension  `json:"execution"`
	Validation                validationSummary               `json:"validation"`
	Review                    core.ProjectReviewDimension     `json:"review"`
	Integration               string                          `json:"integration"`
	WorktreeMatch             string                          `json:"worktree_match"`
	IntegratorUnreadMailCount int                             `json:"integrator_unread_mail_count"`
}

func projectDetail(d core.ProjectJobDetailOutput) projectJobDetail {
	j := d.Job
	return projectJobDetail{
		ProjectID: d.ProjectID, JobID: d.JobID, Role: d.Role, BindingRevision: d.BindingRevision, BriefDigest: d.BriefDigest,
		TargetJobID: d.TargetJobID, TargetResultDigest: d.TargetResultDigest, RecordedAt: d.RecordedAt, LaneID: d.LaneID,
		FsforkDriver: d.FsforkDriver, FsforkPath: d.FsforkPath, SealedManifestDigest: d.SealedManifestDigest,
		ReadAt: d.ReadAt, Freshness: d.Freshness,
		Job:             storedJob{ID: j.ID, Sequence: strconv.FormatInt(j.Sequence, 10), State: j.State, Reason: j.Reason, Outcome: j.Outcome, ExitCode: j.ExitCode, Validation: j.Validation, OutputStatus: j.OutputStatus, AcceptedAt: j.AcceptedAt, Agent: projectAgent(j.Agent)},
		CompletionEvent: d.CompletionEvent, ResultStatus: d.ResultStatus, Associations: d.Associations, Execution: d.Execution,
		Validation: projectValidation(d.Validation), Review: d.Review, Integration: d.Integration, WorktreeMatch: d.WorktreeMatch, IntegratorUnreadMailCount: d.IntegratorUnreadMailCount,
	}
}

type projectResult struct {
	ProjectID       string                    `json:"project_id"`
	JobID           string                    `json:"job_id"`
	Role            string                    `json:"role"`
	BindingRevision int                       `json:"binding_revision"`
	Event           core.Notification         `json:"event"`
	CurrentJob      storedJob                 `json:"current_job"`
	ResultSummary   core.ProjectResultSummary `json:"result_summary"`
}

type projectResultsPage struct {
	ProjectID           string          `json:"project_id"`
	AfterSequence       int64           `json:"after_sequence"`
	NextSequence        int64           `json:"next_sequence"`
	ReadAt              string          `json:"read_at"`
	Freshness           string          `json:"freshness"`
	PendingResultJobIDs []string        `json:"pending_result_job_ids"`
	Items               []projectResult `json:"items"`
}

func projectResults(p core.ProjectResultsOutput) projectResultsPage {
	out := projectResultsPage{ProjectID: p.ProjectID, AfterSequence: p.AfterSequence, NextSequence: p.NextSequence, ReadAt: p.ReadAt, Freshness: p.Freshness, PendingResultJobIDs: p.PendingResultJobIDs, Items: []projectResult{}}
	for _, item := range p.Items {
		j := item.CurrentJob
		out.Items = append(out.Items, projectResult{item.ProjectID, item.JobID, item.Role, item.BindingRevision, item.Event,
			storedJob{ID: j.ID, JobID: j.JobID, State: j.State, Outcome: j.Outcome, Reason: j.Reason, Validation: j.Validation, OutputStatus: j.OutputStatus, Agent: projectAgent(j.Agent), Freshness: j.Freshness}, item.ResultSummary})
	}
	return out
}

type projectJobView struct {
	JobID                string                         `json:"job_id"`
	ID                   string                         `json:"id"`
	Sequence             int64                          `json:"sequence"`
	ProjectID            string                         `json:"project_id"`
	Role                 string                         `json:"role"`
	BindingRevision      int                            `json:"binding_revision"`
	BriefDigest          string                         `json:"brief_digest"`
	TargetJobID          *string                        `json:"target_job_id"`
	TargetResultDigest   *string                        `json:"target_result_digest"`
	RecordedAt           string                         `json:"recorded_at"`
	LaneID               string                         `json:"lane_id,omitempty"`
	FsforkDriver         string                         `json:"fsfork_driver,omitempty"`
	FsforkPath           string                         `json:"fsfork_path,omitempty"`
	SealedManifestDigest *string                        `json:"sealed_manifest_digest,omitempty"`
	State                string                         `json:"state"`
	Outcome              string                         `json:"outcome"`
	Reason               string                         `json:"reason"`
	ResultStatus         string                         `json:"result_status"`
	AssociationCount     int                            `json:"association_count"`
	Execution            core.ProjectExecutionDimension `json:"execution"`
	Validation           validationSummary              `json:"validation"`
	Review               core.ProjectReviewDimension    `json:"review"`
	Integration          string                         `json:"integration"`
	WorktreeMatch        string                         `json:"worktree_match"`
	Freshness            string                         `json:"freshness"`
}

type projectBinding struct {
	ProjectID             string `json:"project_id"`
	Revision              int    `json:"revision"`
	RepoRoot              string `json:"repo_root"`
	GitCommonDir          string `json:"git_common_dir"`
	CommitAnchor          string `json:"commit_anchor"`
	ElephantSessionID     string `json:"elephant_session_id"`
	OpenCodeProjectID     string `json:"opencode_project_id"`
	OpenCodeDirectory     string `json:"opencode_directory"`
	HandoffManifestDigest string `json:"handoff_manifest_digest"`
	VerifiedAt            string `json:"verified_at"`
	RecordedAt            string `json:"recorded_at"`
}

type bindingAvailability struct {
	Status     string `json:"status"`
	VerifiedAt string `json:"verified_at"`
	Freshness  string `json:"freshness"`
}

type projectShow struct {
	ProjectID                 string                `json:"project_id"`
	Label                     string                `json:"label"`
	CreatedAt                 string                `json:"created_at"`
	ReadAt                    string                `json:"read_at"`
	Freshness                 string                `json:"freshness"`
	CurrentBinding            projectBinding        `json:"current_binding"`
	BindingAvailability       bindingAvailability   `json:"binding_availability"`
	BindingCount              int                   `json:"binding_count"`
	JobCounts                 core.ProjectJobCounts `json:"job_counts"`
	Jobs                      []projectJobView      `json:"jobs"`
	Lanes                     []core.ProjectLane    `json:"lanes"`
	IntegratorUnreadMailCount int                   `json:"integrator_unread_mail_count"`
	PendingResultJobIDs       []string              `json:"pending_result_job_ids"`
}

type projectBoard struct {
	Project    projectShow                `json:"project"`
	MailReadAt string                     `json:"mail_read_at"`
	Messages   []core.ProjectInboxMessage `json:"messages"`
}

func boardView(b core.Board) projectBoard {
	p, binding := b.Project, b.Project.CurrentBinding
	out := projectShow{ProjectID: p.ProjectID, Label: p.Label, CreatedAt: p.CreatedAt, ReadAt: p.ReadAt, Freshness: p.Freshness,
		CurrentBinding:      projectBinding{binding.ProjectID, binding.Revision, binding.RepoRoot, binding.GitCommonDir, binding.CommitAnchor, binding.ElephantSessionID, binding.OpenCodeProjectID, binding.OpenCodeDirectory, binding.HandoffManifestDigest, binding.VerifiedAt, binding.RecordedAt},
		BindingAvailability: bindingAvailability{"last_observed", binding.VerifiedAt, p.Freshness}, BindingCount: p.BindingCount, JobCounts: p.JobCounts,
		Jobs: []projectJobView{}, Lanes: p.Lanes, IntegratorUnreadMailCount: p.IntegratorUnreadMailCount, PendingResultJobIDs: p.PendingResultJobIDs}
	for _, j := range p.Jobs {
		out.Jobs = append(out.Jobs, projectJobView{j.JobID, j.ID, j.Sequence, j.ProjectID, j.Role, j.BindingRevision, j.BriefDigest, j.TargetJobID, j.TargetResultDigest, j.RecordedAt, j.LaneID, j.FsforkDriver, j.FsforkPath, j.SealedManifestDigest, j.State, j.Outcome, j.Reason, j.ResultStatus, j.AssociationCount, j.Execution, projectValidation(j.Validation), j.Review, j.Integration, j.WorktreeMatch, j.Freshness})
	}
	return projectBoard{out, b.MailReadAt, b.Messages}
}
