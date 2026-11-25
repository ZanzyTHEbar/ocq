package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	must(UpgradeProjectMainHandled(dir))
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
	rec, p, _, err := workLoad(r, j, project, id)
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
		must(err)
		return q.store.insertTaskHandoff(ctx, c, taskHandoffTarget{
			RequestID: id, IntentDigest: rec.Digest, SetupID: "fixture-main", Binding: rec.Binding,
		}, e, job)
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
			life.MainHandled != "unverified" || life.SessionArchived != "unverified" ||
			life.Handoff == nil || life.Handoff.State != "awaiting_result" || life.Handoff.InboxID != "" {
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
	manifest := ResultManifest{Version: 1, Kind: "result", ProjectID: project, Source: "author_import",
		JobID: job, RequestDigest: e.RequestDigest, Base: base, Worktree: repo, StagedStatus: "clean", UnstagedStatus: "clean",
		UntrackedPaths: []string{}, BeforeStatusDigest: "not_recorded", AfterStatusDigest: "not_recorded",
		BeforeContentDigest: "not_recorded", AfterContentDigest: "not_recorded", IgnoredExclusions: []string{},
		ChangedFiles: []ManifestChangedFile{}, ClaimedValidation: ClaimedValidation{Status: "not_run", Commands: []string{}, Provenance: "not_recorded", Limitations: "fixture"}}
	result, err := json.Marshal(manifest)
	must(err)
	if _, err = q.RecordResult(project, job, result, ""); err == nil || !strings.Contains(err.Error(), "retained terminal proof") {
		t.Fatalf("publication without proof: %v", err)
	}
	var count int
	must(q.store.db.QueryRow(`SELECT count(*) FROM project_result_associations`).Scan(&count))
	if count != 0 {
		t.Fatal("proof refusal did not roll back association")
	}
	check("finished", "agent_failed", true)
	_, err = q.store.db.Exec(`UPDATE jobs SET state='unknown',outcome='agent_unknown' WHERE id=?`, job)
	must(err)
	var historical string
	must(q.store.db.QueryRow(`SELECT outcome FROM completion_notifications WHERE job_id=?`, job).Scan(&historical))
	if historical != "agent_failed" {
		t.Fatalf("expected retained first-terminal outcome, got %q", historical)
	}
	check("unknown", "agent_unknown", false)

	// Supply the terminator's retained wire shape without claiming a live
	// callback. The publication and receipt projections below use real SQLite.
	proofPath := filepath.Join(root, "retained-proof.json")
	jb, err := q.store.get(job)
	must(err)
	metadata, err := json.Marshal(ownershipMetadata(jb))
	must(err)
	session := sessionInfo{ID: "ses_worker", ProjectID: "fixture", Agent: jb.agentRequest.Agent,
		Model: jb.agentRequest.Model, Metadata: metadata, Permissions: jb.agentRequest.Permissions}
	proof, err := json.Marshal(map[string]any{"completion_policy": agentCompletionPolicy, "prompt_id": jb.Agent.PromptID,
		"request_digest": jb.Digest, "session": session, "active": false, "stable_transcript": true})
	must(err)
	must(os.WriteFile(proofPath, proof, 0600))
	must(q.store.write(func(c *sql.Conn) error {
		if _, err := c.ExecContext(context.Background(), `UPDATE agent_ownership SET terminal_evidence=? WHERE job_id=?`, proofPath, job); err != nil {
			return err
		}
		_, err := c.ExecContext(context.Background(), `UPDATE jobs SET state='finished',outcome='agent_failed' WHERE id=?`, job)
		return err
	}))
	assoc, err := q.RecordResult(project, job, result, "")
	must(err)
	notification, err := q.store.acknowledge("completion-" + job)
	must(err)
	if notification.AcknowledgedAt == nil || notification.Outcome != historical {
		t.Fatalf("unchanged first-terminal completion event expected: %+v", notification)
	}
	retry, err := q.RecordResult(project, job, result, "")
	must(err)
	status, err := InspectProjectWork(dir, project, id)
	must(err)
	h := status.TaskLifecycle.Handoff
	must(q.store.db.QueryRow(`SELECT count(*) FROM project_inbox_messages WHERE kind='work.submitted'`).Scan(&count))
	if count != 1 || retry.ID != assoc.ID || h == nil || h.State != "pending_main" ||
		h.AssociationID != assoc.ID || h.ResultDigest != assoc.ManifestDigest || h.MainSessionID != "ses_fixture" ||
		h.MainRevision != 1 || !isValidInboxMessageID(h.InboxID) || status.TaskLifecycle.MainHandled != "unverified" ||
		status.TaskLifecycle.SessionArchived != "unverified" || !status.Job.TerminalProven {
		t.Fatalf("expected one pending pointer after reopen and exact retry: handoff=%+v status=%+v", h, status)
	}
	_, err = q.AcknowledgeMail(project, h.InboxID, true)
	must(err)
	status, err = InspectProjectWork(dir, project, id)
	must(err)
	if *status.TaskLifecycle.Handoff != *h || status.TaskLifecycle.MainHandled != "unverified" ||
		status.TaskLifecycle.SessionArchived != "unverified" {
		t.Fatalf("mail archive cannot acknowledge or archive native tasks: %+v", status.TaskLifecycle)
	}
	// The original pointer stays pinned when the binding changes; replacing
	// Main creates no message and is never a redrive.
	_, err = q.store.db.Exec(`INSERT INTO project_bindings SELECT project_id,2,repo_root,git_common_dir,commit_anchor,
 'ses_replacement',opencode_project_id,opencode_directory,handoff_manifest_digest,auth_ref,verified_at,recorded_at
 FROM project_bindings WHERE project_id=? AND revision=1`, project)
	must(err)
	status, err = InspectProjectWork(dir, project, id)
	must(err)
	held := status.TaskLifecycle.Handoff
	if held == nil || held.State != "dead_letter" || held.Reason != "main_binding_replaced" ||
		held.InboxID != h.InboxID || held.MainSessionID != h.MainSessionID || !status.Job.TerminalProven {
		t.Fatalf("replacement must hold the original handoff without changing job proof: %+v", status)
	}
}
