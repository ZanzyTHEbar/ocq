package core

import (
	"context"
	"database/sql"
	"errors"
)

type taskHandoffTarget struct {
	RequestID, IntentDigest, SetupID string
	Binding                          ProjectBinding
}

// TaskHandoff is an outbox projection referencing the existing inbox, not a
// delivery attempt. The request and inbox IDs are the Main tool's pointer.
type TaskHandoff struct {
	RequestID     string `json:"request_id"`
	InboxID       string `json:"inbox_id,omitempty"`
	AssociationID string `json:"association_id,omitempty"`
	ResultDigest  string `json:"result_sha256,omitempty"`
	MainRevision  int    `json:"main_revision"`
	MainSetupID   string `json:"main_setup_id"`
	MainSessionID string `json:"main_session_id"`
	State         string `json:"state"`
	Reason        string `json:"reason,omitempty"`
}

type taskHandoffRow struct {
	TaskHandoff
	job, intent, lane string
	sequence          int64
}

func taskHandoffFrom(ctx context.Context, q sqlQueryer, project, request string) (h taskHandoffRow, err error) {
	err = q.QueryRowContext(ctx, `SELECT request_id,coalesce(inbox_id,''),coalesce(assoc_id,''),
 coalesce(manifest_digest,''),main_revision,main_setup_id,main_session_id,state,reason,
 job_id,intent_digest,coalesce(lane_id,''),coalesce(inbox_sequence,0)
 FROM project_task_handoff_status WHERE project_id=? AND request_id=?`, project, request).
		Scan(&h.RequestID, &h.InboxID, &h.AssociationID, &h.ResultDigest, &h.MainRevision, &h.MainSetupID,
			&h.MainSessionID, &h.State, &h.Reason, &h.job, &h.intent, &h.lane, &h.sequence)
	return h, err
}

func (s *store) insertTaskHandoff(ctx context.Context, c *sql.Conn, t taskHandoffTarget, parsed parsedProjectSubmission, job string) error {
	p := parsed.Envelope
	current, err := s.currentProjectBinding(ctx, c, p.ProjectID)
	if err != nil {
		return err
	}
	if current != t.Binding || t.Binding.Revision != p.BindingRevision || p.Role != "implementation" ||
		!workID(t.SetupID) || !workID(t.RequestID) || !isValidHexLen(t.IntentDigest, 64) ||
		p.Key != workSubmissionKey(p.ProjectID, t.RequestID) {
		return errors.New("exact standalone Main selection changed; no admission")
	}
	_, err = c.ExecContext(ctx, `INSERT INTO project_task_handoffs
 (project_id,request_id,intent_digest,job_id,main_revision,main_setup_id,main_session_id)
 VALUES (?,?,?,?,?,?,?)`, p.ProjectID, t.RequestID, t.IntentDigest, job, current.Revision, t.SetupID, current.ElephantSessionID)
	return err
}

// Called only after the existing lane seal and auto-mail insert within the
// result writer's transaction. Failure rolls back publication, not execution.
func publishTaskHandoff(ctx context.Context, c *sql.Conn, m ProjectInboxMessage) error {
	ready, err := mainHandledSchemaReady(c)
	if err != nil || !ready {
		return err
	}
	var pointer sql.NullString
	var mainSession string
	err = c.QueryRowContext(ctx, `SELECT inbox_id,main_session_id FROM project_task_handoffs WHERE job_id=?`, m.JobID).
		Scan(&pointer, &mainSession)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && pointer.Valid) {
		// Legacy/manual publication, or the original handoff was already
		// published. Superseding associations may not retarget it.
		return nil
	}
	if err != nil {
		return err
	}
	job, err := getJobTx(ctx, c, m.JobID)
	if err != nil {
		return err
	}
	if !taskHandoffTerminal(job, mainSession) {
		return errors.New("standalone handoff requires retained terminal proof and a distinct task session; result not published")
	}
	_, err = c.ExecContext(ctx, `UPDATE project_task_handoffs SET inbox_id=? WHERE job_id=?`, m.ID, m.JobID)
	return err
}
