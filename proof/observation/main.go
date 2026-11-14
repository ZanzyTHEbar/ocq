// Real private queue/CLI/SQLite observation acceptance. No agent, server or UI.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
	"ocq/internal/core"
)

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func check(ok bool, message string) {
	if !ok {
		panic(message)
	}
}
func raw(v any) []byte       { b, e := json.Marshal(v); must(e); return b }
func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

type fixture struct {
	ctx                               context.Context
	old, current, bridge, root, state string
	db                                *sql.DB
	job, event, path                  string
}

func (f *fixture) command(bin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, bin, args...)
	c.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "HOME=" + os.Getenv("HOME"), "OCQ_STATE_DIR=" + f.state}
	var stderr bytes.Buffer
	c.Stderr = &stderr
	b, e := c.Output()
	if e != nil {
		return b, fmt.Errorf("%v: %w: %s", args, e, stderr.String())
	}
	return b, nil
}
func (f *fixture) cli(bin string, args ...string) []byte {
	b, e := f.command(bin, append([]string{"--state-dir", f.state}, args...)...)
	must(e)
	return b
}
func (f *fixture) head() core.ObservationSnapshot {
	h, e := core.ObserveHead(f.ctx, f.state)
	must(e)
	return h
}
func (f *fixture) snapshot(selectors ...core.ObservationSelector) core.ObservationSnapshot {
	h := f.head()
	s, e := core.ObserveSnapshot(f.ctx, f.state, core.ObservationRequest{Protocol: core.ObservationProtocol, QueueIdentity: h.QueueIdentity, StoreContext: h.StoreContext, Selectors: selectors})
	must(e)
	return s
}
func (f *fixture) waitRequest() core.ObservationWaitRequest {
	h := f.head()
	return core.ObservationWaitRequest{Protocol: core.ObservationProtocol, QueueIdentity: h.QueueIdentity, StoreContext: h.StoreContext, Revision: h.Revision, WaitSeconds: 2}
}
func (f *fixture) revision() string { return f.head().Revision }
func (f *fixture) wait(req core.ObservationWaitRequest, action func()) core.ObservationWaitOutput {
	var out core.ObservationWaitOutput
	var err error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); out, err = core.ObserveWait(f.ctx, f.state, req) }()
	time.Sleep(80 * time.Millisecond)
	action()
	wg.Wait()
	must(err)
	return out
}
func (f *fixture) log(c core.ObservationLogCursor) core.ObservationLogOutput {
	h := f.head()
	s, e := core.ObserveSnapshot(f.ctx, f.state, core.ObservationRequest{Protocol: core.ObservationProtocol, QueueIdentity: h.QueueIdentity, StoreContext: h.StoreContext, Log: &c})
	must(e)
	check(s.Status == "ready" && s.Log != nil, "log snapshot unavailable")
	return *s.Log
}
func (f *fixture) frozen() []byte {
	var canonical []byte
	var event, mail string
	must(f.db.QueryRow("SELECT canonical FROM receipts WHERE job_id=?", f.job).Scan(&canonical))
	must(f.db.QueryRow("SELECT json_array(id,job_id,state,outcome,exit_code,validation,origin,recorded_at,acknowledged_at) FROM completion_notifications WHERE job_id=?", f.job).Scan(&event))
	must(f.db.QueryRow("SELECT json_array(sequence,id,key,project_id,sender,recipient,lane_id,job_id,kind,subject,body,created_at,read_at,archived_at) FROM project_inbox_messages").Scan(&mail))
	journal, e := os.ReadFile(filepath.Join(f.state, "retained-fixture-journal", "intent.json"))
	must(e)
	return raw([]any{canonical, event, mail, journal})
}
func (f *fixture) version() int {
	var version int
	must(f.db.QueryRow("PRAGMA user_version").Scan(&version))
	return version
}
func (f *fixture) seed() {
	var receipt struct {
		ID string `json:"id"`
	}
	must(json.Unmarshal(f.cli(f.old, "submit", "--key", "observation-seed", "--cwd", f.root, "--", "/usr/bin/printf", "seed-output"), &receipt))
	f.job = receipt.ID
	f.event = "completion-" + f.job
	f.db = func() *sql.DB {
		db, e := sql.Open("sqlite", "file:"+filepath.Join(f.state, "queue.db")+"?_txlock=immediate&_pragma=busy_timeout(1000)&_pragma=synchronous(FULL)&_pragma=foreign_keys(1)")
		must(e)
		db.SetMaxOpenConns(1)
		return db
	}()
	check(f.version() == 5, "pinned old executable did not create schema 5")
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, f.old, "--state-dir", f.state, "dispatch", "--commands-only", "--slots", "1", "--memory-floor-bytes", "0")
	c.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "HOME=" + os.Getenv("HOME")}
	output, e := os.OpenFile(filepath.Join(f.root, "dispatcher.log"), os.O_CREATE|os.O_WRONLY, 0600)
	must(e)
	defer output.Close()
	c.Stdout, c.Stderr = output, output
	must(c.Start())
	reaped := false
	defer func() {
		if !reaped {
			c.Process.Signal(syscall.SIGTERM)
			_ = c.Wait()
		}
	}()
	deadline := time.Now().Add(6 * time.Second)
	for {
		var state string
		must(f.db.QueryRow("SELECT state,stdout FROM jobs WHERE id=?", f.job).Scan(&state, &f.path))
		if state == "finished" {
			break
		}
		check(time.Now().Before(deadline), "seed command did not finish")
		time.Sleep(20 * time.Millisecond)
	}
	must(c.Process.Signal(syscall.SIGTERM))
	must(c.Wait())
	reaped = true
	manifest := []byte(`{"version":1,"kind":"handoff","project_id":"proj-observe","source":"operator_import","observed_at":null,"intent":"observation acceptance fixture","decisions":[],"risks":[]}`)
	md := digest(manifest)
	// Retained mail/control-plane fixture uses the real SQLite contract; it does
	// not claim live registration, agent execution or imported validation.
	tx, e := f.db.Begin()
	must(e)
	defer tx.Rollback()
	insert := func(query string, args ...any) { _, e := tx.Exec(query, args...); must(e) }
	insert("INSERT INTO projects VALUES('proj-observe','Observation','time')")
	insert("INSERT INTO project_manifests VALUES(?,'proj-observe',1,'handoff',?,'time')", md, manifest)
	insert("INSERT INTO project_bindings VALUES('proj-observe',1,?,?,'base','ses-fixture','project-fixture',?,?,'fixture-auth-ref','time','time')", f.root, f.root, f.root, md)
	insert("INSERT INTO project_jobs VALUES(?,'proj-observe',1,'implementation',?,NULL,NULL,'time')", f.job, md)
	insert("INSERT INTO project_lanes VALUES('lane-observe','proj-observe',?,'implementation','base','git_worktree','',NULL,'time',NULL)", f.job)
	insert(`INSERT INTO project_inbox_messages(id,key,project_id,sender,recipient,lane_id,job_id,kind,subject,body,created_at)
 VALUES('msg-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','fixture-mail','proj-observe','lane:lane-observe','integrator:proj-observe','lane-observe',?,'lane.blocked','Fixture','Retained content','time')`, f.job)
	must(tx.Commit())
	must(os.Mkdir(filepath.Join(f.state, "retained-fixture-journal"), 0700))
	must(os.WriteFile(filepath.Join(f.state, "retained-fixture-journal", "intent.json"), []byte("{\"fixture\":true,\"immutable_bytes\":\"retain exactly\"}\n"), 0600))
}
func (f *fixture) run() {
	f.seed()
	defer f.db.Close()
	frozen := f.frozen()
	must(os.WriteFile(filepath.Join(f.root, "before-migration.json"), frozen, 0600))
	h := f.head()
	check(h.Status == "upgrade_required" && h.Schema == 5, "read-only head did not refuse schema 5")
	check(bytes.Equal(frozen, f.frozen()) && f.version() == 5, "read-only head mutated schema 5")
	lock, e := os.OpenFile(filepath.Join(f.state, "dispatch.lock"), os.O_RDWR, 0600)
	must(e)
	must(syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
	_, e = f.command(f.current, "--state-dir", f.state, "list")
	check(e != nil && f.version() == 5, "migration ran with dispatch lock held")
	var applied int
	must(f.db.QueryRow("SELECT count(*) FROM goose_db_version WHERE version_id=2 AND is_applied=1").Scan(&applied))
	check(applied == 0, "failed migration recorded applied version")
	must(syscall.Flock(int(lock.Fd()), syscall.LOCK_UN))
	must(lock.Close())
	// Goose v3.28.0 provider_run.go runs RunTx first, then inserts the
	// applied version on that SAME transaction. Its SQLite store writes:
	// INSERT INTO goose_db_version (version_id,is_applied) VALUES (?,?).
	// Fail that real ledger insert only after all schema-6 objects and the
	// user_version change are visible inside the migration transaction. This
	// exercises rollback AFTER DDL, unlike the dispatch-lock refusal above.
	must(f.db.QueryRow("SELECT count(*) FROM goose_db_version WHERE version_id=1 AND is_applied=1").Scan(&applied))
	check(applied == 1, "pinned schema-5 fixture lacks applied Goose migration 1")
	_, e = f.db.Exec(`CREATE TRIGGER proof_fail_observation_migration2_ledger
 BEFORE INSERT ON goose_db_version
 WHEN NEW.version_id=2 AND NEW.is_applied=1
 BEGIN
 SELECT CASE WHEN
  (SELECT user_version FROM pragma_user_version)=6 AND
  (SELECT count(*) FROM sqlite_master WHERE
   (type='table' AND name='observation_revision') OR
   (type='trigger' AND (name GLOB 'observe_*' OR name='observation_revision_retained')))=22 AND
  (SELECT revision FROM observation_revision WHERE singleton=1)=0
 THEN RAISE(ABORT,'ocq-proof-migration2-after-ddl')
 ELSE RAISE(ABORT,'ocq-proof-migration2-before-ddl') END;
 END`)
	must(e)
	faultInstalled := true
	defer func() {
		if faultInstalled {
			if _, cleanupErr := f.db.Exec("DROP TRIGGER proof_fail_observation_migration2_ledger"); cleanupErr != nil {
				fmt.Fprintln(os.Stderr, "fixture fault-trigger cleanup failed:", cleanupErr)
			}
		}
	}()
	_, e = f.command(f.current, "--state-dir", f.state, "list")
	check(e != nil && strings.Contains(e.Error(), "ocq-proof-migration2-after-ddl"), "migration did not fail at the real post-DDL ledger insert")
	must(os.WriteFile(filepath.Join(f.root, "migration-failure.txt"), []byte(e.Error()+"\n"), 0600))
	check(f.version() == 5, "failed migration leaked schema version 6")
	var observationObjects int
	must(f.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE
 (type='table' AND name='observation_revision') OR
 (type='trigger' AND (name GLOB 'observe_*' OR name='observation_revision_retained'))`).Scan(&observationObjects))
	check(observationObjects == 0, "failed migration leaked observation table/triggers")
	must(f.db.QueryRow("SELECT count(*) FROM goose_db_version WHERE version_id=2").Scan(&applied))
	check(applied == 0, "failed post-DDL migration retained a Goose version-2 record")
	failedVersionRows := applied
	must(f.db.QueryRow("SELECT count(*) FROM goose_db_version WHERE version_id=1 AND is_applied=1").Scan(&applied))
	check(applied == 1, "failed migration changed historical Goose migration 1")
	check(bytes.Equal(frozen, f.frozen()), "failed migration changed retained receipt/event/mail/journal bytes")
	must(os.WriteFile(filepath.Join(f.root, "after-failed-migration.json"), f.frozen(), 0600))
	reader, e := core.OpenReader(f.state)
	must(e)
	retainedJob, jobErr := reader.QueueJob(f.job)
	retainedMail, mailErr := reader.Inbox("proj-observe", "all", false, 0, 20)
	must(reader.Close())
	must(jobErr)
	must(mailErr)
	check(retainedJob.Job.ID == f.job && len(retainedMail.Messages) == 1 && retainedMail.UnreadCount == 1, "existing schema-5 read-only views failed after migration rollback")
	h = f.head()
	check(h.Status == "upgrade_required" && h.Schema == 5 && bytes.Equal(frozen, f.frozen()), "post-rollback read-only observation changed schema-5 state")
	must(os.WriteFile(filepath.Join(f.root, "migration-rollback.json"), raw(map[string]any{
		"schema": f.version(), "observation_objects": observationObjects,
		"goose_2_rows": failedVersionRows, "goose_1_applied_rows": applied,
		"before_sha256": digest(frozen), "after_sha256": digest(f.frozen()),
		"read_only_job_id": retainedJob.Job.ID, "read_only_mail_count": len(retainedMail.Messages),
		"read_only_unread_count": retainedMail.UnreadCount, "observation_status": h.Status,
	}), 0600))
	// Remove ONLY this fixture fault, then explicitly retry the administrative
	// migration once after rollback has been established. Never retry execution.
	faultInstalled = false // The explicit removal below is attempted only once.
	_, e = f.db.Exec("DROP TRIGGER proof_fail_observation_migration2_ledger")
	must(e)
	f.cli(f.current, "list")
	check(f.version() == 6, "migration did not reach schema 6")
	must(f.db.QueryRow("SELECT count(*) FROM goose_db_version WHERE version_id IN(1,2) AND is_applied=1").Scan(&applied))
	check(applied == 2, "Goose migration records missing")
	check(bytes.Equal(frozen, f.frozen()), "migration changed immutable receipts/events/mail/journal")
	must(os.WriteFile(filepath.Join(f.root, "after-migration.json"), f.frozen(), 0600))
	before := f.revision()
	f.cli(f.current, "ack", f.event)
	check(f.revision() != before, "real acknowledgement did not advance revision")
	before = f.revision()
	f.cli(f.current, "ack", f.event)
	check(f.revision() == before, "idempotent ack churned revision")
	tx, e := f.db.Begin()
	must(e)
	_, e = tx.Exec("UPDATE jobs SET reason='rolled back' WHERE id=?", f.job)
	must(e)
	must(tx.Rollback())
	check(f.revision() == before, "rollback leaked revision")
	var reason string
	must(f.db.QueryRow("SELECT reason FROM jobs WHERE id=?", f.job).Scan(&reason))
	check(reason != "rolled back", "rollback leaked row")
	// The row embeds the revision that its AFTER UPDATE trigger will commit.
	write := func() {
		_, e := f.db.Exec("UPDATE jobs SET reason='r:'||(SELECT revision+1 FROM observation_revision WHERE singleton=1) WHERE id=?", f.job)
		must(e)
	}
	write()
	ctx, cancel := context.WithCancel(f.ctx)
	writerDone := make(chan error, 1)
	go func() {
		for i := 0; i < 40; i++ {
			select {
			case <-ctx.Done():
				writerDone <- nil
				return
			default:
			}
			_, e := f.db.Exec("UPDATE jobs SET reason='r:'||(SELECT revision+1 FROM observation_revision WHERE singleton=1) WHERE id=?", f.job)
			if e != nil {
				writerDone <- e
				return
			}
			time.Sleep(time.Millisecond)
		}
		writerDone <- nil
	}()
	for i := 0; i < 20; i++ {
		s := f.snapshot(core.ObservationSelector{Slot: "job", Kind: "queue-job", JobID: f.job})
		check(s.Status == "ready" && len(s.Slots) == 1, "coherent snapshot unavailable")
		j := s.Slots[0].Data.(core.QueueJobSummary)
		check(j.Reason == "r:"+s.Revision, "mixed row/revision snapshot")
	}
	cancel()
	must(<-writerDone)
	selectors := []core.ObservationSelector{{Slot: "projects", Kind: "projects", Cursor: "0"}, {Slot: "board", Kind: "board", ProjectID: "proj-observe"}, {Slot: "job", Kind: "job", ProjectID: "proj-observe", JobID: f.job}, {Slot: "inbox", Kind: "inbox", ProjectID: "proj-observe", Cursor: "0"}, {Slot: "results", Kind: "results", ProjectID: "proj-observe", Cursor: "0"}, {Slot: "lanes", Kind: "lanes", ProjectID: "proj-observe", Cursor: "0"}, {Slot: "mail", Kind: "mail", ProjectID: "proj-observe", MessageID: "msg-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Sequence: "1"}, {Slot: "event", Kind: "notification", EventID: f.event}}
	s := f.snapshot(selectors...)
	check(s.Status == "ready" && len(s.Slots) == 8, "selected project projections failed")
	for _, slot := range s.Slots {
		check(slot.Status == "ready", "selected slot unavailable: "+slot.Kind)
	}
	var md string
	must(f.db.QueryRow("SELECT digest FROM project_manifests LIMIT 1").Scan(&md))
	s = f.snapshot(core.ObservationSelector{Slot: "manifest", Kind: "manifest", ProjectID: "proj-observe", Digest: md}, core.ObservationSelector{Slot: "queue", Kind: "queue-jobs", Cursor: "0"}, core.ObservationSelector{Slot: "notifications", Kind: "notifications", Cursor: "0"})
	check(s.Status == "ready", "manifest/queue projections failed")
	// Checkpoint while the foreground wait is idle: no reader transaction pins WAL.
	req := f.waitRequest()
	out := f.wait(req, func() {
		var busy, log, done int
		must(f.db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &done))
		check(busy == 0, "idle watch pinned SQLite reader")
	})
	check(out.Status == "unchanged", "checkpoint falsely invalidated projections")
	req = f.waitRequest()
	out = f.wait(req, write)
	check(out.Status == "changed" && out.DatabaseChanged, "commit hint did not wake wait")
	out, e = core.ObserveWait(f.ctx, f.state, req)
	must(e)
	check(out.Status == "changed", "arm/check gap lost committed revision")
	// One selected real retained output stream. EOF never proves completion:
	// appending after EOF must still wake without any database revision change.
	c := core.ObservationLogCursor{JobID: f.job, Stream: "stdout", Offset: "0", ObservedSize: "0", ModifiedNS: "0"}
	page := f.log(c)
	check(page.Status == "ready" && page.EOF && page.NonAtomicWithDatabase, "initial selected log read failed")
	c = page.Cursor
	req = f.waitRequest()
	req.Log = &c
	before = f.revision()
	out = f.wait(req, func() {
		file, e := os.OpenFile(f.path, os.O_WRONLY|os.O_APPEND, 0600)
		must(e)
		_, e = file.Write(bytes.Repeat([]byte("x"), 40000))
		must(e)
		must(file.Close())
	})
	check(out.Status == "changed" && out.LogChanged && !out.DatabaseChanged && f.revision() == before, "log append did not independently wake")
	page = f.log(c)
	check(page.Status == "ready" && len(page.Bytes) == core.QueueLogPageBytes && !page.EOF, "log page not bounded at 32KiB")
	c = page.Cursor
	req = f.waitRequest()
	req.Log = &c
	out, e = core.ObserveWait(f.ctx, f.state, req)
	must(e)
	check(out.LogChanged && !out.DatabaseChanged, "unread log remainder did not wake")
	page = f.log(c)
	check(page.Status == "ready" && page.EOF && len(page.Bytes) == 40000-core.QueueLogPageBytes, "log remainder wrong")
	c = page.Cursor
	req = f.waitRequest()
	req.Log = &c
	out = f.wait(req, func() { must(os.Truncate(f.path, 1)) })
	check(out.Status == "reset" && out.ResetScope == "log" && out.Log.ResetReason == "truncated", "truncation silently appended")
	page = f.log(c)
	check(page.Status == "reset" && len(page.Bytes) == 0, "truncated snapshot returned append bytes")
	c = core.ObservationLogCursor{JobID: f.job, Stream: "stdout", Offset: "0", ObservedSize: "0", ModifiedNS: "0"}
	page = f.log(c)
	c = page.Cursor
	out = f.wait(func() core.ObservationWaitRequest { r := f.waitRequest(); r.Log = &c; return r }(), func() {
		must(os.Rename(f.path, f.path+".retained"))
		must(os.WriteFile(f.path, []byte("replacement"), 0600))
	})
	check(out.Status == "reset" && out.ResetScope == "log", "replacement not reset")
	page = f.log(c)
	check(page.Status == "reset" && len(page.Bytes) == 0, "replacement snapshot silently adopted")
	// Exercise both registered bridge routes and exact compatibility fencing.
	prefix := []string{"--mod-space", "spObserve", "--mod-folder", f.root}
	freshLog := core.ObservationLogCursor{JobID: f.job, Stream: "stdout", Offset: "0", ObservedSize: "0", ModifiedNS: "0"}
	b, e := f.command(f.bridge, append(prefix, "view-snapshot", string(raw(core.ObservationRequest{Protocol: core.ObservationProtocol, Selectors: []core.ObservationSelector{{Slot: "job", Kind: "queue-job", JobID: f.job}}, Log: &freshLog})))...)
	must(e)
	var wire struct {
		Status, Revision string
		Queue            string                     `json:"queue_identity"`
		Store            string                     `json:"store_context"`
		Bridge           string                     `json:"bridge_context"`
		Log              *core.ObservationLogOutput `json:"log"`
	}
	must(json.Unmarshal(b, &wire))
	check(wire.Status == "ready" && len(wire.Bridge) == 64, "bridge route/context discovery failed")
	check(wire.Log != nil && wire.Log.NonAtomicWithDatabase && string(wire.Log.Bytes) == "replacement" && wire.Log.ObservedAt != "", "bridge selected base64 log projection failed")
	must(os.WriteFile(filepath.Join(f.root, "bridge-snapshot.json"), b, 0600))
	descriptorBytes, e := f.command(f.bridge, "compatibility", "spObserve", f.root)
	must(e)
	var descriptor struct {
		Context string `json:"observation_context"`
		Store   string `json:"observation_store_context"`
	}
	must(json.Unmarshal(descriptorBytes, &descriptor))
	check(descriptor.Context == wire.Bridge && descriptor.Store == wire.Store, "compatibility and observation contexts differ")
	// A byte-identical executable at another owned inode is a new incarnation.
	image, e := os.ReadFile(f.bridge)
	must(e)
	copyFile, e := os.CreateTemp(filepath.Dir(f.bridge), "observation-incarnation-")
	must(e)
	copyPath := copyFile.Name()
	defer os.Remove(copyPath)
	must(copyFile.Chmod(0700))
	_, e = copyFile.Write(image)
	must(e)
	must(copyFile.Close())
	continuation := core.ObservationRequest{Protocol: core.ObservationProtocol, QueueIdentity: wire.Queue, StoreContext: wire.Store, BridgeContext: wire.Bridge, Selectors: []core.ObservationSelector{{Slot: "job", Kind: "queue-job", JobID: f.job}}}
	changedImage, e := f.command(copyPath, append(prefix, "view-snapshot", string(raw(continuation)))...)
	must(e)
	check(bytes.Contains(changedImage, []byte(`"status":"reset"`)), "new executable incarnation adopted cached binding")
	watch := core.ObservationWaitRequest{Protocol: core.ObservationProtocol, QueueIdentity: wire.Queue, StoreContext: wire.Store, BridgeContext: wire.Bridge, Revision: wire.Revision, WaitSeconds: 1, Log: &wire.Log.Cursor}
	b, e = f.command(f.bridge, append(prefix, "watch-wait", string(raw(watch)))...)
	must(e)
	check(bytes.Contains(b, []byte(`"status":"unchanged"`)), "registered watch route failed")
	watch.BridgeContext = strings.Repeat("0", 64)
	b, e = f.command(f.bridge, append(prefix, "watch-wait", string(raw(watch)))...)
	must(e)
	check(bytes.Contains(b, []byte(`"status":"reset"`)), "bridge context mismatch adopted cached selection")
	// Rename the owned queue while no connection is open; never retarget a live
	// database. A new private queue at the same pathname has a different context.
	req = f.waitRequest()
	must(f.db.Close())
	f.db = nil
	must(os.Rename(f.state, f.state+"-retained"))
	f.cli(f.current, "list")
	out, e = core.ObserveWait(f.ctx, f.state, req)
	must(e)
	check(out.Status == "reset" && out.ResetScope == "context", "queue inode replacement not fenced")
	missing := filepath.Join(f.root, "missing", "deeper")
	head, e := core.ObserveHead(f.ctx, missing)
	must(e)
	check(head.Status == "unavailable", "missing queue misreported")
	_, e = os.Lstat(filepath.Join(f.root, "missing"))
	check(os.IsNotExist(e), "read-only observation initialized state")
	missingReq := core.ObservationWaitRequest{Protocol: core.ObservationProtocol, StoreContext: head.StoreContext, Revision: "0", WaitSeconds: 1}
	var missingOut core.ObservationWaitOutput
	done := make(chan struct{})
	go func() { missingOut, e = core.ObserveWait(f.ctx, missing, missingReq); close(done) }()
	time.Sleep(80 * time.Millisecond)
	_, createErr := f.command(f.current, "--state-dir", missing, "list")
	must(createErr)
	<-done
	must(e)
	check(missingOut.Status == "reset", "missing ancestor creation was not observed")
	frozenAfter, e := os.ReadFile(filepath.Join(f.root, "after-migration.json"))
	must(e)
	check(bytes.Equal(frozen, frozenAfter), "retained migration evidence changed")
	// Exercise real kernel capability loss, not a mocked watcher or database.
	// The irreversible filter is confined to THIS proof process's locked thread,
	// installed at the end of the flow, and denies only inotify_init1. It changes
	// no host/user limits, services, configuration or other process permissions.
	fallback := f.waitRequest()
	fallback.WaitSeconds = 1
	runtime.LockOSThread()
	filter := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 0, Jf: 1, K: uint32(unix.SYS_INOTIFY_INIT1)},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
	}
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	must(unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0))
	must(unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&program)), 0, 0))
	writeDone := make(chan error, 1)
	var fallbackJob string
	go func() {
		time.Sleep(80 * time.Millisecond)
		b, err := f.command(f.current, "--state-dir", f.state, "submit", "--key", "expiry-proof", "--cwd", f.root, "--", "/usr/bin/true")
		if err == nil {
			var receipt struct {
				ID string `json:"id"`
			}
			err = json.Unmarshal(b, &receipt)
			fallbackJob = receipt.ID
		}
		writeDone <- err
	}()
	out, e = core.ObserveWait(f.ctx, f.state, fallback)
	must(e)
	must(<-writeDone)
	check(out.Status == "changed" && out.DatabaseChanged && out.Watch == "degraded", "expiry fallback missed commit without notification support")
	check(fallbackJob != "", "fallback write receipt missing")
	f.cli(f.current, "cancel", fallbackJob)
	fmt.Println("PASS schema5 refusal; dispatch-lock migration refusal; actual post-DDL ledger-insert failure/rollback; schema5 read-only recovery; explicit schema6/Goose2 retry; exact retained bytes; real CLI/idempotent/rollback revision; coherent concurrent snapshots; idle checkpoint; commit/arm/expiry; selected log append/page/truncate/replace; registered bridge context; queue replacement; missing ancestor/non-init")
}
func main() {
	syscall.Umask(0077)
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: observation-proof PINNED_3850901_OCQ CURRENT_OCQ CURRENT_BRIDGE | --idle-leases CURRENT_OCQ CURRENT_BRIDGE")
		os.Exit(2)
	}
	base := os.Getenv("OCQ_OBSERVATION_PROOF_BASE")
	if base == "" {
		home, e := os.UserHomeDir()
		must(e)
		base = filepath.Join(home, ".local", "state")
	}
	root, e := os.MkdirTemp(base, "ocq-observation-proof-")
	must(e)
	fmt.Println("Artifacts:", root)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	f := &fixture{ctx: ctx, old: os.Args[1], current: os.Args[2], bridge: os.Args[3], root: root, state: filepath.Join(root, "state")}
	defer func() {
		if f.db != nil {
			f.db.Close()
		}
		if p := recover(); p != nil {
			fmt.Fprintln(os.Stderr, "FAIL:", p)
			os.Exit(1)
		}
	}()
	if os.Args[1] == "--idle-leases" {
		f.runIdleLeases()
	} else {
		f.run()
	}
}
