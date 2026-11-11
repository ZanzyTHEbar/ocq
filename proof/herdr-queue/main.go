// Real command/SQLite/bridge acceptance for the non-consuming queue surface.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
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
		b := make([]byte, 32769)
		for i := range b {
			b[i] = byte(i)
		}
		_, err := os.Stdout.Write(b)
		must(err)
		_, err = fmt.Fprint(os.Stderr, "harmless stderr\n")
		must(err)
		return
	}
	check(len(os.Args) == 3, "usage: herdr-queue-proof BRIDGE FRESH_PRIVATE_ROOT")
	bridge, err := filepath.Abs(os.Args[1])
	must(err)
	root, err := filepath.Abs(os.Args[2])
	must(err)
	must(os.Mkdir(root, 0700))
	state := filepath.Join(root, "queue")
	self, err := os.Executable()
	must(err)
	q, err := core.OpenQueue(state)
	must(err)
	job, err := q.SubmitCommand("private-request-sentinel", root, []string{self, "--emit"}, 0)
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
		check(ctx.Err() == nil, "command did not finish")
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	must(<-done)
	must(q.Close())
	read := func(dir string, args ...string) ([]byte, error) {
		cmd := exec.Command(bridge, args...)
		cmd.Env = append(os.Environ(), "OCQ_STATE_DIR="+dir)
		return cmd.Output()
	}
	decode := func(args ...string) map[string]any {
		b, err := read(state, args...)
		must(err)
		for _, secret := range []string{"private-request-sentinel", "owner_token", "\"request\"", "\"prompt\"", "\"prompt_id\"", "\"resolution_evidence\"", "\"terminal_evidence\"", promptSentinel, tokenSentinel, evidenceSentinel, authSentinel} {
			check(!bytes.Contains(b, []byte(secret)), "private request/ownership data in bridge response")
		}
		var out map[string]any
		must(json.Unmarshal(b, &out))
		return out
	}
	// Compare all persisted tables around the real bridge reads, not merely the
	// job state: read-only means no event acknowledgement or receipt mutation.
	db, err := sql.Open("sqlite", "file:"+filepath.Join(state, "queue.db")+"?mode=ro")
	must(err)
	defer db.Close()
	snapshot := func() string {
		rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
		must(err)
		var names []string
		for rows.Next() {
			var name string
			must(rows.Scan(&name))
			names = append(names, name)
		}
		must(rows.Err())
		must(rows.Close())
		var result strings.Builder
		for _, name := range names {
			rows, err := db.Query(`SELECT * FROM "` + strings.ReplaceAll(name, `"`, `""`) + `" ORDER BY rowid`)
			must(err)
			columns, err := rows.Columns()
			must(err)
			for rows.Next() {
				values := make([]any, len(columns))
				pointers := make([]any, len(columns))
				for i := range values {
					pointers[i] = &values[i]
				}
				must(rows.Scan(pointers...))
				fmt.Fprintf(&result, "%s:%#v\n", name, values)
			}
			must(rows.Err())
			must(rows.Close())
		}
		return result.String()
	}
	before := snapshot()
	page := decode("queue-jobs", "0")
	items := page["items"].([]any)
	check(len(items) == 1, "expected one queue job")
	summary := items[0].(map[string]any)
	check(summary["id"] == job.ID && summary["sequence"] == "1" && summary["state"] == "finished", "safe queue summary mismatch")
	detail := decode("queue-job", job.ID)["job"].(map[string]any)
	check(detail["kind"] == "command" && detail["validation"] == "not_run", "safe detail mismatch")
	first := decode("queue-log", job.ID, "stdout", "0")
	b, err := base64.StdEncoding.DecodeString(first["bytes_base64"].(string))
	must(err)
	check(len(b) == 32768 && first["next_offset"] == "32768" && first["eof"] == false, "log must stop at 32KiB")
	for i, v := range b {
		check(v == byte(i), "binary log bytes changed")
	}
	last := decode("queue-log", job.ID, "stdout", "32768")
	check(last["bytes_base64"] == "AA==" && last["next_offset"] == "32769" && last["eof"] == true, "log continuation mismatch")
	stderr := decode("queue-log", job.ID, "stderr", "0")
	b, err = base64.StdEncoding.DecodeString(stderr["bytes_base64"].(string))
	must(err)
	check(string(b) == "harmless stderr\n", "stderr mismatch")
	for _, args := range [][]string{{"queue-log", job.ID, "../stdout", "0"}, {"queue-log", job.ID, "stdout", "-1"}, {"queue-job", "missing"}, {"notification", job.ID}} {
		_, err := read(state, args...)
		check(err != nil, "invalid exact read unexpectedly succeeded")
	}
	events := decode("notifications")["items"].([]any)
	check(len(events) == 1, "completion event missing")
	event := events[0].(map[string]any)
	check(event["acknowledged_at"] == nil && event["sequence"] == "1", "pending event mismatch")
	exact := decode("notification", event["id"].(string))["event"].(map[string]any)
	check(exact["job_id"] == job.ID, "exact event lookup mismatch")
	check(snapshot() == before, "bridge reads changed persistent queue tables")
	worktree, manifest := seedProject(state, root)
	beforePrivacy := snapshot()
	checkProjectPrivacy(decode, worktree)
	// Explicit exact-manifest inspection must still return the retained content,
	// including evidence deliberately excluded from automatic summaries.
	manifestJSON, err := read(state, "manifest", privacyProject, digest(manifest))
	must(err)
	var retained string
	must(json.Unmarshal(manifestJSON, &retained))
	check(retained == string(manifest), "explicit manifest bytes changed")
	check(snapshot() == beforePrivacy, "project bridge reads changed persistent queue tables")
	missing := filepath.Join(root, "missing")
	_, err = read(missing, "queue-jobs", "0")
	check(err != nil, "missing store accepted")
	_, err = os.Stat(missing)
	check(os.IsNotExist(err), "reader initialized missing state")
	old := filepath.Join(root, "old")
	must(os.Mkdir(old, 0700))
	oldDB, err := sql.Open("sqlite", filepath.Join(old, "queue.db"))
	must(err)
	_, err = oldDB.Exec("PRAGMA journal_mode=WAL; CREATE TABLE retained_marker(value TEXT); PRAGMA user_version=4;")
	must(err)
	var mode string
	must(oldDB.QueryRow("PRAGMA journal_mode").Scan(&mode))
	check(mode == "wal", "old-schema fixture must use supported WAL mode")
	must(oldDB.Close())
	must(os.Chmod(filepath.Join(old, "queue.db"), 0600))
	oldBefore, err := os.ReadFile(filepath.Join(old, "queue.db"))
	must(err)
	_, err = read(old, "queue-jobs", "0")
	check(err != nil, "old schema accepted")
	var exit *exec.ExitError
	check(errors.As(err, &exit) && bytes.Contains(exit.Stderr, []byte("requires schema version 5, got 4")), "old store was not rejected at the schema-version gate")
	oldAfter, err := os.ReadFile(filepath.Join(old, "queue.db"))
	must(err)
	check(bytes.Equal(oldBefore, oldAfter), "reader migrated old store")
	must(json.NewEncoder(os.Stdout).Encode(map[string]any{"result": "PASS", "state_dir": state, "job_id": job.ID, "event_id": event["id"], "checks": "bounded binary logs, safe agent/project summaries, exact manifest content, pending/exact event, no queue writes, missing/old WAL schema no initialization"}))
}
