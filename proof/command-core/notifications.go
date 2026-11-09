package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type notice struct {
	ID             string  `json:"id"`
	JobID          string  `json:"job_id"`
	State          string  `json:"state"`
	Outcome        string  `json:"outcome"`
	Validation     string  `json:"validation"`
	Origin         string  `json:"origin"`
	RecordedAt     string  `json:"recorded_at"`
	Exit           *int    `json:"exit_code"`
	AcknowledgedAt *string `json:"acknowledged_at"`
}

func notificationConsumer() {
	bin, state, root := os.Args[2], os.Args[3], os.Args[4]
	b, err := json.Marshal(who(os.Getpid()))
	must(err)
	save(filepath.Join(root, "consumer.identity"), b)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	b, err = exec.CommandContext(ctx, bin, "--state-dir", state, "notifications", "--limit", "1").Output()
	must(err)
	save(filepath.Join(root, "consumer-pull.json"), b)
	// The parent crashes this actual consumer after it has read a real event,
	// before acknowledgement. A finite timeout prevents an orphaned proof loop.
	time.Sleep(20 * time.Second)
}

func (s *scenario) pullNotices(limit string) ([]notice, []byte) {
	b, err := s.cli("notifications", "--limit", limit)
	must(err)
	var items []notice
	must(json.Unmarshal(b, &items))
	check(items != nil, "notification output is not a JSON array")
	return items, b
}

