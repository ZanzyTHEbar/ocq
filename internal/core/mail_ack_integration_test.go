package core_test

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"ocq/internal/core"

	_ "modernc.org/sqlite"
)

// Exercises the real queue, SQLite and non-initializing mail API without an
// agent or external service. The directory is private persistent local state.
func TestExistingMailAcknowledgement(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(filepath.Join(home, ".local", "state"), "ocq-mail-acceptance-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	missing := filepath.Join(root, "missing")
	if a, err := core.OpenExistingMailAcknowledger(missing); err == nil {
		a.Close()
		t.Fatal("missing state was accepted")
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatalf("non-initializing open created state: %v", err)
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
	initialSchema, err := os.ReadFile(filepath.Join("sql", "schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oldDB.Exec("PRAGMA journal_mode=WAL; " + string(initialSchema) + " CREATE TABLE completion_notifications(x)"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(old, "queue.db"), 0600); err != nil {
		t.Fatal(err)
	}
	if a, err := core.OpenExistingMailAcknowledger(old); err == nil {
		a.Close()
		t.Fatal("old schema was accepted")
	}
	var version, gooseTables int
	if err := oldDB.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := oldDB.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='goose_db_version'").Scan(&gooseTables); err != nil || version != 1 || gooseTables != 0 {
		t.Fatalf("old schema changed: version=%d gooseTables=%d err=%v", version, gooseTables, err)
	}
	if q, err := core.OpenQueue(old); err == nil {
		q.Close()
		t.Fatal("conflicting notification table did not fail migration")
	}
	var agentTables int
	if err := oldDB.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := oldDB.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='agent_ownership'").Scan(&agentTables); err != nil || version != 1 || agentTables != 0 {
		t.Fatalf("migration did not roll back: version=%d agentTables=%d err=%v", version, agentTables, err)
	}
	if err := oldDB.QueryRow("SELECT count(*) FROM goose_db_version WHERE version_id=0").Scan(&gooseTables); err != nil || gooseTables != 1 {
		t.Fatalf("retry-safe Goose metadata missing: rows=%d err=%v", gooseTables, err)
	}
	if _, err := oldDB.Exec("DROP TABLE completion_notifications"); err != nil {
		t.Fatal(err)
	}
	migrated, err := core.OpenQueue(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrated.Close(); err != nil {
		t.Fatal(err)
	}
	if err := oldDB.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 5 {
		t.Fatalf("migration retry failed: version=%d err=%v", version, err)
	}
	if err := oldDB.Close(); err != nil {
		t.Fatal(err)
	}
	preGoose := filepath.Join(root, "pre-goose")
	if err := os.Mkdir(preGoose, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(preGoose, "schema.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	preDB, err := sql.Open("sqlite", "file:"+filepath.Join(preGoose, "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer preDB.Close()
	if _, err := preDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"schema.sql", "schema2.sql", "schema3.sql", "schema4.sql", "schema5.sql"} {
		data, err := os.ReadFile(filepath.Join("sql", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := preDB.Exec(string(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(preGoose, "queue.db"), 0600); err != nil {
		t.Fatal(err)
	}
	preAcker, err := core.OpenExistingMailAcknowledger(preGoose)
	if err != nil {
		t.Fatal(err)
	}
	if err := preAcker.Close(); err != nil {
		t.Fatal(err)
	}
	if err := preDB.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='goose_db_version'").Scan(&gooseTables); err != nil || gooseTables != 0 {
		t.Fatalf("existing schema-5 open initialized Goose metadata: rows=%d err=%v", gooseTables, err)
	}
	if err := preDB.Close(); err != nil {
		t.Fatal(err)
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
INSERT INTO receipts(key,job_id,canonical,digest,accepted_at) VALUES('key','job-test',CAST('{"version":2,"kind":"agent","repo":"/","base":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","prompt":"fixture","provision_policy":"workmux-headless-no-hooks-no-file-ops-v1","memory_bytes":0}' AS BLOB),'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','time');
INSERT INTO jobs(id,state) VALUES('job-test','queued');
INSERT INTO agent_ownership(job_id,session_id,prompt_id,token,worktree) VALUES('job-test','ses-test','msg-test','owner-test','/fixture');
INSERT INTO projects(id,label,created_at) VALUES('proj-test','label','time');
INSERT INTO project_manifests(digest,project_id,version,kind,canonical,recorded_at) VALUES('brief','proj-test',1,'brief',x'7b7d','time');
INSERT INTO project_bindings(project_id,revision,repo_root,git_common_dir,commit_anchor,elephant_session_id,opencode_project_id,opencode_directory,handoff_manifest_digest,auth_ref,verified_at,recorded_at)
 VALUES('proj-test',1,'/','/','base','ses','project','/','brief','auth','time','time');
INSERT INTO project_jobs(job_id,project_id,binding_revision,role,brief_digest,recorded_at) VALUES('job-test','proj-test',1,'implementation','brief','time');
INSERT INTO project_lanes(lane_id,project_id,owner_job_id,role,base_commit,fsfork_driver,created_at) VALUES('lane-test','proj-test','job-test','implementation','base','git_worktree','time');
INSERT INTO project_inbox_messages(sequence,id,key,project_id,sender,recipient,lane_id,job_id,kind,subject,body,created_at)
 VALUES(41,'msg-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','mail','proj-test','lane:lane-test','integrator:proj-test','lane-test','job-test','lane.blocked','subject','body','time'),
 (42,'msg-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','mail-2','proj-test','lane:lane-test','integrator:proj-test','lane-test','job-test','lane.blocked','subject','body','time');`)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := core.OpenReader(state)
	if err != nil {
		t.Fatal(err)
	}
	page, err := reader.Inbox("proj-test", "all", true, 0, 20)
	if err != nil || len(page.Messages) != 2 || page.Messages[0].Sequence != 41 || page.UnreadCount != 2 {
		t.Fatalf("unread inbox projection: %+v, %v", page, err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	a, err := core.OpenExistingMailAcknowledger(state)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	const id = "msg-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := a.Acknowledge("proj-test", id, 42, false); err == nil {
		t.Fatal("wrong message sequence was accepted")
	}
	if _, err := a.Acknowledge("proj-test", id, 41, true); err == nil {
		t.Fatal("unread message was archived")
	}
	var read, archived sql.NullString
	if err := db.QueryRow("SELECT read_at,archived_at FROM project_inbox_messages WHERE id=?", id).Scan(&read, &archived); err != nil || read.Valid || archived.Valid {
		t.Fatalf("failed acknowledgement changed message: read=%v archived=%v err=%v", read, archived, err)
	}
	first, err := a.Acknowledge("proj-test", id, 41, false)
	if err != nil || first.ReadAt == nil || first.ArchivedAt != nil {
		t.Fatalf("read acknowledgement: %+v, %v", first, err)
	}
	second, err := a.Acknowledge("proj-test", id, 41, true)
	if err != nil || second.ReadAt == nil || *second.ReadAt != *first.ReadAt || second.ArchivedAt == nil {
		t.Fatalf("archive acknowledgement: %+v, %v", second, err)
	}
	third, err := a.Acknowledge("proj-test", id, 41, true)
	if err != nil || third.ReadAt == nil || third.ArchivedAt == nil || *third.ReadAt != *second.ReadAt || *third.ArchivedAt != *second.ArchivedAt {
		t.Fatalf("idempotent acknowledgement: %+v, %v", third, err)
	}
	if _, err := db.Exec("PRAGMA user_version=4"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Acknowledge("proj-test", id, 41, false); err == nil {
		t.Fatal("acknowledgement accepted a changed schema version")
	}
	if _, err := db.Exec("PRAGMA user_version=5"); err != nil {
		t.Fatal(err)
	}
	queue, err = core.OpenQueue(state)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := queue.AcknowledgeMail("proj-test", "msg-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", true)
	if err != nil || legacy.ReadAt == nil || legacy.ArchivedAt == nil {
		queue.Close()
		t.Fatalf("CLI acknowledgement must still read and archive together: %+v, %v", legacy, err)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}
	queue, err = core.OpenQueue(state)
	if err != nil {
		t.Fatal(err)
	}
	manifest := core.ResultManifest{Version: 1, Kind: "result", ProjectID: "proj-test", Source: "author_import",
		JobID: "job-test", RequestDigest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Base: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Worktree: "/fixture", StagedStatus: "clean", UnstagedStatus: "clean",
		UntrackedPaths: []string{}, BeforeStatusDigest: "not_recorded", AfterStatusDigest: "not_recorded",
		BeforeContentDigest: "not_recorded", AfterContentDigest: "not_recorded", IgnoredExclusions: []string{},
		ChangedFiles: []core.ManifestChangedFile{}, ClaimedValidation: core.ClaimedValidation{Status: "not_run", Commands: []string{}, Provenance: "not_recorded", Limitations: "fixture"}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	association, err := queue.RecordResult("proj-test", "job-test", raw, "")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := queue.RecordResult("proj-test", "job-test", raw, "")
	if err != nil || retry.ID != association.ID || retry.Sequence != association.Sequence {
		t.Fatalf("result retry: first=%+v retry=%+v err=%v", association, retry, err)
	}
	manifest.InlineOutput = "later result"
	laterRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	later, err := queue.RecordResult("proj-test", "job-test", laterRaw, association.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.RecordResult("proj-test", "job-test", raw, ""); err != nil {
		t.Fatalf("late retry of earlier result: %v", err)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}
	var associations, mailed int
	var sealed, lanePath string
	if err := db.QueryRow("SELECT sealed_manifest_digest,fsfork_path FROM project_lanes WHERE project_id='proj-test'").Scan(&sealed, &lanePath); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM project_result_associations WHERE project_id='proj-test'").Scan(&associations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM project_inbox_messages WHERE kind='work.submitted'").Scan(&mailed); err != nil {
		t.Fatal(err)
	}
	if associations != 2 || mailed != 2 || sealed != later.ManifestDigest || lanePath != "/fixture" {
		t.Fatalf("result publication: associations=%d mail=%d sealed=%q path=%q", associations, mailed, sealed, lanePath)
	}
	reader, err = core.OpenReader(state)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	projects, err := reader.Projects()
	if err != nil || len(projects.Projects) != 1 || projects.Projects[0].BindingRevision != 1 {
		t.Fatalf("project list: %+v, %v", projects, err)
	}
	detail, err := reader.Job("proj-test", "job-test")
	if err != nil || len(detail.Associations) != 2 || detail.SealedManifestDigest == nil || *detail.SealedManifestDigest != later.ManifestDigest {
		t.Fatalf("project job projection: %+v, %v", detail, err)
	}
}
