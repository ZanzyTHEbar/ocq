// One real command/bridge/SQLite acknowledgement flow. No agent or mocks.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"ocq/internal/core"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func check(ok bool, message string) {
	if !ok {
		panic(message)
	}
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--emit" {
		fmt.Println("harmless global acknowledgement fixture")
		return
	}
	check(len(os.Args) == 3 || (len(os.Args) == 4 && os.Args[3] == "--fixture"), "usage: herdr-notification-ack-proof BRIDGE FRESH_PRIVATE_ROOT [--fixture]")
	bridge, err := filepath.Abs(os.Args[1])
	must(err)
	root, err := filepath.Abs(os.Args[2])
	must(err)
	must(os.Mkdir(root, 0700))
	state := filepath.Join(root, "queue")
	q, err := core.OpenQueue(state)
	must(err)
	self, err := os.Executable()
	must(err)
	job, err := q.SubmitCommand("global-ack-fixture", root, []string{self, "--emit"}, 0)
	must(err)
	db, err := sql.Open("sqlite", filepath.Join(state, "queue.db"))
	must(err)
	defer db.Close()
	result, err := db.Exec(`UPDATE sqlite_sequence SET seq=9007199254740992 WHERE name='completion_notifications'`)
	must(err)
	changed, err := result.RowsAffected()
	must(err)
	check(changed == 1, "notification sequence seed missing")
	_, err = db.Exec(`CREATE TABLE proof_ack_updates(event_id TEXT NOT NULL);
CREATE TRIGGER proof_ack_update AFTER UPDATE OF acknowledged_at ON completion_notifications
BEGIN INSERT INTO proof_ack_updates(event_id) VALUES(NEW.id); END;`)
	must(err)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- q.Dispatch(ctx, 1, 0, true, nil) }()
	for {
		job, err = q.Job(job.ID)
		must(err)
		if job.State == "finished" {
			break
		}
		check(ctx.Err() == nil, "fixture did not finish")
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	must(<-done)
	must(q.Close())
	call := func(args ...string) ([]byte, error) {
		cmd := exec.Command(bridge, args...)
		cmd.Env = append(os.Environ(), "OCQ_STATE_DIR="+state)
		return cmd.Output()
	}
	read := func(out any, args ...string) {
		b, err := call(args...)
		must(err)
		must(json.Unmarshal(b, out))
		check(!bytes.Contains(b, []byte("owner_token")) && !bytes.Contains(b, []byte(`"request"`)), "private job data in acknowledgement bridge output")
	}
	var observed core.QueueNotificationOutput
	read(&observed, "notification", "completion-"+job.ID)
	check(observed.Event.Sequence == "9007199254740993" && observed.Event.AcknowledgedAt == nil, "large exact sequence/pending observation mismatch")
	scope := core.NotificationAckScope{Machine: "local", Space: "sp-ack-fixture", Queue: observed.QueueIdentity, Event: observed.Event.ID, Job: job.ID, Sequence: observed.Event.Sequence}
	args := func(action string, s core.NotificationAckScope, id string) []string {
		out := []string{"notification-ack-" + action, s.Space, s.Queue, s.Event, s.Job, s.Sequence}
		if id != "" {
			out = append(out, id)
		}
		return out
	}
	jobSnapshot := func() string {
		rows, err := db.Query("SELECT j.*,r.* FROM jobs j JOIN receipts r ON j.id=r.job_id ORDER BY r.sequence")
		must(err)
		defer rows.Close()
		cols, err := rows.Columns()
		must(err)
		var out strings.Builder
		for rows.Next() {
			values := make([]any, len(cols))
			pointers := make([]any, len(cols))
			for i := range values {
				pointers[i] = &values[i]
			}
			must(rows.Scan(pointers...))
			fmt.Fprintf(&out, "%#v", values)
		}
		must(rows.Err())
		return out.String()
	}
	before := jobSnapshot()
	count := func() int { var n int; must(db.QueryRow("SELECT count(*) FROM proof_ack_updates").Scan(&n)); return n }
	var status core.NotificationAckStatus
	read(&status, args("status", scope, "")...)
	check(status.State == "none" && count() == 0, "view consumed notification")
	_, err = os.Stat(filepath.Join(state, "herdr-notification-acks"))
	check(os.IsNotExist(err), "read-only status created a journal")
	if len(os.Args) == 4 {
		must(json.NewEncoder(os.Stdout).Encode(map[string]any{"result": "FIXTURE_READY", "state_dir": state, "scope": scope}))
		return
	}
	read(&status, args("prepare", scope, "")...)
	check(status.State == "prepared" && status.Scope == scope && count() == 0, "prepare acknowledged or changed context")
	id := status.IntentID
	intentPath := filepath.Join(state, "herdr-notification-acks", id+".json")
	intent, err := os.ReadFile(intentPath)
	must(err)
	info, err := os.Stat(intentPath)
	must(err)
	check(info.Mode().Perm() == 0600 && bytes.Contains(intent, []byte(scope.Sequence)) && bytes.Contains(intent, []byte(scope.Queue)), "durable exact intent missing")
	other := scope
	other.Space = "sp-other"
	_, err = call(args("arm", other, id)...)
	check(err != nil && count() == 0, "wrong space armed acknowledgement")
	other = scope
	other.Queue = strings.Repeat("0", 64)
	_, err = call(args("arm", other, id)...)
	check(err != nil && count() == 0, "wrong queue armed acknowledgement")
	other = scope
	other.Machine = "remote"
	_, err = core.ArmNotificationAcknowledgement(state, other, id)
	check(err != nil && count() == 0, "remote scope armed local acknowledgement")
	a, err := core.OpenExistingNotificationAcknowledger(state)
	must(err)
	_, err = a.Acknowledge(scope.Event, scope.Job, 9007199254740992, scope.Queue)
	check(err != nil && count() == 0, "rounded sequence acknowledged event")
	must(a.Close())
	read(&status, args("arm", scope, id)...)
	check(status.State == "armed" && count() == 0, "arm consumed notification")
	read(&status, args("status", scope, "")...)
	check(status.State == "armed" && status.Observation.Event.AcknowledgedAt == nil && count() == 0, "read/reload dispatched a claim")
	// A real controlled transport wrapper loses the process response after the
	// bridge runs. Recovery uses only the exact persisted event and journal.
	lostArgs := append([]string{"-c", `"$@" >/dev/null; exit 73`, "lost-ack-response", bridge}, args("attempt", scope, id)...)
	lost := exec.Command("/bin/sh", lostArgs...)
	lost.Env = append(os.Environ(), "OCQ_STATE_DIR="+state)
	err = lost.Run()
	var exit *exec.ExitError
	check(errors.As(err, &exit) && exit.ExitCode() == 73, "response-loss wrapper did not run")
	read(&status, args("status", scope, "")...)
	check(status.State == "observed" && status.Observation.Event.Sequence == scope.Sequence && status.Observation.Event.AcknowledgedAt != nil && count() == 1, "lost response did not recover exact acknowledgement")
	ackTime := *status.Observation.Event.AcknowledgedAt
	_, err = call(args("attempt", scope, id)...)
	check(err != nil && count() == 1, "second mutation attempt was allowed")
	_, err = call(args("arm", scope, id)...)
	check(err != nil && count() == 1, "second durable claim was allowed")
	read(&observed, "notification", scope.Event)
	check(*observed.Event.AcknowledgedAt == ackTime && observed.Event.JobID == scope.Job && observed.Event.Sequence == scope.Sequence, "exact acknowledgement timestamp/identity changed")
	var pending core.QueueNotificationsOutput
	read(&pending, "notifications")
	check(len(pending.Items) == 0, "acknowledged event remained pending")
	check(jobSnapshot() == before, "acknowledgement changed job/receipt/capacity state")
	missing := filepath.Join(root, "missing")
	_, err = core.OpenExistingNotificationAcknowledger(missing)
	check(err != nil, "writer initialized missing queue")
	_, err = os.Stat(missing)
	check(os.IsNotExist(err), "missing writer state created")
	old := filepath.Join(root, "old")
	oldQ, err := core.OpenQueue(old)
	must(err)
	must(oldQ.Close())
	oldDB, err := sql.Open("sqlite", filepath.Join(old, "queue.db"))
	must(err)
	_, err = oldDB.Exec("PRAGMA user_version=4")
	must(err)
	must(oldDB.Close())
	oldBefore, err := os.ReadFile(filepath.Join(old, "queue.db"))
	must(err)
	_, err = core.OpenExistingNotificationAcknowledger(old)
	check(err != nil && strings.Contains(err.Error(), "requires schema version 5, got 4"), "writer did not refuse old schema")
	oldAfter, err := os.ReadFile(filepath.Join(old, "queue.db"))
	must(err)
	check(bytes.Equal(oldBefore, oldAfter), "writer migrated old queue")
	must(json.NewEncoder(os.Stdout).Encode(map[string]any{"result": "PASS", "state_dir": state, "scope": scope, "checks": "view non-consuming; durable prepare/arm; exact >2^53 sequence; wrong scope rejected; lost response read-only recovery; one SQL ack; unchanged job/receipt; missing/old writer refused"}))
}
