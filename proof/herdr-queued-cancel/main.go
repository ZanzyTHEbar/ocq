// One private real bridge/SQLite/commands-only cancellation scenario.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"ocq/internal/core"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func check(ok bool, why string) {
	if !ok {
		panic(why)
	}
}

// Native-only scheduler barrier, not a bridge or queue substitute. The retained
// mod copy runs the real callbacks and real bridge after this bounded pause.
// Copy this executable as ocq-cancel-callback-gate on the private core PATH and
// set OCQ_CANCEL_GATE_DIR to its private gate directory; never use a live core.
// Create prepare.hold/arm.hold, wait for .entered, change Herdr context, then
// create .release. trace records cleanup even on the abandoned callback object.
func callbackGate(args []string) {
	check(len(args) >= 2 && (args[0] == "enter" || args[0] == "settled"), "invalid native callback gate")
	phase, stage := args[0], args[1]
	check(stage == "prepare" || stage == "arm" || stage == "status" || stage == "review", "invalid callback stage")
	dir := os.Getenv("OCQ_CANCEL_GATE_DIR")
	check(filepath.IsAbs(dir), "explicit private callback gate directory required")
	if phase == "enter" && (stage == "prepare" || stage == "arm") {
		if _, err := os.Stat(filepath.Join(dir, stage+".hold")); err == nil {
			must(os.WriteFile(filepath.Join(dir, stage+".entered"), nil, 0600))
			deadline := time.Now().Add(3 * time.Minute)
			for {
				if _, err := os.Stat(filepath.Join(dir, stage+".release")); err == nil {
					break
				}
				check(time.Now().Before(deadline), "native callback gate timed out")
				time.Sleep(20 * time.Millisecond)
			}
		}
	}
	f, err := os.OpenFile(filepath.Join(dir, "trace"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	must(err)
	_, err = fmt.Fprintln(f, strings.Join(args, " "))
	must(err)
	must(f.Close())
}

func nativeMod(source, root string) string {
	dir := filepath.Join(root, "native-mod")
	must(os.Mkdir(dir, 0700))
	for _, name := range []string{"main.js", "queue.js", "queued-cancel.js", "command-resolution.js", "agent-reconcile.js", "worktree-check.js", "result-publication.js", "private-frames.js", "mod.json"} {
		b, err := os.ReadFile(filepath.Join(source, name))
		must(err)
		if name == "queued-cancel.js" {
			parts := strings.Split(string(b), "  send(owner, cx) {")
			check(len(parts) == 2, "native cancellation instrumentation boundary changed")
			for i, stage := range []string{"action", `"arm"`} {
				header := "cx.spawn(async (cx) => {\n      try {"
				check(strings.Count(parts[i], header) == 1, "native callback boundary changed")
				parts[i] = strings.Replace(parts[i], header, header+"\n        "+`const gate = await exec("ocq-cancel-callback-gate", ["--callback-gate", "enter", `+stage+`], { on: null });`+"\n        "+`if (gate.code !== 0) throw new Error("Native callback gate failed");`, 1)
				cleanup := "this.working = false;"
				if i == 1 {
					cleanup += " this.confirm = false;"
				}
				check(strings.Count(parts[i], cleanup) == 1, "native cleanup boundary changed")
				parts[i] = strings.Replace(parts[i], cleanup, cleanup+` await exec("ocq-cancel-callback-gate", ["--callback-gate", "settled", `+stage+`, String(this.working)], { on: null });`, 1)
			}
			b = []byte(strings.Join(parts, "  send(owner, cx) {"))
		}
		must(os.WriteFile(filepath.Join(dir, name), b, 0600))
	}
	return dir
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--callback-gate" {
		callbackGate(os.Args[2:])
		return
	}
	if len(os.Args) == 3 && os.Args[1] == "--hold" {
		for i := 0; i < 200; i++ {
			if _, err := os.Stat(os.Args[2]); err == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		panic("probe release timeout")
	}
	check(len(os.Args) == 3 || len(os.Args) == 4 && os.Args[3] == "--fixture" || len(os.Args) == 5 && os.Args[3] == "--native-fixture", "usage: herdr-queued-cancel BRIDGE FRESH_PRIVATE_ROOT [--fixture | --native-fixture SOURCE_MOD]")
	bridge, err := filepath.Abs(os.Args[1])
	must(err)
	root, err := filepath.Abs(os.Args[2])
	must(err)
	must(os.Mkdir(root, 0700))
	state := filepath.Join(root, "queue")
	q, err := core.OpenQueue(state)
	must(err)
	defer q.Close()
	self, err := os.Executable()
	must(err)
	seed, err := q.SubmitCommand("seed", root, []string{self, "--hold", filepath.Join(root, "seed-release")}, 0)
	must(err)
	_, err = q.Cancel(seed.ID)
	must(err)
	db, err := sql.Open("sqlite", filepath.Join(state, "queue.db"))
	must(err)
	defer db.Close()
	_, err = db.Exec("UPDATE sqlite_sequence SET seq=9007199254740992 WHERE name='receipts'")
	must(err)
	target, err := q.SubmitCommand("cancel-target", root, []string{self, "--hold", filepath.Join(root, "must-not-run")}, 0)
	must(err)
	racer, err := q.SubmitCommand("admission-racer", root, []string{self, "--hold", filepath.Join(root, "release")}, 0)
	must(err)
	spare, err := q.SubmitCommand("untouched", root, []string{self, "--hold", filepath.Join(root, "spare-release")}, 1<<62)
	must(err)
	_, err = db.Exec(`CREATE TABLE proof_cancel_updates(job_id TEXT);
 CREATE TRIGGER proof_cancel_update AFTER UPDATE OF state ON jobs WHEN NEW.state='cancelled'
 BEGIN INSERT INTO proof_cancel_updates VALUES(NEW.id); END;`)
	must(err)
	call := func(args ...string) ([]byte, error) {
		c := exec.Command(bridge, args...)
		c.Env = append(os.Environ(), "OCQ_STATE_DIR="+state)
		return c.Output()
	}
	var detail core.QueueJobOutput
	b, err := call("queue-job", target.ID)
	must(err)
	must(json.Unmarshal(b, &detail))
	s := core.QueuedCancelScope{Machine: "local", Space: "sp-cancel-fixture", Queue: detail.QueueIdentity, Job: target.ID, Sequence: detail.Job.Sequence, DisplayedState: "queued"}
	check(s.Sequence == "9007199254740993", "receipt precision lost")
	if len(os.Args) >= 4 {
		out := map[string]any{"state_dir": state, "scope": s, "racer": racer.ID, "spare": spare.ID}
		if len(os.Args) == 5 {
			out["native_mod"] = nativeMod(os.Args[4], root)
		}
		must(json.NewEncoder(os.Stdout).Encode(out))
		return
	}
	args := func(action string, s core.QueuedCancelScope, id string) []string {
		a := []string{"queued-cancel-" + action, s.Space, s.Queue, s.Job, s.Sequence, s.DisplayedState}
		if id != "" {
			a = append(a, id)
		}
		return a
	}
	read := func(action string, s core.QueuedCancelScope, id string) core.QueuedCancelStatus {
		b, err := call(args(action, s, id)...)
		must(err)
		var out core.QueuedCancelStatus
		must(json.Unmarshal(b, &out))
		return out
	}
	count := func() int {
		var n int
		must(db.QueryRow("SELECT count(*) FROM proof_cancel_updates").Scan(&n))
		return n
	}
	spareSnapshot := func() []any {
		rows, err := db.Query("SELECT j.*,r.* FROM jobs j JOIN receipts r ON j.id=r.job_id WHERE j.id=?", spare.ID)
		must(err)
		defer rows.Close()
		columns, err := rows.Columns()
		must(err)
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		check(rows.Next(), "spare missing")
		must(rows.Scan(pointers...))
		must(rows.Err())
		return values
	}
	originalSpare := spareSnapshot()
	check(read("status", s, "").State == "none" && count() == 0, "view consumed cancellation")
	prepared := read("prepare", s, "")
	check(prepared.State == "prepared" && count() == 0, "prepare mutated job")
	// A fresh bridge process represents reload. Merely reading retained intent
	// cannot arm it, and a second space cannot establish another authorization.
	check(read("status", s, "").State == "prepared", "reload lost intent")
	wrong := s
	wrong.Space = "sp-other"
	check(read("status", wrong, "").State == "blocked", "other space adopted intent")
	_, err = call(args("arm", wrong, prepared.IntentID)...)
	check(err != nil && count() == 0, "wrong-context arm mutated")
	wrong = s
	wrong.Queue = strings.Repeat("0", 64)
	_, err = call(args("prepare", wrong, "")...)
	check(err != nil, "wrong queue accepted")
	wrong = s
	wrong.Sequence = "9007199254740994"
	_, err = call(args("prepare", wrong, "")...)
	check(err != nil, "wrong receipt accepted")
	wrong = s
	wrong.Machine = "remote"
	_, err = core.ArmQueuedCancellation(state, wrong, prepared.IntentID)
	check(err != nil && count() == 0, "remote scope armed local cancellation")
	check(read("arm", s, prepared.IntentID).State == "armed", "claim not persisted")
	check(read("status", s, "").State == "armed" && count() == 0, "reload dispatched armed claim")
	// Drop the successful response at a real subprocess boundary. Recovery is a
	// separate bridge invocation and cannot issue another mutation.
	c := exec.Command("/bin/sh", "-c", `"$@" >/dev/null || exit $?; exit 73`, "lost-response", bridge)
	c.Args = append(c.Args, args("attempt", s, prepared.IntentID)...)
	c.Env = append(os.Environ(), "OCQ_STATE_DIR="+state)
	err = c.Run()
	check(err != nil && c.ProcessState.ExitCode() == 73, "response-loss fixture did not commit")
	out := read("status", s, "")
	check(out.State == "cancelled_observed" && out.Observation.Completion != nil && count() == 1, "exact cancellation/completion not observed")
	_, err = call(args("attempt", s, prepared.IntentID)...)
	check(err != nil && count() == 1, "duplicate attempt retried cancellation")
	_, err = q.Cancel(target.ID)
	check(err != nil && count() == 1, "CLI repeat semantics changed")
	check(reflect.DeepEqual(originalSpare, spareSnapshot()), "cancellation affected another stored job or receipt")
	got, err := q.Job(target.ID)
	must(err)
	check(got.Digest == target.Digest && reflect.DeepEqual(got.Request, target.Request) && got.Process.PID == 0, "receipt changed or cancelled command launched")
	// Prepare/arm while queued, then admit a real harmless command first. The
	// dispatcher wins; neither the typed writer nor delayed attempt may cancel it.
	rs := s
	rs.Job = racer.ID
	rs.Sequence = fmt.Sprint(racer.Sequence)
	rp := read("prepare", rs, "")
	check(read("arm", rs, rp.IntentID).State == "armed", "race claim missing")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- q.Dispatch(ctx, 1, 0, true, nil) }()
	defer func() { _ = os.WriteFile(filepath.Join(root, "release"), nil, 0600); cancel(); <-done }()
	for {
		j, err := q.Job(racer.ID)
		must(err)
		if j.State == "running" {
			break
		}
		check(ctx.Err() == nil, "race command did not launch")
		time.Sleep(20 * time.Millisecond)
	}
	writer, err := core.OpenExistingQueuedCanceller(state)
	must(err)
	err = writer.Cancel(rs)
	must(writer.Close())
	check(err != nil, "typed writer cancelled running work")
	out = read("attempt", rs, rp.IntentID)
	check(out.State == "not_queued" && out.Observation.Job.State == "running" && out.Observation.Completion == nil && count() == 1, "race falsely claimed cancellation")
	j, err := q.Job(racer.ID)
	must(err)
	check(j.State == "running", "cancellation stopped running process")
	must(os.WriteFile(filepath.Join(root, "release"), nil, 0600))
	for {
		j, err = q.Job(racer.ID)
		must(err)
		if j.State == "finished" {
			break
		}
		check(ctx.Err() == nil, "probe did not finish")
		time.Sleep(20 * time.Millisecond)
	}
	out = read("status", rs, "")
	check(out.State == "not_queued" && out.Observation.Job.State == "finished" && out.Observation.Completion.State == "finished", "race actual outcome lost")
	missing := filepath.Join(root, "missing")
	_, err = core.OpenExistingQueuedCanceller(missing)
	check(err != nil, "writer initialized missing queue")
	_, err = os.Stat(missing)
	check(os.IsNotExist(err), "missing writer created directory")
	must(json.NewEncoder(os.Stdout).Encode(map[string]any{"result": "PASS", "state_dir": state, "checks": "queued cancellation and completion; exact >2^53 receipt; durable prepare/arm; wrong context/queue/receipt; dropped response and fresh-process read-only recovery; one SQL cancellation; repeat refused; other job unchanged; real dispatcher win preserved; missing writer noninitializing"}))
}
