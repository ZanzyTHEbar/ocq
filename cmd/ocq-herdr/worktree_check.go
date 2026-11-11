package main

import (
	"path/filepath"
	"regexp"
	"time"

	"ocq/internal/core"
)

// This allowlist contains no manifest contents, request, evidence or ownership
// capabilities. AttemptedAt is not a successful filesystem observation time.
type worktreeCheck struct {
	ProjectID       string `json:"project_id"`
	JobID           string `json:"job_id"`
	AssociationID   string `json:"association_id"`
	ManifestDigest  string `json:"manifest_digest"`
	Worktree        string `json:"worktree"`
	AttemptedAt     string `json:"attempted_at"`
	CheckedAt       string `json:"checked_at,omitempty"`
	Freshness       string `json:"freshness"`
	WorktreeMatch   string `json:"worktree_match"`
	Source          string `json:"source,omitempty"`
	Verification    string `json:"verification,omitempty"`
	Validation      string `json:"validation"`
	Integration     string `json:"integration"`
	DifferenceCount int    `json:"difference_count"`
	Error           string `json:"error,omitempty"`
}

var snapshotDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

func verifyOwnedWorktree(r *core.Reader, args []string) (any, error) {
	out := worktreeCheck{ProjectID: args[0], JobID: args[1], AssociationID: args[2], ManifestDigest: args[3], Worktree: args[4], AttemptedAt: time.Now().UTC().Format(time.RFC3339Nano), Freshness: "not_checked", WorktreeMatch: "unknown", Validation: "unknown", Integration: "not_authorized"}
	if !snapshotDigest.MatchString(out.ManifestDigest) || !filepath.IsAbs(out.Worktree) || filepath.Clean(out.Worktree) != out.Worktree {
		out.Error = "Exact result digest and stored owned worktree required; arbitrary paths and manifest-path fallback are not supported."
		return out, nil
	}
	d, err := r.Job(out.ProjectID, out.JobID)
	if err != nil {
		out.Error = "Exact project/job unavailable; no filesystem check performed."
		return out, nil
	}
	out.Validation = d.Job.Validation
	var association *core.ProjectResultAssociation
	for i := range d.Associations {
		a := &d.Associations[i]
		if a.ID == out.AssociationID && a.ManifestDigest == out.ManifestDigest && a.Kind == "result" && a.ProjectID == out.ProjectID && a.JobID == out.JobID {
			association = a
			break
		}
	}
	if association == nil {
		out.Error = "Exact recorded result association unavailable; no filesystem check performed."
		return out, nil
	}
	out.Source, out.Verification = association.Source, association.Verification
	if association.Source != "machine_capture" || association.Verification != "machine_verified_snapshot" {
		out.Error = "This result has structure/hash/linkage verification only, not a machine-captured snapshot. Herdr does not offer live verification of imported claims."
		return out, nil
	}
	if association.Availability != "available" || d.Role != "implementation" || d.Job.Agent == nil || d.Job.Agent.Worktree == "" || d.Job.Agent.Worktree != out.Worktree {
		out.Error = "Result or exact stored owned implementation worktree unavailable or changed; no path fallback or adoption is allowed."
		return out, nil
	}
	lanes, err := r.Lanes(out.ProjectID)
	if err != nil {
		out.Error = "Retained owned lane unavailable; no filesystem check performed."
		return out, nil
	}
	laneMatches := false
	for _, lane := range lanes.Lanes {
		if lane.LaneID == d.LaneID && lane.ProjectID == out.ProjectID && lane.OwnerJobID == out.JobID && lane.Role == "implementation" && lane.FsforkPath != "" && lane.FsforkPath == d.FsforkPath && lane.FsforkPath == out.Worktree {
			laneMatches = true
			break
		}
	}
	if !laneMatches {
		out.Error = "Retained lane owner/path and recorded agent worktree must agree; no filesystem check or path fallback is allowed."
		return out, nil
	}
	result, err := r.VerifyWorktree(core.WorktreeCheckRequest{ProjectID: out.ProjectID, JobID: out.JobID, ManifestDigest: out.ManifestDigest, Worktree: out.Worktree})
	if err != nil {
		// Core diagnostics may contain paths or private manifest claims. Keep the
		// bridge error narrow; never turn an error into a retained good check.
		out.Error = "Core could not verify this exact retained snapshot; match unknown. Inspect the local core CLI for diagnostics."
		return out, nil
	}
	if result.ProjectID != out.ProjectID || result.JobID != out.JobID || result.ManifestDigest != out.ManifestDigest || result.Worktree != out.Worktree {
		out.Error = "Core check context changed; match unknown."
		return out, nil
	}
	out.CheckedAt, out.Freshness, out.WorktreeMatch, out.Integration = result.CheckedAt, result.Freshness, result.WorktreeMatch, result.Integration
	out.DifferenceCount = len(result.Differences)
	if result.WorktreeMatch == "worktree_unavailable" {
		out.Error = "Owned worktree is unavailable or cannot be inspected; no match established. No replacement path is used."
	}
	return out, nil
}
