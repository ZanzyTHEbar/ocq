package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A real failed Git parent leaves an explicitly synchronized descendant holding
// both output pipes. The fixture owns adoption/reaping, never a queue mock or
// production-wide reaper. It also cleans up on an expected baseline failure.
func TestStandaloneAgentGitInheritedPipes(t *testing.T) {
	if role := os.Getenv("OCQ_GIT_PIPE_ROLE"); role != "" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		if role == "parent" {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			child := exec.Command(exe, "-test.run", "^TestStandaloneAgentGitInheritedPipes$")
			child.Env = append(os.Environ(), "OCQ_GIT_PIPE_ROLE=descendant")
			child.Stdout, child.Stderr, child.ExtraFiles = os.Stdout, os.Stderr, []*os.File{w}
			if err = child.Start(); err != nil {
				t.Fatal(err)
			}
			_ = w.Close()
			var release [1]byte
			if _, err = io.ReadFull(r, release[:]); err != nil {
				t.Fatal(err)
			}
			os.Exit(71) // descendant retains stdout/stderr after this parent fails
		}
		if role != "descendant" {
			t.Fatal("unknown fixture role")
		}
		c, err := net.Dial("unix", os.Getenv("OCQ_GIT_PIPE_SOCKET"))
		if err != nil {
			t.Fatal(err)
		}
		start, err := startTicks(os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		parentStart, err := startTicks(os.Getppid())
		if err != nil {
			t.Fatal(err)
		}
		pgid, err := syscall.Getpgid(0)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.NewEncoder(c).Encode(struct {
			PID         int
			Start       uint64
			Parent      int
			ParentStart uint64
			PGID        int
		}{os.Getpid(), start, os.Getppid(), parentStart, pgid}); err != nil {
			t.Fatal(err)
		}
		var release [1]byte
		if _, err = io.ReadFull(c, release[:]); err != nil {
			t.Fatal(err)
		}
		ready := os.NewFile(3, "parent-release")
		if _, err = ready.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
		_ = ready.Close()
		// Only the test owns this connection; do not voluntarily exit after the
		// parent does. Keep both inherited descriptors until the runner kills us.
		_, _ = io.ReadFull(c, release[:])
		os.Exit(72)
	}
	root := os.Getenv("OCQ_GIT_PIPE_FIXTURE_ROOT")
	if root == "" {
		t.Skip("requires a fresh private persistent fixture root")
	}
	if !filepath.IsAbs(root) {
		t.Fatal("absolute root required")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal("fresh root required: ", err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// Applies only to this opt-in test process, not the host or any service.
	must(unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0))
	t.Cleanup(func() {
		if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 0, 0, 0, 0); err != nil {
			t.Error(err)
		}
	})
	exe, err := os.Executable()
	must(err)
	repo := filepath.Join(root, "repo")
	must(os.Mkdir(repo, 0700))
	for _, args := range [][]string{{"init", "-q", repo}, {"-C", repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "pipe fixture"}} {
		out, e := exec.Command("git", args...).CombinedOutput()
		if e != nil {
			t.Fatalf("Git: %v %s", e, out)
		}
	}
	base, err := gitRead(repo, "rev-parse", "HEAD")
	must(err)
	raw, err := canonicalAgentSubmission(AgentSubmission{Key: "failed-git", Repo: repo, Base: base, Prompt: []byte("Never execute."), Agent: "fixture-not-executed", Model: "fixture/never-run", Permissions: []byte(`[{"action":"*","resource":"*","effect":"deny"}]`)})
	must(err)
	q, err := OpenQueue(filepath.Join(root, "queue"))
	must(err)
	defer q.Close()
	submitter, err := OpenExistingAgentSubmitter(filepath.Join(root, "queue"))
	must(err)
	defer submitter.Close()
	bin := filepath.Join(root, "bin")
	must(os.Mkdir(bin, 0700))
	must(os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexport OCQ_GIT_PIPE_ROLE=parent\nexec '"+strings.ReplaceAll(exe, "'", "'\"'\"'")+"' -test.run '^TestStandaloneAgentGitInheritedPipes$'\n"), 0700))
	socket := filepath.Join(root, "pipes.sock")
	listener, err := net.Listen("unix", socket)
	must(err)
	defer listener.Close()
	must(listener.(*net.UnixListener).SetDeadline(time.Now().Add(5 * time.Second)))
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("OCQ_GIT_PIPE_SOCKET", socket)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	started := time.Now()
	go func() { _, err := submitter.submitPreparedAgent(ctx, "failed-git", raw); done <- err }()
	c, err := listener.Accept()
	must(err)
	defer c.Close()
	must(c.SetDeadline(time.Now().Add(5 * time.Second)))
	var owned struct {
		PID         int
		Start       uint64
		Parent      int
		ParentStart uint64
		PGID        int
	}
	must(json.NewDecoder(c).Decode(&owned))
	identity, err := json.Marshal(owned)
	must(err)
	must(os.WriteFile(filepath.Join(root, "owned.json"), identity, 0600))
	start, err := startTicks(owned.PID)
	must(err)
	if start != owned.Start || owned.PGID != owned.Parent {
		t.Fatal("descendant identity/group mismatch")
	}
	parentStart, err := startTicks(owned.Parent)
	must(err)
	if parentStart != owned.ParentStart {
		t.Fatal("parent identity changed")
	}
	pidfd, err := unix.PidfdOpen(owned.PID, 0)
	must(err)
	reaped, returned := false, false
	// Even the old buggy runner is safely cleaned up through the exact pidfd,
	// not a guessed group after its leader has been reaped.
	t.Cleanup(func() {
		defer unix.Close(pidfd)
		cancel()
		if !returned {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("fixture runner did not return")
			}
		}
		if !reaped {
			if err := unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, syscall.ESRCH) {
				t.Error(err)
			}
			var status unix.WaitStatus
			if pid, err := unix.Wait4(owned.PID, &status, 0, nil); err != nil {
				t.Error("fixture descendant reap: ", err)
			} else {
				if err := os.WriteFile(filepath.Join(root, "failure-cleanup.txt"), []byte(fmt.Sprintf("fixture cleanup reaped exact descendant PID=%d start=%d Wait4 status=%v\n", pid, owned.Start, status)), 0600); err != nil {
					t.Error(err)
				}
			}
		}
	})
	// The failed admission must not block an unrelated real write/transition.
	writeDone := make(chan error, 1)
	go func() {
		j, e := q.SubmitCommand("unrelated", repo, []string{"/usr/bin/true"}, 0)
		if e == nil {
			_, e = q.Cancel(j.ID)
		}
		writeDone <- e
	}()
	writeStarted := time.Now()
	select {
	case err = <-writeDone:
		must(err)
	case <-time.After(time.Second):
		t.Fatal("unrelated write blocked")
	}
	writeElapsed := time.Since(writeStarted)
	before, err := q.Jobs()
	must(err)
	if len(before) != 1 || before[0].State != "cancelled" {
		t.Fatal("unexpected queue before parent exit")
	}
	parentReleased := time.Now()
	_, err = c.Write([]byte{1})
	must(err)
	select {
	case err = <-done:
		returned = true
	case <-time.After(2 * time.Second):
		t.Fatal("failed parent pipe cleanup exceeded 2s")
	}
	elapsed := time.Since(parentReleased)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 71 {
		t.Fatalf("expected actual failed parent exit71: %v", err)
	}
	poll := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
	n, err := unix.Poll(poll, 1000)
	must(err)
	if n != 1 || poll[0].Revents&unix.POLLIN == 0 {
		t.Fatalf("owned descendant still alive after parent failure: pid=%d start=%d; runner returned in %s", owned.PID, owned.Start, elapsed)
	}
	var status unix.WaitStatus
	reapedPID, err := unix.Wait4(owned.PID, &status, 0, nil)
	must(err)
	reaped = true
	if reapedPID != owned.PID || !status.Signaled() || status.Signal() != unix.SIGKILL {
		t.Fatalf("descendant did not stop by cleanup: %v", status)
	}
	after, err := q.Jobs()
	must(err)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed validation changed queue")
	}
	var receipts, owners int
	must(q.store.db.QueryRow("SELECT count(*) FROM receipts WHERE key='failed-git'").Scan(&receipts))
	must(q.store.db.QueryRow("SELECT count(*) FROM agent_ownership").Scan(&owners))
	if receipts != 0 || owners != 0 {
		t.Fatal("failed validation admitted agent")
	}
	evidence := fmt.Sprintf("PASS parent PID=%d start=%d exit=71; descendant PID=%d start=%d PGID=%d pidfd exit observed, Wait4 SIGKILL and reaped; parent-release-to-return=%s total=%s unrelated-write+cancel=%s; queue unchanged, no agent receipt/ownership\n", owned.Parent, owned.ParentStart, owned.PID, owned.Start, owned.PGID, elapsed, time.Since(started), writeElapsed)
	must(os.WriteFile(filepath.Join(root, "result.txt"), []byte(evidence), 0600))
	t.Log(strings.TrimSpace(evidence))
}
