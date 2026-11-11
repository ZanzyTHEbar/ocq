package core

import (
	"context"
	"errors"
	"sort"
	"unicode/utf8"

	"ocq/internal/core/dbquery"
)

// ComposeContext is stored routing metadata, never authentication/OS authority.
// No subject, body, manifest contents, credentials or execution controls.
type ComposeContext struct {
	ProjectID         string   `json:"project_id"`
	LaneID            string   `json:"lane_id"`
	JobID             string   `json:"job_id"`
	LanePath          string   `json:"lane_path"`
	IntegratorSenders []string `json:"integrator_senders"`
	RecipientLanes    []string `json:"recipient_lanes"`
	Manifest          string   `json:"manifest"`
	ReplyTo           string   `json:"reply_to"`
	ReadAt            string   `json:"read_at"`
	Freshness         string   `json:"freshness"`
}

// ComposeContext validates exact retained optional links and reads route choices
// from one non-initializing SQLite snapshot. It neither prepares nor sends mail.
func (r *Reader) ComposeContext(project, lane, manifest, reply string) (ComposeContext, error) {
	out := ComposeContext{ProjectID: project, LaneID: lane, Manifest: manifest, ReplyTo: reply, IntegratorSenders: []string{}, RecipientLanes: []string{}}
	if !isValidProjectID(project) || !isValidLaneID(lane) || (manifest != "" && !isValidHexLen(manifest, 64)) || (reply != "" && !isValidInboxMessageID(reply)) {
		return out, errors.New("exact compose context identifiers required")
	}
	err := r.store.readSchema5(func(tx sqlQueryer) error {
		ctx := context.Background()
		queries := dbquery.New(tx)
		owner, err := queries.LaneOwnerAndPath(ctx, dbquery.LaneOwnerAndPathParams{ProjectID: project, LaneID: lane})
		if err != nil {
			return err
		}
		out.JobID, out.LanePath = owner.OwnerJobID, owner.FsforkPath
		if !utf8.ValidString(out.LanePath) {
			return errors.New("stored lane path is not lossless UTF-8")
		}
		binding, err := r.store.currentProjectBinding(ctx, tx, project)
		if err != nil {
			return err
		}
		senders, err := queries.ComposeIntegratorSenders(ctx, project)
		if err != nil {
			return err
		}
		if binding.ElephantSessionID != "" {
			senders = append(senders, "integrator:"+binding.ElephantSessionID)
		}
		sort.Strings(senders)
		for _, sender := range senders {
			if validateInboxSender(sender, lane, owner.OwnerJobID) != nil {
				return errors.New("invalid stored integrator metadata")
			}
			if len(out.IntegratorSenders) == 0 || sender != out.IntegratorSenders[len(out.IntegratorSenders)-1] {
				out.IntegratorSenders = append(out.IntegratorSenders, sender)
			}
		}
		lanes, err := queries.ProjectLanes(ctx, project)
		if err != nil {
			return err
		}
		for _, recipient := range lanes {
			out.RecipientLanes = append(out.RecipientLanes, recipient.LaneID)
		}
		if manifest != "" {
			p, err := queries.ManifestProject(ctx, manifest)
			if err != nil || p != project {
				return errors.New("exact retained manifest does not belong to project")
			}
		}
		if reply != "" {
			p, err := queries.InboxMessageProject(ctx, reply)
			if err != nil || p != project {
				return errors.New("exact reply message does not belong to project")
			}
		}
		return nil
	})
	if err == nil {
		out.ReadAt, out.Freshness = now(), "stored_db_snapshot"
	}
	return out, err
}
