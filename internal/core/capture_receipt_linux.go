package core

import (
	"context"
	"errors"
	"strconv"

	"ocq/internal/core/dbquery"
)

// RecordedCaptureReceipt observes an ALREADY published capture. It neither
// imports bytes nor captures the worktree, creates a claim, seals a lane or
// repairs mail. Receipt-first recovery has no second attempt to authorize.
type RecordedCaptureReceipt struct {
	State           string                  `json:"state"`
	Target          ResultPublicationTarget `json:"target"`
	Association     PublicationAssociation  `json:"association"`
	OwnershipDigest string                  `json:"ownership_sha256"`
}

func (r *Reader) RecordedCaptureReceipt(t ResultPublicationTarget, digest string) (RecordedCaptureReceipt, string, error) {
	out := RecordedCaptureReceipt{State: "unavailable", Target: t}
	var exact string
	if !t.valid() || !isValidHexLen(digest, 64) {
		return out, exact, errors.New("exact recorded capture target required")
	}
	err := r.store.readSchema5(func(q sqlQueryer) error {
		owner, err := publicationOwnership(r.store, q, t)
		if err != nil {
			return err
		}
		var raw []byte
		if err = q.QueryRowContext(context.Background(), "SELECT canonical FROM project_manifests WHERE project_id=? AND digest=?", t.Project, digest).Scan(&raw); err != nil {
			return err
		}
		m, err := parseManifestBytes(raw, "result", t.Project)
		if err != nil || m.Digest != digest || m.Source != "machine_capture" || m.Result == nil {
			return errors.New("original machine capture manifest required")
		}
		result := m.Result
		if result.JobID != t.Job || result.ClaimLabel != "machine_verified_snapshot" || result.ObservedAt == nil ||
			result.BeforeStatusDigest != result.AfterStatusDigest || result.BeforeContentDigest != result.AfterContentDigest ||
			!isValidHexLen(result.AfterStatusDigest, 64) || !isValidHexLen(result.AfterContentDigest, 64) ||
			result.ClaimedValidation.Status != "not_run" || len(result.ClaimedValidation.Commands) != 0 || result.ClaimedValidation.Results != "not_run" {
			return errors.New("stored capture provenance differs")
		}
		row, err := dbquery.New(q).ExistingAssociation(context.Background(), dbquery.ExistingAssociationParams{ProjectID: t.Project, JobID: t.Job, Kind: "result", ManifestDigest: digest})
		if err != nil {
			return err
		}
		var sup *string
		if row.SupersedesID.Valid {
			sup = &row.SupersedesID.String
		}
		supID := ""
		if sup != nil {
			supID = *sup
		}
		if row.ID != deterministicAssociationID(t.Project, t.Job, "result", digest, supID) {
			return errors.New("capture receipt lineage differs")
		}
		if err = verifyStoredManifest(context.Background(), q, m); err != nil {
			return err
		}
		// Link the original snapshot to the immutable agent receipt/location.
		location, err := dbquery.New(q).AgentLocation(context.Background(), t.Job)
		if err != nil || location.Worktree != result.Worktree {
			return errors.New("capture receipt worktree differs")
		}
		a := publicationAssociation(summarizeAssociation(row.ID, row.Sequence, t.Project, t.Job, "result", digest, sup, row.RecordedAt, &m, true))
		if a.Source != "machine_capture" || a.Verification != "machine_verified_snapshot" || a.Sequence != strconv.FormatInt(row.Sequence, 10) {
			return errors.New("capture receipt provenance unavailable")
		}
		out.State, out.Association, out.OwnershipDigest = "recorded_observed", a, owner
		exact = string(raw)
		return nil
	})
	return out, exact, err
}
