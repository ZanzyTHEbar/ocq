package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// One opt-in native/private-bridge/SQLite mail scenario. Stored project/lane
// metadata is seeded explicitly, not asserted as a live OpenCode identity.
// Real queued command receipts are never dispatched; no agent/server is needed.
func TestComposeOptionsNativeFixture(t *testing.T) {
	root := os.Getenv("OCQ_MAIL_FIXTURE_ROOT")
	if root == "" {
		t.Skip("requires explicitly owned native mail fixture")
	}
	info, err := os.Stat(root)
	if err != nil || !filepath.IsAbs(root) || info.Mode() != os.ModeDir|0700 {
		t.Fatal("private absolute fixture root required")
	}
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	encode := func(v any) []byte { t.Helper(); b, e := json.Marshal(v); must(e); return b }
	write := func(name string, b []byte) { t.Helper(); must(os.WriteFile(filepath.Join(root, name), b, 0600)) }
	load := func(name string) []byte {
		t.Helper()
		b, e := os.ReadFile(filepath.Join(root, name))
		must(e)
		return b
	}
	const project = "proj-mail-options"
	const lane = "lane-owner"
	state, repo := filepath.Join(root, "queue"), filepath.Join(root, "repo")
	phase := os.Getenv("OCQ_MAIL_FIXTURE_PHASE")
	if phase == "prepare" {
		if _, e := os.Lstat(state); !os.IsNotExist(e) {
			t.Fatal("fresh queue required; never retry uncertain fixture setup")
		}
		q, e := OpenQueue(state)
		must(e)
		defer q.Close()
		owner, e := q.SubmitCommand("mail-owner", repo, []string{"/bin/true"}, 0)
		must(e)
		recipient, e := q.SubmitCommand("mail-recipient", repo, []string{"/bin/true"}, 0)
		must(e)
		base, e := gitRead(repo, "rev-parse", "HEAD")
		must(e)
		handoff, e := parseManifestBytes(encode(handoffManifest{Version: 1, Kind: "handoff", ProjectID: project, Source: "operator_import", Intent: "PRIVATE_MAIL_ATTACHMENT café\nRetained fixture metadata only", Decisions: []string{}, Risks: []string{}}), "handoff", project)
		must(e)
		brief, e := parseManifestBytes(encode(briefManifest{Version: 1, Kind: "brief", ProjectID: project, Source: "operator_import", Goal: "Stored mail fixture only", Scope: []string{"."}, Exclusions: []string{}, AcceptanceCriteria: []string{"no execution"}, Base: base, Agent: "fixture-not-run", Model: agentModel{ID: "never-run", ProviderID: "fixture"}, Permissions: []permissionRule{{Action: "*", Resource: "*", Effect: "deny"}}, AllowedActions: []string{"read"}, ContextReferences: []string{}}), "brief", project)
		must(e)
		path := filepath.Join(root, "metadata-only-"+strings.Repeat("long-path-", 45)+"café\u202e\u200b")
		must(q.store.write(func(c *sql.Conn) error {
			ctx := context.Background()
			if _, e := c.ExecContext(ctx, "INSERT INTO projects(id,label,created_at) VALUES(?,?,?)", project, "Native mail options fixture", now()); e != nil {
				return e
			}
			if e := insertManifestConn(ctx, c, handoff, now()); e != nil {
				return e
			}
			if e := insertManifestConn(ctx, c, brief, now()); e != nil {
				return e
			}
			if _, e := c.ExecContext(ctx, `INSERT INTO project_bindings(project_id,revision,repo_root,git_common_dir,commit_anchor,elephant_session_id,opencode_project_id,opencode_directory,handoff_manifest_digest,auth_ref,verified_at,recorded_at) VALUES(?,1,?,?,?,?,?,?,?,?,?,?)`, project, repo, filepath.Join(repo, ".git"), base, "ses_mail_stored_fixture", "fixture-no-server", repo, handoff.Digest, "fixture-unused", "fixture_metadata_not_live_verified", now()); e != nil {
				return e
			}
			for n, job := range []Job{owner, recipient} {
				if _, e := c.ExecContext(ctx, "INSERT INTO project_jobs(job_id,project_id,binding_revision,role,brief_digest,recorded_at) VALUES(?,?,1,'implementation',?,?)", job.ID, project, brief.Digest, now()); e != nil {
					return e
				}
				id, p := lane, path
				if n == 1 {
					id, p = "lane-recipient", ""
				}
				if _, e := c.ExecContext(ctx, "INSERT INTO project_lanes(lane_id,project_id,owner_job_id,role,base_commit,fsfork_driver,fsfork_path,created_at) VALUES(?,?,?,'implementation',?,'git_worktree',?,?)", id, project, job.ID, base, p, now()); e != nil {
					return e
				}
			}
			return nil
		}))
		seed, e := q.SendMail(MailRequest{ProjectID: project, Key: "mail-reply-root", From: "lane:" + lane, To: "integrator:" + project, LaneID: lane, JobID: owner.ID, Kind: "lane.blocked", Subject: "Retained reply root", Body: "PRIVATE_SEED_REPLY", Manifest: handoff.Digest})
		must(e)
		assignment := MailRequest{ProjectID: project, From: "integrator:ses_mail_stored_fixture", To: "lane:lane-recipient", LaneID: lane, JobID: owner.ID, Kind: "lane.assigned", Subject: "PRIVATE_ASSIGN café\u202e", Body: "Assignment α\nExact body\u200b\n\x01", Manifest: handoff.Digest, ReplyTo: seed.ID, FsforkPath: ""}
		write("assignment.json", encode(assignment))
		write("fixture.json", encode(map[string]string{"project": project, "lane": lane, "owner_job": owner.ID, "recipient_job": recipient.ID, "manifest": handoff.Digest, "reply_root": seed.ID, "lane_path": path}))
		// Actual contract refusal, not malformed fixtures or mocked validators.
		valid := map[string]string{"ProjectID": project, "From": "lane:" + lane, "To": "integrator:" + project, "LaneID": lane, "JobID": owner.ID, "Kind": "lane.blocked", "Subject": "valid control", "Body": "", "Manifest": "", "ReplyTo": "", "FsforkPath": ""}
		if !validComposeRequest(encode(valid)) {
			t.Fatal("otherwise-valid private mail control rejected")
		}
		for _, kind := range []string{"work.submitted", "review.submitted"} {
			valid["Kind"] = kind
			if validComposeRequest(encode(valid)) {
				t.Fatal("automatic kind allowed through private preparation")
			}
			invalid := assignment
			invalid.Key = "automatic-" + kind
			invalid.Kind = kind
			if _, e := q.SendMail(invalid); e == nil {
				t.Fatal("automatic kind manually fabricated")
			}
		}
		invalid := assignment
		invalid.Key = "wrong-direction"
		invalid.From = "job:" + owner.ID
		if _, e := q.SendMail(invalid); e == nil {
			t.Fatal("invalid assignment direction accepted")
		}
		r, e := OpenReader(state)
		must(e)
		defer r.Close()
		stored, e := r.ComposeContext(project, lane, handoff.Digest, seed.ID)
		must(e)
		if stored.JobID != owner.ID || stored.LanePath != path || !reflect.DeepEqual(stored.RecipientLanes, []string{lane, "lane-recipient"}) {
			t.Fatal("stored route/path/link metadata mismatch")
		}
		if _, e := r.ComposeContext("proj-wrong-context", lane, handoff.Digest, seed.ID); e == nil {
			t.Fatal("wrong project context accepted")
		}
		t.Log("PASS real queued receipts/stored metadata, retained attachment/reply, full legal cross-lane assignment; wrong route/context and automatic kinds refused; no agent/server")
		return
	}
	if phase != "assignment" && phase != "verify" {
		t.Fatal("explicit prepare, assignment or verify phase required")
	}
	status, e := LocalComposeStatus(state)
	must(e)
	intent, e := ReadLocalComposeIntent(state, status.ID)
	must(e)
	var actual, expected MailRequest
	must(json.Unmarshal(intent.Request, &actual))
	file := "assignment.json"
	if phase == "verify" {
		file = "blocked.json"
	}
	must(json.Unmarshal(load(file), &expected))
	if !reflect.DeepEqual(actual, expected) {
		t.Fatal("native retained content/options differ from explicit fixture input")
	}
	r, e := OpenReader(state)
	must(e)
	message, e := r.MailByKey(project, intent.Key)
	must(e)
	var fixture map[string]string
	must(json.Unmarshal(load("fixture.json"), &fixture))
	path := expected.FsforkPath
	if path == "" {
		path = fixture["lane_path"]
	}
	record := composeRecord{Key: intent.Key}
	if !composeMailMatches(message, record, expected, path) {
		t.Fatal("exact mail fields, direction, owner, attachment/reply or path mismatch")
	}
	write(phase+"-message.json", encode(message))
	write(phase+"-intent.json", encode(intent))
	actual.Key = intent.Key
	sender, e := OpenExistingMailSender(state)
	must(e)
	again, e := sender.Send(actual)
	must(e)
	must(sender.Close())
	if !inboxMessageContentEqual(message, again) || again.ID != message.ID || again.Sequence != message.Sequence {
		t.Fatal("core same-key send was not idempotent")
	}
	if phase == "assignment" {
		ack, e := OpenExistingMailAcknowledger(state)
		must(e)
		read, e := ack.Acknowledge(project, message.ID, message.Sequence, false)
		must(e)
		readAgain, e := ack.Acknowledge(project, message.ID, message.Sequence, false)
		must(e)
		archived, e := ack.Acknowledge(project, message.ID, message.Sequence, true)
		must(e)
		archiveAgain, e := ack.Acknowledge(project, message.ID, message.Sequence, true)
		must(e)
		must(ack.Close())
		if !reflect.DeepEqual(read, readAgain) || !reflect.DeepEqual(archived, archiveAgain) || !inboxMessageContentEqual(message, archived) || archived.ReadAt == nil || archived.ArchivedAt == nil {
			t.Fatal("exact core acknowledgement not content-preserving/idempotent")
		}
		blocked := MailRequest{ProjectID: project, From: "job:" + fixture["owner_job"], To: "integrator:" + project, LaneID: lane, JobID: fixture["owner_job"], Kind: "lane.blocked", Subject: "PRIVATE_BLOCKED café\u200b", Body: "Blocked β\nExact reply\u202e\u200b\n\x01", Manifest: fixture["manifest"], ReplyTo: message.ID, FsforkPath: "lane default"}
		write("blocked.json", encode(blocked))
	}
	expectedCount := 2
	if phase == "verify" {
		expectedCount = 3
	}
	for _, table := range []string{"agent_operations", "completion_notifications", "agent_ownership"} {
		var n int
		must(r.store.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n))
		if n != 0 {
			t.Fatal("unexpected execution/agent side effect")
		}
	}
	var count, queued int
	must(r.store.db.QueryRow("SELECT count(*) FROM project_inbox_messages").Scan(&count))
	must(r.store.db.QueryRow("SELECT count(*) FROM jobs WHERE state='queued' AND validation='not_run'").Scan(&queued))
	if count != expectedCount || queued != 2 {
		t.Fatal("unexpected mail duplicate or job transition")
	}
	must(r.Close())
	t.Logf("PASS exact native %s content/options/path, core same-key idempotency, retained links; mail rows=%d, queued jobs unchanged, zero agents/operations", phase, count)
}
