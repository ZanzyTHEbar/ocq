package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"ocq/internal/core"
)

// One real CLI/SQLite/command lifecycle. Historical project/association fixtures
// stand in for already-published agent artifacts; no mock execution or live agent.
func (s *scenario) runScheduling() {
	started := time.Now()
	command := func(key string) job {
		b, e := s.cli("submit", "--key", key, "--cwd", s.root, "--memory-bytes", "0", "--", "/bin/true")
		must(e)
		var j job
		must(json.Unmarshal(b, &j))
		return j
	}
	producer := command("historical-producer")
	p := s.start("--commands-only", "--slots", "1", "--memory-floor-bytes", "0")
	s.wait("producer exit", func() bool { return s.get(producer.ID).State == "finished" })
	s.stop(p, syscall.SIGTERM)
	unknown := command("lost-handoff")
	barrier := s.submit("legacy-barrier", "barrier", "4611686018427387904")
	db, e := sql.Open("sqlite", "file:"+filepath.Join(s.state, "queue.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	must(e)
	defer db.Close()
	// Crash fixture: intent committed, no spawn receipt. Restart must hold, not run.
	_, e = db.Exec("UPDATE jobs SET state='launching',reason='acceptance crash after intent before spawn' WHERE id=?", unknown.ID)
	must(e)
	_, e = s.cli("activate-scheduling")
	must(e)
	head, e := core.ObserveHead(s.ctx, s.state)
	must(e)
	queueIdentity := head.QueueIdentity
	var cutoff int64
	must(db.QueryRow("SELECT cutoff FROM scheduling_policy").Scan(&cutoff))
	check(cutoff == barrier.Sequence, "activation cutoff changed accepted ordering")
	_, e = s.cli("activate-scheduling")
	must(e)
	must(db.QueryRow("SELECT cutoff FROM scheduling_policy").Scan(&cutoff))
	check(cutoff == barrier.Sequence, "activation was not idempotent")

	// Seed a retained project history, not fabricated agent completion evidence.
	project := "proj-" + strings.Repeat("0", 32)
	base := strings.Repeat("1", 40)
	digest := func(b []byte) string { x := sha256.Sum256(b); return hex.EncodeToString(x[:]) }
	manifest := []byte(`{"version":1,"kind":"result","source":"machine_capture","claim_label":"machine_verified_snapshot","claimed_validation":{"status":"not_run"}}`)
	artifact := digest(manifest)
	assoc := "assoc-" + strings.Repeat("b", 32)
	tx, e := db.Begin()
	must(e)
	for i := 1; i <= 25; i++ {
		id := fmt.Sprintf("proj-%032x", i)
		_, e = tx.Exec("INSERT INTO projects VALUES(?,?,?)", id, "page fixture", fmt.Sprintf("%03d", i))
		must(e)
	}
	_, e = tx.Exec("INSERT INTO projects VALUES(?,?,?)", project, "artifact fixture", "100")
	must(e)
	brief := strings.Repeat("c", 64)
	_, e = tx.Exec("INSERT INTO project_manifests VALUES(?,?,1,'brief',?,'fixture')", brief, project, []byte(`{}`))
	must(e)
	_, e = tx.Exec("INSERT INTO project_bindings VALUES(?,1,?,?,?,?,?,?,?,'fixture','fixture','fixture')", project, s.root, s.root, base, "ses_fixture", "fixture", s.root, brief)
	must(e)
	_, e = tx.Exec("INSERT INTO project_jobs VALUES(?,?,1,'implementation',?,NULL,NULL,'fixture')", producer.ID, project, brief)
	must(e)
	must(tx.Commit())
	for i := 0; i < 25; i++ {
		j := command(fmt.Sprintf("lane-history-%d", i))
		_, e = s.cli("cancel", j.ID)
		must(e)
		_, e = db.Exec("INSERT INTO project_jobs VALUES(?,?,1,'implementation',?,NULL,NULL,'fixture')", j.ID, project, brief)
		must(e)
		_, e = db.Exec("INSERT INTO project_lanes(lane_id,project_id,owner_job_id,role,base_commit,fsfork_driver,created_at) VALUES(?,?,?,'implementation',?,'git_worktree',?)", fmt.Sprintf("lane-%032x", i), project, j.ID, base, fmt.Sprintf("%03d", i))
		must(e)
	}

	schedule := func(name, lane string, deps []core.ExactDependency) string {
		v := core.SchedulingEnvelope{Version: 1, QueueIdentity: queueIdentity, ProjectID: lane, Dependencies: deps}
		if v.Dependencies == nil {
			v.Dependencies = []core.ExactDependency{}
		}
		b, e := json.Marshal(v)
		must(e)
		path := filepath.Join(s.root, name+".json")
		save(path, b)
		return path
	}
	submit := func(key, label, lane, memory string, deps []core.ExactDependency) job {
		release := filepath.Join(s.root, "release-"+label)
		s.releases = append(s.releases, release)
		path := schedule(key, lane, deps)
		b, e := s.cli("submit", "--key", key, "--cwd", s.root, "--memory-bytes", memory, "--scheduling-file", path, "--", s.self, "probe", s.root, label, release, "literal ; $HOME")
		must(e)
		var j job
		must(json.Unmarshal(b, &j))
		return j
	}
	dep := core.ExactDependency{ProjectID: project, ProducerJob: producer.ID, AssociationID: assoc, ManifestDigest: artifact, Criterion: "captured_snapshot", Verification: "machine_verified_snapshot"}
	blocked := submit("blocked", "blocked", project, "0", []core.ExactDependency{dep})
	importBytes := []byte(`{"version":1,"kind":"result","source":"author_import","claim_label":"author_assertion","claimed_validation":{"status":"passed"}}`)
	importDigest := digest(importBytes)
	importAssoc := "assoc-" + strings.Repeat("f", 32)
	_, e = db.Exec("INSERT INTO project_manifests VALUES(?,?,1,'result',?,'fixture')", importDigest, project, importBytes)
	must(e)
	_, e = db.Exec("INSERT INTO project_result_associations(id,project_id,job_id,kind,manifest_digest,recorded_at) VALUES(?,?,?,'result',?,'fixture')", importAssoc, project, producer.ID, importDigest)
	must(e)
	importDep := dep
	importDep.ManifestDigest = importDigest
	importDep.AssociationID = importAssoc
	imported := submit("imported", "imported", project, "0", []core.ExactDependency{importDep})
	wrongDep := dep
	wrongDep.ManifestDigest = strings.Repeat("d", 64)
	wrongDigest := submit("wrong-digest", "wrong-digest", project, "0", []core.ExactDependency{wrongDep})
	a := project
	bLane := fmt.Sprintf("proj-%032x", 1)
	z := fmt.Sprintf("proj-%032x", 2)
	a1 := submit("a1", "a1", a, "0", nil)
	a2 := submit("a2", "a2", a, "0", nil)
	b1 := submit("b1", "b1", bLane, "0", nil)
	b2 := submit("b2", "b2", bLane, "0", nil)
	// A fitting later request must not bypass its same-project oversized head.
	big := submit("big", "big", z, "4611686018427387904", nil)
	small := submit("small", "small", z, "0", nil)
	bad := dep
	bad.ManifestDigest = strings.Repeat("d", 64)
	save(filepath.Join(s.root, "blocked.json"), mustJSON(core.SchedulingEnvelope{Version: 1, QueueIdentity: queueIdentity, ProjectID: project, Dependencies: []core.ExactDependency{bad}}))
	_, e = s.cli("submit", "--key", "blocked", "--cwd", s.root, "--memory-bytes", "0", "--scheduling-file", filepath.Join(s.root, "blocked.json"), "--", s.self, "probe", s.root, "blocked", filepath.Join(s.root, "release-blocked"), "literal ; $HOME")
	check(e != nil, "changed dependency silently recovered")

	observe := func(kind, project, cursor string) core.ObservationSlot {
		start := time.Now()
		v, e := core.ObserveSnapshot(s.ctx, s.state, core.ObservationRequest{Protocol: core.ObservationProtocol, Selectors: []core.ObservationSelector{{Slot: "page", Kind: kind, ProjectID: project, Cursor: cursor}}})
		must(e)
		check(v.Status == "ready" && len(v.Slots) == 1, "observation not ready")
		s.note(fmt.Sprintf("page kind=%s status=%s latency=%s", kind, v.Slots[0].Status, time.Since(start)))
		return v.Slots[0]
	}
	first := observe("projects", "", "0")
	check(first.HasMore && strings.HasPrefix(first.Cursor, "k1."), "keyset first page missing")
	second := observe("projects", "", first.Cursor)
	check(!second.HasMore && second.Status == "ready", "keyset continuation failed")
	check(len(first.Data.([]core.ProjectSummary)) == 20 && len(second.Data.([]core.ProjectSummary)) == 6, "project keyset row counts")
	laneFirst := observe("lanes", project, "0")
	laneNext := observe("lanes", project, laneFirst.Cursor)
	check(laneFirst.HasMore && !laneNext.HasMore && laneNext.Status == "ready", "lane keyset continuation failed")
	check(len(laneFirst.Data.([]core.ProjectLane)) == 20 && len(laneNext.Data.([]core.ProjectLane)) == 5, "lane keyset row counts")
	check(observe("projects", "", laneFirst.Cursor).Status == "reset_required", "cross-selector cursor accepted")
	// Record query plans from real SQLite; no benchmark or artificial pure tests.
	for _, query := range []string{
		`EXPLAIN QUERY PLAN SELECT id FROM projects WHERE (created_at,id)>('','') ORDER BY created_at,id LIMIT 21`,
		`EXPLAIN QUERY PLAN SELECT lane_id FROM project_lanes WHERE project_id='x' AND (created_at,lane_id)>('','') ORDER BY created_at,lane_id LIMIT 21`,
		`EXPLAIN QUERY PLAN SELECT id FROM jobs WHERE state IN ('launching','running','unknown')`,
	} {
		rows, e := db.Query(query)
		must(e)
		for rows.Next() {
			var id, parent, unused int
			var detail string
			must(rows.Scan(&id, &parent, &unused, &detail))
			s.note("query plan: " + detail)
		}
		must(rows.Close())
	}
	revisionJob := command("revision")
	_, e = s.cli("cancel", revisionJob.ID)
	must(e)
	check(observe("projects", "", first.Cursor).Status == "reset_required", "revision update silently continued old page")
	other := s.state
	s.state = filepath.Join(s.root, "independent")
	otherJob := command("independent")
	q := s.start("--commands-only", "--slots", "1", "--memory-floor-bytes", "0")
	s.wait("independent store executes", func() bool { return s.get(otherJob.ID).State == "finished" })
	s.stop(q, syscall.SIGTERM)
	s.state = other

	p = s.start("--commands-only", "--slots", "2", "--memory-floor-bytes", "0")
	admitted := func(label string, j job) {
		var accepted string
		must(db.QueryRow("SELECT accepted_at FROM receipts WHERE job_id=?", j.ID).Scan(&accepted))
		t, e := time.Parse(time.RFC3339Nano, accepted)
		must(e)
		s.note(fmt.Sprintf("running observed %s queued-to-observation=%s (poll bound, not exact admission latency)", label, time.Since(t)))
	}
	s.wait("unknown restored", func() bool { return s.get(unknown.ID).State == "unknown" })
	time.Sleep(300 * time.Millisecond)
	check(s.get(a1.ID).State == "queued" && s.get(blocked.ID).State == "queued", "legacy drain barrier bypassed")
	_, e = s.cli("cancel", barrier.ID)
	must(e)
	s.wait("a1 admitted", func() bool { return s.get(a1.ID).State == "running" })
	admitted("a1", a1)
	lockStart := time.Now()
	lockTx, e := db.Begin()
	must(e)
	_, e = lockTx.Exec("UPDATE jobs SET reason=reason WHERE id=?", big.ID)
	must(e)
	must(lockTx.Rollback())
	s.note(fmt.Sprintf("external SQLite write-lock acquisition while dispatcher has a running command: %s", time.Since(lockStart)))
	check(s.get(a2.ID).State == "queued" && s.get(b1.ID).State == "queued", "unknown reservation capacity lost")
	save(filepath.Join(s.root, "release-a1"), []byte("release"))
	s.wait("b1 round robin", func() bool { return s.get(b1.ID).State == "running" })
	admitted("b1", b1)
	check(s.get(a2.ID).State == "queued", "round robin chose same project twice")
	s.signal(p, syscall.SIGTERM)
	save(filepath.Join(s.root, "release-b1"), []byte("release"))
	s.stop(p, syscall.SIGTERM)
	var last string
	must(db.QueryRow("SELECT last_project FROM scheduling_policy").Scan(&last))
	check(last == bLane, "cursor not committed with reservation")
	p = s.start("--commands-only", "--slots", "2", "--memory-floor-bytes", "0")
	s.wait("restart cursor picks a2", func() bool { return s.get(a2.ID).State == "running" })
	admitted("a2", a2)
	save(filepath.Join(s.root, "release-a2"), []byte("release"))
	s.wait("b2 admitted", func() bool { return s.get(b2.ID).State == "running" })
	admitted("b2", b2)
	save(filepath.Join(s.root, "release-b2"), []byte("release"))
	s.wait("b2 completed", func() bool { return s.get(b2.ID).State == "finished" })
	check(s.get(big.ID).State == "queued" && s.get(small.ID).State == "queued" && s.get(blocked.ID).State == "queued", "same-project fit bypass or exit-only dependency admitted")
	// Wrong exact artifact, then right artifact. These are historical publication
	// fixtures, not claims of newly executed or verified agent/capture workflows.
	_, e = db.Exec("INSERT INTO project_manifests VALUES(?,?,1,'result',?,'fixture')", artifact, project, manifest)
	must(e)
	_, e = db.Exec("INSERT INTO project_result_associations(id,project_id,job_id,kind,manifest_digest,recorded_at) VALUES(?,?,?,'result',?,'fixture')", "assoc-"+strings.Repeat("e", 32), project, producer.ID, artifact)
	must(e)
	time.Sleep(300 * time.Millisecond)
	check(s.get(blocked.ID).State == "queued", "wrong association satisfied exact prerequisite")
	_, e = db.Exec("INSERT INTO project_result_associations(id,project_id,job_id,kind,manifest_digest,recorded_at) VALUES(?,?,?,'result',?,'fixture')", assoc, project, producer.ID, artifact)
	must(e)
	s.wait("exact artifact releases dependency", func() bool { return s.get(blocked.ID).State == "running" })
	admitted("dependent", blocked)
	save(filepath.Join(s.root, "release-blocked"), []byte("release"))
	s.wait("dependent terminal", func() bool { return s.get(blocked.ID).State == "finished" })
	s.stop(p, syscall.SIGTERM)
	check(s.get(unknown.ID).State == "unknown", "unknown hold changed")
	check(s.get(imported.ID).State == "queued" && s.get(wrongDigest.ID).State == "queued", "imported success or wrong digest satisfied verified exact artifact")
	var events int
	must(db.QueryRow("SELECT count(*) FROM completion_notifications WHERE job_id=?", blocked.ID).Scan(&events))
	check(events == 1, "terminal/outbox atomic boundary absent or duplicated")
	b, e := os.ReadFile(filepath.Join(s.root, "events"))
	must(e)
	check(string(b) == "a1\nb1\na2\nb2\nblocked\n", "duplicate launch or wrong project order: "+string(b))
	s.note(fmt.Sprintf("bounded lifecycle elapsed=%s; 26 projects; project pages 20+6; exact cursor revision rejection; held capacity=1; independent stores=2", time.Since(started)))
}

func mustJSON(v any) []byte { b, e := json.Marshal(v); must(e); return b }