func (s *scenario) runNotifications(legacy1, legacy2 string) {
	oldCLI := func(bin string, args ...string) []byte {
		ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
		defer cancel()
		b, err := exec.CommandContext(ctx, bin, append([]string{"--state-dir", s.state}, args...)...).Output()
		must(err)
		return b
	}
	legacyArgs := func(key string) []string {
		return []string{"submit", "--key", key, "--cwd", s.root, "--memory-bytes", "1", "--", "/usr/bin/printf", "%s\n", "never executed"}
	}
	var old job
	for _, key := range []string{"legacy-a", "legacy-b"} {
		must(json.Unmarshal(oldCLI(legacy1, legacyArgs(key)...), &old))
		oldCLI(legacy1, "cancel", old.ID)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(s.state, "queue.db")+"?mode=ro&_pragma=busy_timeout(5000)")
	must(err)
	defer db.Close()
	version := func() int { var n int; must(db.QueryRow("PRAGMA user_version").Scan(&n)); return n }
	snapshot := func() string {
		var b string
		must(db.QueryRow(`SELECT json_group_array(json_object('sequence',sequence,'key',key,'job',job_id,'canonical',hex(canonical),'digest',digest,'accepted',accepted_at)) FROM (SELECT * FROM receipts ORDER BY sequence)`).Scan(&b))
		return b
	}
	check(version() == 1, "legacy fixture is not real schema1")
	before := snapshot()
	save(filepath.Join(s.root, "migration-receipts-before.json"), []byte(before))
	oldCLI(legacy2, "status", old.ID)
	check(version() == 2 && snapshot() == before, "schema1->2 changed receipts")
	items, _ := s.pullNotices("1")
	check(version() == 5 && snapshot() == before && len(items) == 1, "schema5 migration/receipt/bounded pull failed")
	all, b := s.pullNotices("100")
	save(filepath.Join(s.root, "migration-events.json"), b)
	check(len(all) == 2, "migration must create exactly one snapshot per existing terminal job")
	for _, item := range all {
		check(item.ID == "completion-"+item.JobID && item.State == "cancelled" && item.Outcome == "cancelled_before_launch" && item.Origin == "migration_snapshot" && item.RecordedAt != "" && item.Exit == nil && item.Validation == "not_run" && item.AcknowledgedAt == nil, "migration invented terminal time/outcome/validation")
		_, err = s.cli("ack", item.ID)
		must(err)
	}
	b, err = s.cli(legacyArgs("legacy-b")...)
	must(err)
	var retry job
	must(json.Unmarshal(b, &retry))
	check(retry.ID == old.ID && retry.State == "cancelled" && snapshot() == before, "v1 matching retry changed receipt")
	s.note("PASS real schema1->2->3 additive migration, exact receipts/digests preserved, deterministic labelled terminal snapshots, bounded pull and matching v1 retry")

	// A single real execution, with a durable marker. Legacy jobs never launch.
	j := s.submit("notify-once", "notification", "1")
	save(filepath.Join(s.root, "release-notification"), []byte("ready\n"))
	d := s.start()
	s.wait("command and atomic completion event", func() bool { return s.get(j.ID).State == "finished" })
	var count int
	must(db.QueryRow(`SELECT count(*) FROM jobs j JOIN completion_notifications n ON n.job_id=j.id WHERE j.id=? AND j.state=n.state AND j.outcome=n.outcome AND j.exit_code=n.exit_code AND j.validation=n.validation`, j.ID).Scan(&count))
	check(count == 1, "terminal state lacks matching committed notification snapshot")
	items, b = s.pullNotices("1")
	save(filepath.Join(s.root, "first-pull.json"), b)
	check(len(items) == 1 && items[0].JobID == j.ID && items[0].Origin == "transition" && items[0].State == "finished" && items[0].Outcome == "exited" && items[0].Exit != nil && *items[0].Exit == 0 && items[0].Validation == "not_run", "command outcome mistaken for validation")
	event := items[0]
	receipts := snapshot()
	c := exec.Command(s.self, "notification-consumer", s.bin, s.state, s.root)
	must(c.Start())
	consumer := &owned{identity: who(c.Process.Pid), cmd: c, done: make(chan error, 1)}
	s.processes = append(s.processes, consumer)
	go func() { consumer.done <- c.Wait() }()
	s.note(fmt.Sprintf("owned consumer pid=%d start=%d boot=%s", consumer.PID, consumer.Start, consumer.Boot))
	s.wait("consumer received event before ack", func() bool { _, err := os.Stat(filepath.Join(s.root, "consumer-pull.json")); return err == nil })
	consumerPull, err := os.ReadFile(filepath.Join(s.root, "consumer-pull.json"))
	must(err)
	check(bytes.Equal(bytes.TrimSpace(b), bytes.TrimSpace(consumerPull)), "consumer received a different event")
	s.stop(consumer, syscall.SIGKILL)
	s.stop(d, syscall.SIGKILL)
	index := len(s.processes)
	d = s.start()
	s.wait("restarted dispatcher ready", func() bool {
		b, _ := os.ReadFile(filepath.Join(s.root, fmt.Sprintf("dispatcher-%d.identity.log", index)))
		return strings.Contains(string(b), "dispatcher ready")
	})
	_, redelivered := s.pullNotices("1")
	check(bytes.Equal(bytes.TrimSpace(b), bytes.TrimSpace(redelivered)), "consumer/dispatcher crash lost or changed the unacknowledged event")
	save(filepath.Join(s.root, "redelivery.json"), redelivered)
	_, err = s.cli("ack", event.ID)
	must(err) // Deliberately discard ack stdout.
	ack, err := s.cli("ack", event.ID)
	must(err)
	save(filepath.Join(s.root, "ack.json"), ack)
	var acknowledged notice
	must(json.Unmarshal(ack, &acknowledged))
	check(acknowledged.AcknowledgedAt != nil, "ack did not persist")
	s.stop(d, syscall.SIGTERM)
	index = len(s.processes)
	d = s.start()
	s.wait("post-ack dispatcher restart", func() bool {
		b, _ := os.ReadFile(filepath.Join(s.root, fmt.Sprintf("dispatcher-%d.identity.log", index)))
		return strings.Contains(string(b), "dispatcher ready")
	})
	items, _ = s.pullNotices("100")
	check(len(items) == 0, "acknowledged event redelivered after restart")
	again, err := s.cli("ack", event.ID)
	must(err)
	check(bytes.Equal(ack, again), "idempotent ack changed timestamp or payload after restart")
	_, err = s.cli("status", j.ID)
	must(err)
	must(db.QueryRow("SELECT count(*) FROM completion_notifications WHERE job_id=?", j.ID).Scan(&count))
	check(count == 1, "reinspection duplicated completion")
	check(snapshot() == receipts, "delivery changed immutable receipts")
	marker, err := os.ReadFile(filepath.Join(s.root, "events"))
	must(err)
	check(string(marker) == "notification\n", "delivery/restart executed the command again")
	s.stop(d, syscall.SIGTERM)
	s.note("PASS consumer SIGKILL before ack and dispatcher restart redeliver byte-identical event; lost ack response/idempotent ack survive restart; one execution and unchanged receipts")

	// Current queued cancellation and a genuine OS exec failure are terminal too.
	cancelled := s.submit("notify-cancel", "cancelled", "1")
	_, err = s.cli("cancel", cancelled.ID)
	must(err)
	missing := filepath.Join(s.root, "removed-executable")
	save(missing, []byte("#!/bin/sh\nexit 0\n"))
	must(os.Chmod(missing, 0700))
	b, err = s.cli("submit", "--key", "launch-failure", "--cwd", s.root, "--memory-bytes", "1", "--", missing)
	must(err)
	var failed job
	must(json.Unmarshal(b, &failed))
	must(os.Rename(missing, missing+".retained"))
	d = s.start()
	s.wait("launch failure terminal", func() bool { return s.get(failed.ID).State == "finished" })
	s.stop(d, syscall.SIGTERM)
	items, b = s.pullNotices("100")
	save(filepath.Join(s.root, "other-terminal-events.json"), b)
	check(len(items) == 2, "cancel/spawn failure completion missing or duplicate")
	for _, item := range items {
		check(item.Validation == "not_run" && item.Exit == nil && item.Origin == "transition", "nonexecution notification fabricated success/exit")
		check((item.JobID == cancelled.ID && item.State == "cancelled" && item.Outcome == "cancelled_before_launch") || (item.JobID == failed.ID && item.State == "finished" && item.Outcome == "launch_failed"), "terminal outcome incorrect")
	}
	marker, err = os.ReadFile(filepath.Join(s.root, "events"))
	must(err)
	check(string(marker) == "notification\n", "cancel/failed launch unexpectedly executed a payload")
	s.note("PASS queued cancellation and real exec failure create accurate terminal events, not success; no extra execution")
}
