package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
)

// Use the real standalone preparation, SQLite projection and mailbox. Seed
// earlier admission/observer writes without contacting OpenCode or dispatching.
func TestStandaloneTaskLifecycle(t *testing.T) {
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	repo, dir := root, filepath.Join(root, "queue")
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "-qm", "base", "--allow-empty", "--no-verify"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, b)
		}
	}
	base, err := gitRead(repo, "rev-parse", "HEAD")
	must(err)
	q, err := OpenQueue(dir)
	must(err)
	defer q.Close()
	const project, id, job = "proj-task", "task-one", "job-standalone"
	must(q.store.write(func(c *sql.Conn) error {
		ctx := context.Background()
		handoff, err := parseManifestBytes([]byte(`{"version":1,"kind":"handoff","project_id":"proj-task","source":"operator_import","observed_at":null,"intent":"fixture","decisions":[],"risks":[]}`), "handoff", project)
		if err != nil {
			return err
		}
		if _, err = c.ExecContext(ctx, `INSERT INTO projects(id,label,created_at) VALUES(?, 'Fixture', 'time')`, project); err != nil {
			return err
		}
		if err = insertManifestConn(ctx, c, handoff, "time"); err != nil {
			return err
		}
		_, err = c.ExecContext(ctx, `INSERT INTO project_bindings(project_id,revision,repo_root,git_common_dir,commit_anchor,elephant_session_id,opencode_project_id,opencode_directory,handoff_manifest_digest,auth_ref,verified_at,recorded_at)
			VALUES (?,1,?,?,?,'ses_fixture','fixture',?,?,'fixture','time','time')`, project, repo, filepath.Join(repo, ".git"), base, repo, handoff.Digest)
		return err
	}))
	raw, err := json.Marshal(WorkPrepareRequest{Protocol: WorkProtocol, ProjectID: project, RequestID: id, Kind: "implementation",
		BindingRevision: "1", Brief: "Goal from the saved base", BaseRef: base,
		Profile: `{"agent":"build","provider_id":"fixture","model_id":"fixture","variant":"","memory_bytes":"1","permissions":[{"action":"*","resource":"*","effect":"deny"}]}`})
	must(err)
	prepared, fresh, err := PrepareStandaloneTask(dir, raw)
	must(err)
	if !fresh || prepared.State != "prepared" || prepared.TaskGoal == nil || prepared.TaskLifecycle == nil ||
		prepared.TaskLifecycle.WorkFinished != nil || prepared.TaskLifecycle.LaneID != "" || prepared.Job != nil {
		t.Fatalf("prepared task: %+v", prepared)
	}
	r, err := OpenReader(dir)
	must(err)
	defer r.Close()
	j, err := workReadDir(r, project)
	must(err)
	defer j.Close()
	_, p, _, err := workLoad(r, j, project, id)
	must(err)
	e, err := parseSubmissionEnvelope([]byte(p.Payload))
	must(err)
	must(q.store.write(func(c *sql.Conn) error {
		ctx := context.Background()
		must(insertManifestConn(ctx, c, e.BriefManifest, "time"))
		_, err := c.ExecContext(ctx, `INSERT INTO receipts(key,job_id,canonical,digest,accepted_at) VALUES(?,?,?,?,'time')`, e.Envelope.Key, job, e.RequestBytes, e.RequestDigest)
		must(err)
		_, err = c.ExecContext(ctx, `INSERT INTO jobs(id,state) VALUES(?,'running')`, job)
		must(err)
		_, err = c.ExecContext(ctx, `INSERT INTO agent_ownership(job_id,session_id,prompt_id,token,phase) VALUES(?,'ses_worker','msg_worker','test','running')`, job)
		must(err)
		_, err = c.ExecContext(ctx, `INSERT INTO project_submissions(key,envelope_digest,envelope_canonical,job_id,recorded_at) VALUES(?,?,?,?,'time')`, e.Envelope.Key, e.EnvelopeDigest, e.EnvelopeBytes, job)
		must(err)
		_, err = c.ExecContext(ctx, `INSERT INTO project_jobs(job_id,project_id,binding_revision,role,brief_digest,recorded_at) VALUES(?,?,1,'implementation',?,'time')`, job, project, e.BriefManifest.Digest)
		must(err)
		_, err = c.ExecContext(ctx, `INSERT INTO project_lanes(lane_id,project_id,owner_job_id,role,base_commit,fsfork_driver,created_at) VALUES(?,?,?,'implementation',?,'git_worktree','time')`, e.LaneID, project, job, base)
		return err
	}))
	check := func(state, outcome string, finished bool) {
		t.Helper()
		got, err := InspectProjectWork(dir, project, id)
		must(err)
		life := got.TaskLifecycle
		if got.State != "recorded_observed" || got.RequestID != id || got.TaskGoal == nil || *got.TaskGoal != *prepared.TaskGoal ||
			got.Receipt == nil || got.Receipt.JobID != job || got.Receipt.EnvelopeDigest != e.EnvelopeDigest ||
			got.Job == nil || got.Job.State != state || got.Job.Outcome != outcome || got.Job.TerminalProven ||
			life == nil || life.LaneID != e.LaneID || life.WorkFinished == nil || *life.WorkFinished != finished ||
			life.MainHandled != "unverified" || life.SessionArchived != "unverified" {
			t.Fatalf("unexpected task projection: %+v, job=%+v lifecycle=%+v", got, got.Job, life)
		}
	}
	msg, err := q.SendMail(MailRequest{ProjectID: project, Key: "work-note", From: "lane:" + e.LaneID, To: "integrator:" + project,
		LaneID: e.LaneID, JobID: job, Kind: "lane.blocked", Subject: "Any text", Body: "A progress note with no result."})
	must(err)
	_, err = q.AcknowledgeMail(project, msg.ID, false)
	must(err)
	_, err = q.SendMail(MailRequest{ProjectID: project, Key: "reply-note", From: "integrator:ses_fixture", To: "lane:" + e.LaneID,
		LaneID: e.LaneID, JobID: job, Kind: "lane.assigned", Subject: "Reply", Body: "Not evidence of an actor", ReplyTo: msg.ID})
	must(err)
	check("running", "pending", false)
	must(q.store.write(func(c *sql.Conn) error {
		_, err := c.ExecContext(context.Background(), `UPDATE agent_ownership SET phase='terminal',server_outcome='failed',terminal_evidence=? WHERE job_id=?`, filepath.Join(root, "missing-proof"), job)
		if err != nil {
			return err
		}
		_, err = c.ExecContext(context.Background(), `UPDATE jobs SET state='finished',outcome='agent_failed' WHERE id=?`, job)
		return err
	}))
	check("finished", "agent_failed", true)
	_, err = q.store.db.Exec(`UPDATE jobs SET state='unknown',outcome='agent_unknown' WHERE id=?`, job)
	must(err)
	var historical string
	must(q.store.db.QueryRow(`SELECT outcome FROM completion_notifications WHERE job_id=?`, job).Scan(&historical))
	if historical != "agent_failed" {
		t.Fatalf("expected retained first-terminal outcome, got %q", historical)
	}
	check("unknown", "agent_unknown", false)
}
