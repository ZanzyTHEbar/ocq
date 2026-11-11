package main

// One real CLI-boundary scenario, not a fabricated API/queue. The relay emits
// genuine installed CLI output, then leaves a known child holding both pipes.
import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"ocq/internal/core"
)

func pipeMode() bool {
	if len(os.Args) < 3 || (os.Args[1] != "--pipe-parent" && os.Args[1] != "--pipe-child") {
		return false
	}
	root := os.Getenv("PROBE_ROOT")
	check(filepath.IsAbs(root) && os.Getenv("HOME") == filepath.Join(root, "home"), "private pipe fixture required")
	phase := os.Args[2]
	check(phase == "version" || phase == "session", "unknown pipe phase")
	if os.Args[1] == "--pipe-child" {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		write(filepath.Join(root, "pipe-child-"+phase+".json"), map[string]any{"pid": os.Getpid(), "holds": "stdout_and_stderr"})
		select {
		case <-ch:
			write(filepath.Join(root, "pipe-child-"+phase+"-term.json"), map[string]any{"pid": os.Getpid(), "signal": "TERM"})
		case <-time.After(75 * time.Second):
			panic("pipe child timed out without owned cleanup")
		}
		return true
	}
	check(len(os.Args) > 3, "real CLI argv required")
	c := exec.Command(filepath.Join(root, "bin", "opencode-real"), os.Args[3:]...)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	must(c.Run()) // genuine version or exact-session response, before pipe hold
	self, e := os.Executable()
	must(e)
	child := exec.Command(self, "--pipe-child", phase)
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	must(child.Start())
	wait(func() bool { _, e := os.Stat(filepath.Join(root, "pipe-child-"+phase+".json")); return e == nil })
	write(filepath.Join(root, "pipe-parent-"+phase+".json"), map[string]any{"pid": os.Getpid(), "child_pid": child.Process.Pid, "real_cli": "exited0", "parent_outcome": map[string]string{"version": "exit0", "session": "await_request_deadline"}[phase]})
	if phase == "session" {
		time.Sleep(60 * time.Second)
		panic("backend deadline did not kill pipe parent")
	}
	return true
}

func pipeProof(bridge string, t core.AgentReconcileTarget, service string) {
	root := os.Getenv("PROBE_ROOT")
	var evidence []map[string]any
	for _, scenario := range []struct {
		phase, want string
		min, max    time.Duration
	}{
		{"version", "unsupported_client", 200 * time.Millisecond, 5 * time.Second},
		{"session", "session_unavailable", 14 * time.Second, 18 * time.Second},
	} {
		must(os.WriteFile(filepath.Join(root, "pipe-phase"), []byte(scenario.phase), 0600))
		start := time.Now()
		c := exec.Command(bridge, "agent-handoff-verify", t.Space, t.Queue, t.Job, t.Sequence, t.Session, t.Worktree, service)
		b, e := c.Output()
		elapsed := time.Since(start)
		must(e)
		var s core.AgentHandoff
		must(json.Unmarshal(b, &s))
		check(s.Observation == scenario.want, "held pipe falsely verified or wrong refusal")
		check(elapsed >= scenario.min && elapsed < scenario.max, "backend inherited-pipe bound failed")
		var child struct {
			PID int `json:"pid"`
		}
		read(filepath.Join(root, "pipe-child-"+scenario.phase+".json"), &child)
		_, e = os.Stat(fmt.Sprintf("/proc/%d/stat", child.PID))
		must(e) // still held for launch-time owner, never silently leaked
		evidence = append(evidence, map[string]any{"phase": scenario.phase, "elapsed_ms": float64(elapsed.Microseconds()) / 1000, "observation": s.Observation, "child_pid": child.PID, "child_alive_at_return": true})
	}
	b, e := os.ReadFile(filepath.Join(root, "pipe-cli.trace"))
	must(e)
	check(string(b) == "version\nversion\nsession_get\n", "CLI retried or ran a command after refusal/deadline")
	check(!strings.Contains(string(b), "DENIED"), "non-GET pipe fixture invocation")
	write(filepath.Join(root, "pipe-results.json"), evidence)
	fmt.Println("PASS real CLI inherited stdout/stderr: exited version parent and deadline-killed exact GET parent both refuse boundedly; no subsequent CLI command; children retained for launch-time owner cleanup")
}
