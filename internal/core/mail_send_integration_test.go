package core_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ZanzyTHEbar/faults-go"
	_ "modernc.org/sqlite"

	"ocq/internal/core"
)

// Exercise the narrowly scoped writer and read-only lookup against a real
// private queue, without a dispatcher, agent, or external service.
func TestExistingMailSendAndLookup(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(filepath.Join(home, ".local", "state"), "ocq-mail-send-acceptance-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	missing := filepath.Join(root, "missing")
	if sender, err := core.OpenExistingMailSender(missing); err == nil {
		sender.Close()
		t.Fatal("missing queue accepted")
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatalf("open created missing state: %v", err)
	}

	old := filepath.Join(root, "old")
	if err := os.Mkdir(old, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "schema.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	oldDB, err := sql.Open("sqlite", "file:"+filepath.Join(old, "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer oldDB.Close()
	schema, err := os.ReadFile(filepath.Join("sql", "schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oldDB.Exec("PRAGMA journal_mode=WAL; " + string(schema)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(old, "queue.db"), 0600); err != nil {
		t.Fatal(err)
	}
	if sender, err := core.OpenExistingMailSender(old); err == nil {
		sender.Close()
		t.Fatal("old schema accepted")
	}
	var version, goose int
	if err := oldDB.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := oldDB.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='goose_db_version'").Scan(&goose); err != nil || version != 1 || goose != 0 {
		t.Fatalf("old queue was initialized or migrated: version=%d goose=%d err=%v", version, goose, err)
	}

	state := filepath.Join(root, "state")
	queue, err := core.OpenQueue(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(state, "queue.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
INSERT INTO receipts(key,job_id,canonical,digest,accepted_at) VALUES('fixture-job','job-test',x'7b7d','fixture','time');
INSERT INTO jobs(id,state,reason) VALUES('job-test','unknown','ownership uncertain');
INSERT INTO projects(id,label,created_at) VALUES('proj-test','fixture','time'),('proj-other','other','time');
INSERT INTO project_manifests(digest,project_id,version,kind,canonical,recorded_at) VALUES('brief','proj-test',1,'brief',x'7b7d','time');
INSERT INTO project_bindings(project_id,revision,repo_root,git_common_dir,commit_anchor,elephant_session_id,opencode_project_id,opencode_directory,handoff_manifest_digest,auth_ref,verified_at,recorded_at)
 VALUES('proj-test',1,'/','/','base','ses','project','/','brief','auth','time','time');
INSERT INTO project_jobs(job_id,project_id,binding_revision,role,brief_digest,recorded_at) VALUES('job-test','proj-test',1,'implementation','brief','time');
INSERT INTO project_lanes(lane_id,project_id,owner_job_id,role,base_commit,fsfork_driver,fsfork_path,created_at) VALUES('lane-test','proj-test','job-test','implementation','base','git_worktree','/fixture','time');`)
	if err != nil {
		t.Fatal(err)
	}
	sender, err := core.OpenExistingMailSender(state)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := core.OpenReader(state)
	if err != nil {
		sender.Close()
		t.Fatal(err)
	}
	defer reader.Close()
	defer func() {
		if sender != nil {
			_ = sender.Close()
		}
	}()
	if _, err := reader.MailByKey("proj-test", "fixture-mail"); !faults.IsCode(err, faults.CodeNotFound) {
		t.Fatalf("missing key was not not-found: %v", err)
	}
	req := core.MailRequest{ProjectID: "proj-test", Key: "fixture-mail", LaneID: "lane-test", JobID: "job-test", From: "lane:lane-test", To: "integrator:proj-test", Kind: "lane.blocked", Subject: "fixture subject", Body: "fixture body"}
	first, err := sender.Send(req)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := reader.MailByKey(req.ProjectID, req.Key)
	if err != nil || !reflect.DeepEqual(first, stored) {
		t.Fatalf("exact retained read: %+v, %v", stored, err)
	}
	if _, err := reader.MailByKey("proj-other", req.Key); !faults.IsCode(err, faults.CodeNotFound) {
		t.Fatalf("wrong project resolved key: %v", err)
	}
	again, err := sender.Send(req)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatalf("identical request did not recover original receipt: %+v, %v", again, err)
	}
	conflict := req
	conflict.Body = "different content"
	if _, err := sender.Send(conflict); !faults.IsCode(err, faults.CodeValidationFailed) {
		t.Fatalf("key conflict was not rejected: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version=4"); err != nil {
		t.Fatal(err)
	}
	if _, err := sender.Send(req); !faults.IsCode(err, faults.CodeConfigInvalid) {
		t.Fatalf("writer did not reject changed schema: %v", err)
	}
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 4 {
		t.Fatalf("writer migrated changed schema: version=%d err=%v", version, err)
	}
	if _, err := db.Exec("PRAGMA user_version=5"); err != nil {
		t.Fatal(err)
	}
	if err := sender.Close(); err != nil {
		t.Fatal(err)
	}
	sender = nil
	sender, err = core.OpenExistingMailSender(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.Close(); err != nil {
		t.Fatal(err)
	}
	sender = nil
	var count int
	var jobState, reason string
	var readAt, archivedAt sql.NullString
	if err := db.QueryRow("SELECT count(*) FROM project_inbox_messages WHERE key=?", req.Key).Scan(&count); err != nil || count != 1 {
		t.Fatalf("send/reopen created duplicate mail: count=%d err=%v", count, err)
	}
	if err := db.QueryRow("SELECT state,reason FROM jobs WHERE id='job-test'").Scan(&jobState, &reason); err != nil || jobState != "unknown" || reason != "ownership uncertain" {
		t.Fatalf("mail changed unknown ownership: state=%q reason=%q err=%v", jobState, reason, err)
	}
	if err := db.QueryRow("SELECT read_at,archived_at FROM project_inbox_messages WHERE id=?", first.ID).Scan(&readAt, &archivedAt); err != nil || readAt.Valid || archivedAt.Valid {
		t.Fatalf("lookup acknowledged mail: read=%v archived=%v err=%v", readAt, archivedAt, err)
	}
}
