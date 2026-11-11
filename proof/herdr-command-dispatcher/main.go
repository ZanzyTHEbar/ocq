// One opt-in real native/SQLite command dispatcher acceptance fixture. Never
// dispatch an existing queue, contact an agent API or fake the queue authority.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"ocq/internal/core"
)

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func check(ok bool, s string) {
	if !ok {
		panic(s)
	}
}
func encode(v any) []byte { b, e := json.Marshal(v); must(e); return b }
func save(p string, v any) {
	f, e := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	must(e)
	_, e = f.Write(encode(v))
	must(e)
	must(f.Sync())
	must(f.Close())
}
func load(p string, v any) { b, e := os.ReadFile(p); must(e); must(json.Unmarshal(b, v)) }
func ticks(pid int) string {
	b, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	must(e)
	s := string(b)
	return strings.Fields(s[strings.LastIndex(s, ") ")+2:])[19]
}

type fixture struct {
	Jobs []core.Job `json:"jobs"`
}

func main() {
	root := os.Getenv("PROBE_ROOT")
	if filepath.Base(os.Args[0]) == "ocq-herdr" {
		nativeBridge(root)
		return
	}
	if filepath.Base(os.Args[0]) == "ocq-dispatch-race-gate" {
		nativeGate(root, os.Args[1:])
		return
	}
	mode := os.Args[1]
	if mode == "hold-command" {
		root = os.Args[2]
	}
	check(filepath.IsAbs(root), "private fixture root required")
	if strings.HasPrefix(mode, "cleanup-") {
		cleanupMode(root, mode, os.Args[2:])
		return
	}
	if mode == "native-install" {
		nativeInstall(root, os.Args[2])
		return
	}
	if mode == "native-run" {
		nativeRun(root, os.Args[2])
		return
	}
	if mode == "model-anchor" {
		check(len(os.Args) == 3 && (os.Args[2] == "origin" || os.Args[2] == "peer"), "explicit bounded model anchor role required")
		role := os.Args[2]
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
		defer stop()
		save(filepath.Join(root, "model-anchor-"+role+".json"), map[string]any{"pid": os.Getpid(), "start": ticks(os.Getpid()), "terminal": os.Getenv("HC_TERMINAL_ID"), "role": role, "argv": os.Args, "cwd": func() string { d, e := os.Getwd(); must(e); return d }()})
		fmt.Printf("OWNED DISPOSABLE %s MODEL ANCHOR — NO QUEUE DISPATCH — BOUNDED 120s\n", strings.ToUpper(role))
		select {
		case <-ctx.Done():
		case <-time.After(120 * time.Second):
		}
		return
	}
	if mode == "anchor" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
		defer stop()
		save(filepath.Join(root, "command-anchor-"+os.Getenv("HC_TERMINAL_ID")+".json"), map[string]any{"pid": os.Getpid(), "start": ticks(os.Getpid()), "terminal": os.Getenv("HC_TERMINAL_ID")})
		fmt.Println("OWNED DISPOSABLE COMMANDS-ONLY ACCEPTANCE — EXPLICIT QUEUE ONLY")
		select {
		case <-ctx.Done():
		case <-time.After(time.Hour):
		}
		return
	}
	if mode == "hold-command" {
		listener, e := net.Listen("unix", filepath.Join(root, "command-gate.sock"))
		must(e)
		defer listener.Close()
		save(filepath.Join(root, "command-gate-born.json"), map[string]any{"pid": os.Getpid(), "start": ticks(os.Getpid()), "parent": os.Getppid(), "job": os.Getenv("OCQ_JOB_ID"), "at": time.Now().Format(time.RFC3339Nano)})
		fmt.Println("REAL FOREGROUND COMMAND WAITING FOR EXPLICIT RELEASE")
		c, e := listener.Accept()
		must(e)
		defer c.Close()
		var b [7]byte
		_, e = io.ReadFull(c, b[:])
		must(e)
		check(string(b[:]) == "release", "unexpected gate input")
		fmt.Println("REAL FOREGROUND COMMAND RELEASED")
		return
	}
	if mode == "release" {
		c, e := net.Dial("unix", filepath.Join(root, "command-gate.sock"))
		must(e)
		_, e = c.Write([]byte("release"))
		must(e)
		must(c.Close())
		return
	}
	state := fixtureState(root)
	if mode == "setup" {
		_, e := os.Lstat(state)
		check(os.IsNotExist(e), "fresh never-used queue required; never retry uncertain fixture setup")
		repo := filepath.Join(root, "command-repo")
		must(os.Mkdir(repo, 0700))
		for _, args := range [][]string{{"init", "-q", repo}, {"-C", repo, "-c", "core.hooksPath=/dev/null", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "commands-only fixture"}} {
			out, e := exec.Command("git", args...).CombinedOutput()
			if e != nil {
				panic(fmt.Sprintf("Git %v: %s", e, out))
			}
		}
		q, e := core.OpenQueue(state)
		must(e)
		defer q.Close()
		exe, e := os.Executable()
		must(e)
		var f fixture
		j, e := q.SubmitCommand("native-command-printf", root, []string{"/usr/bin/printf", "%s\n", "harmless native commands-only"}, 0)
		must(e)
		f.Jobs = append(f.Jobs, j)
		j, e = q.SubmitCommand("native-command-drain-gate", root, []string{exe, "hold-command", root}, 0)
		must(e)
		f.Jobs = append(f.Jobs, j)
		j, e = q.SubmitAgent(core.AgentSubmission{Key: "native-agent-must-never-execute", Repo: repo, Base: "HEAD", Prompt: []byte("Never execute this sentinel."), Agent: "fixture-not-executed", Model: "fixture/never-run", Permissions: []byte(`[{"action":"*","resource":"*","effect":"deny"}]`)})
		must(e)
		f.Jobs = append(f.Jobs, j)
		j, e = q.SubmitCommand("native-command-after-agent-must-stay-queued", root, []string{"/usr/bin/printf", "must-not-run\n"}, 0)
		must(e)
		f.Jobs = append(f.Jobs, j)
		save(filepath.Join(root, "command-fixture.json"), f)
		fmt.Println("PASS fresh schema5 queue; four immutable receipts; zero dispatch")
		return
	}
	var f fixture
	load(filepath.Join(root, "command-fixture.json"), &f)
	if mode == "negative" {
		var status core.CommandDispatcherStatus
		load(filepath.Join(root, "dispatcher-ready.json"), &status)
		check(status.State == "ready" && status.Owner != nil && status.Busy, "owned ready dispatcher required")
		snapshot := func() []byte {
			files, e := os.ReadDir(filepath.Join(state, "herdr-command-dispatcher"))
			must(e)
			values := map[string]string{}
			for _, file := range files {
				if file.IsDir() {
					continue
				}
				b, e := os.ReadFile(filepath.Join(state, "herdr-command-dispatcher", file.Name()))
				must(e)
				values[file.Name()] = fmt.Sprintf("%x", sha256.Sum256(b))
			}
			return encode(values)
		}
		before := snapshot()
		_, e := core.PrepareCommandDispatcher(state, status.Target)
		check(e != nil, "second start accepted")
		foreign := status.Target
		foreign.Scope.Space = "spforeign-native-fixture"
		_, e = core.MarkCommandDispatcher(state, foreign, status.ID, "drain-prepare")
		check(e != nil, "foreign-space drain accepted")
		mismatch := status.Target
		mismatch.Scope.Queue = strings.Repeat("0", 64)
		_, e = core.MarkCommandDispatcher(state, mismatch, status.ID, "drain-attempt")
		check(e != nil, "mismatched queue drain accepted")
		_, e = core.MarkCommandDispatcher(state, status.Target, strings.Repeat("0", 64), "drain-attempt")
		check(e != nil, "foreign operation drain accepted")
		check(bytes.Equal(before, snapshot()), "refusal mutated lifecycle journal")
		check(ticks(status.Owner.PID) == status.Owner.Start, "owned dispatcher identity changed during refused drain")
		fmt.Println("PASS second start/singleton held, foreign-space/queue/operation drain refused; no lifecycle journal mutation or signal; owned process still live")
		return
	}
	r, e := core.OpenReader(state)
	must(e)
	defer r.Close()
	db, e := sql.Open("sqlite", "file:"+filepath.Join(state, "queue.db")+"?mode=ro")
	must(e)
	defer db.Close()
	var jobs []core.Job
	for _, original := range f.Jobs {
		var canonical []byte
		var digest, key, status, validation string
		must(db.QueryRow("SELECT r.canonical,r.digest,r.key,j.state,j.validation FROM receipts r JOIN jobs j ON j.id=r.job_id WHERE r.job_id=?", original.ID).Scan(&canonical, &digest, &key, &status, &validation))
		check(bytes.Equal(canonical, original.Request) && digest == original.Digest && key == original.Key, "immutable receipt changed")
		j := original
		j.State = status
		j.Validation = validation
		jobs = append(jobs, j)
	}
	var n int
	must(db.QueryRow("SELECT count(*) FROM agent_operations").Scan(&n))
	check(n == 0, "agent execution operation")
	must(db.QueryRow("SELECT count(*) FROM agent_ownership WHERE phase<>'queued'").Scan(&n))
	check(n == 0, "agent provisioning")
	check(jobs[2].State == "queued" && jobs[3].State == "queued", "agent/FIFO tail executed")
	for _, j := range jobs {
		check(j.Validation == "not_run", "invented validation")
	}
	if mode == "prestart" {
		for _, j := range jobs {
			check(j.State == "queued", "prepare/context test started execution")
		}
		must(db.QueryRow("SELECT count(*) FROM jobs WHERE pid<>0 OR stdout<>'' OR stderr<>''").Scan(&n))
		check(n == 0, "prestart launch intent")
		must(db.QueryRow("SELECT count(*) FROM completion_notifications").Scan(&n))
		check(n == 0, "completion before admission")
		fmt.Println("PASS no job launch intent or process before confirmed native start")
		return
	}
	var gate struct {
		PID    int    `json:"pid"`
		Parent int    `json:"parent"`
		Job    string `json:"job"`
	}
	load(filepath.Join(root, "command-gate-born.json"), &gate)
	check(gate.Job == f.Jobs[1].ID, "gate process/receipt mismatch")
	check(jobs[0].State == "finished", "printf did not finish")
	var stdout string
	var exit sql.NullInt64
	must(db.QueryRow("SELECT stdout,exit_code FROM jobs WHERE id=?", f.Jobs[0].ID).Scan(&stdout, &exit))
	b, e := os.ReadFile(stdout)
	must(e)
	check(string(b) == "harmless native commands-only\n" && exit.Valid && exit.Int64 == 0, "printf output/exit mismatch")
	if mode == "running" {
		check(jobs[1].State == "running", "real command not running")
		check(ticks(gate.PID) != "", "gate process unavailable")
		fmt.Println("PASS actual printf executed; real gate running; agent/FIFO tail queued; no agent operations; immutable receipts")
		return
	}
	check(mode == "verify", "explicit fixture phase required")
	check(jobs[1].State == "finished", "drain did not finish real gate")
	var jobPID int
	must(db.QueryRow("SELECT pid,exit_code FROM jobs WHERE id=?", f.Jobs[1].ID).Scan(&jobPID, &exit))
	check(jobPID == gate.PID && exit.Valid && exit.Int64 == 0, "gate exit/process mismatch")
	must(db.QueryRow("SELECT count(*) FROM completion_notifications").Scan(&n))
	check(n == 2, "completion notification count")
	must(db.QueryRow("SELECT count(*) FROM completion_notifications WHERE acknowledged_at IS NOT NULL").Scan(&n))
	check(n == 0, "implicit notification acknowledgement")
	var reason string
	must(db.QueryRow("SELECT reason FROM jobs WHERE id=?", f.Jobs[2].ID).Scan(&reason))
	check(strings.Contains(reason, "command-only dispatcher: agent at FIFO head"), "commands-only FIFO gate not observed")
	fmt.Println("PASS graceful core drain; printf/gate exit0 not_run; agent/FIFO tail remain queued; receipts unchanged; zero agent operations; exactly two completion events")
}
