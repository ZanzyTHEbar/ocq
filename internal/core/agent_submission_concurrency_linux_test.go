package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// One real admission scenario: control only the Git executable boundary, never
// the queue/SQLite or receipt writer. No dispatcher, agent or remote is started.
func TestStandaloneAgentAdmissionConcurrency(t *testing.T) {
	if os.Getenv("OCQ_ADMISSION_GIT_CHILD") == "1" {
		c, err := net.Dial("unix", os.Getenv("OCQ_ADMISSION_GIT_SOCKET"))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprintf(c, "%d\n", os.Getpid())
		var release [1]byte
		if _, err = io.ReadFull(c, release[:]); err != nil {
			t.Fatal(err)
		}
		_ = c.Close()
		args := os.Args
		for len(args) > 0 && args[0] != "--" {
			args = args[1:]
		}
		if len(args) < 2 {
			t.Fatal("Git arguments missing")
		}
		git := os.Getenv("OCQ_ADMISSION_REAL_GIT")
		if err = syscall.Exec(git, append([]string{git}, args[1:]...), os.Environ()); err != nil {
			t.Fatal(err)
		}
		return
	}
	root := os.Getenv("OCQ_ADMISSION_CONCURRENCY_ROOT")
	if root == "" {
		t.Skip("requires a fresh owned persistent private fixture root")
	}
	if !filepath.IsAbs(root) {
		t.Fatal("absolute root required")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal("fresh root required: ", err)
	}
	t.Log("retained evidence:", root)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	realGit, err := exec.LookPath("git")
	must(err)
	exe, err := os.Executable()
	must(err)
	repo := filepath.Join(root, "repo")
	must(os.Mkdir(repo, 0700))
	for _, args := range [][]string{{"init", "-q", repo}, {"-C", repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "admission fixture"}} {
		out, e := exec.Command(realGit, args...).CombinedOutput()
		if e != nil {
			t.Fatalf("Git %v: %s", e, out)
		}
	}
	base, err := gitRead(repo, "rev-parse", "HEAD")
	must(err)
	in := AgentSubmission{Key: "timeout", Repo: repo, Base: base, Prompt: []byte("Never execute this fixture."), Agent: "fixture-not-executed", Model: "fixture/never-run", Permissions: []byte(`[{"action":"*","resource":"*","effect":"deny"}]`)}
	raw, err := canonicalAgentSubmission(in)
	must(err)
	must(os.WriteFile(filepath.Join(root, "canonical.json"), raw, 0600))
	conflict := bytes.Replace(raw, []byte("Never execute"), []byte("Different never execute"), 1)
	state := filepath.Join(root, "queue")
	q, err := OpenQueue(state)
	must(err)
	defer q.Close()
	w, err := OpenExistingAgentSubmitter(state)
	must(err)
	defer w.Close()
	competitor, err := OpenExistingAgentSubmitter(state)
	must(err)
	defer competitor.Close()
	bin := filepath.Join(root, "bin")
	must(os.Mkdir(bin, 0700))
	// exec preserves Git's process-group identity, allowing exact timeout cleanup.
	must(os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexport OCQ_ADMISSION_GIT_CHILD=1\nexec '"+strings.ReplaceAll(exe, "'", "'\"'\"'")+"' -test.run '^TestStandaloneAgentAdmissionConcurrency$' -- \"$@\"\n"), 0700))
	socket := filepath.Join(root, "git.sock")
	listener, err := net.Listen("unix", socket)
	must(err)
	defer listener.Close()
	pidLog, err := os.OpenFile(filepath.Join(root, "git-pids.txt"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	must(err)
	defer pidLog.Close()
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("OCQ_ADMISSION_REAL_GIT", realGit)
	t.Setenv("OCQ_ADMISSION_GIT_SOCKET", socket)
	type result struct {
		job Job
		err error
	}
	start := func(key string, budget time.Duration) (<-chan result, context.CancelFunc) {
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		done := make(chan result, 1)
		go func() { j, e := w.submitPreparedAgent(ctx, key, raw); done <- result{j, e} }()
		return done, cancel
	}
	ready := func() (net.Conn, int) {
		t.Helper()
		must(listener.(*net.UnixListener).SetDeadline(time.Now().Add(5 * time.Second)))
		c, e := listener.Accept()
		must(e)
		must(c.SetDeadline(time.Now().Add(5 * time.Second)))
		var pid int
		_, e = fmt.Fscanln(c, &pid)
		must(e)
		_, e = fmt.Fprintln(pidLog, pid)
		must(e)
		t.Cleanup(func() { _ = c.Close() })
		return c, pid
	}
	finish := func(done <-chan result) result {
		t.Helper()
		select {
		case r := <-done:
			return r
		case <-time.After(6 * time.Second):
			t.Fatal("admission did not finish bounded")
			return result{}
		}
	}
	done, cancel := start("timeout", 3*time.Second)
	defer cancel()
	c, pid := ready()
	defer c.Close()
	// Git has explicitly signalled entry and cannot proceed until released.
	// Exercise actual unrelated admission AND a terminal queue transition now.
	writeDone := make(chan error, 1)
	go func() {
		j, e := q.SubmitCommand("unrelated", repo, []string{"/usr/bin/true"}, 0)
		if e == nil {
			_, e = q.Cancel(j.ID)
		}
		writeDone <- e
	}()
	select {
	case e := <-writeDone:
		must(e)
	case <-time.After(time.Second):
		t.Fatal("unrelated queue write blocked behind Git")
	}
	r := finish(done)
	if !errors.Is(r.err, context.DeadlineExceeded) {
		t.Fatalf("expected bounded Git timeout, got %v", r.err)
	}
	if e := syscall.Kill(pid, 0); !errors.Is(e, syscall.ESRCH) {
		t.Fatalf("owned Git PID %d survived: %v", pid, e)
	}
	var n int
	must(q.store.db.QueryRow("SELECT count(*) FROM receipts WHERE key='timeout'").Scan(&n))
	if n != 0 {
		t.Fatal("failed validation inserted receipt")
	}
	jobs, err := q.Jobs()
	must(err)
	if len(jobs) != 1 || jobs[0].State != "cancelled" {
		t.Fatal("failed validation inserted job or unrelated transition failed")
	}
	t.Log("PASS synchronized stalled Git: unrelated admission/cancel <1s; 3s deadline; PID reaped; no failed receipt/job")
	// A second real prepared writer validates and wins the same key while the
	// first validator is blocked. Recover exact winners, refuse conflicting ones.
	for _, key := range []string{"matching", "conflicting", "matching-failed-validation"} {
		done, cancel = start(key, 5*time.Second)
		defer cancel()
		c, pid = ready()
		winnerRaw := raw
		if key == "conflicting" {
			winnerRaw = conflict
		}
		winnerDone := make(chan result, 1)
		winnerContext, winnerCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer winnerCancel()
		go func() {
			j, e := competitor.submitPreparedAgent(winnerContext, key, winnerRaw)
			winnerDone <- result{j, e}
		}()
		for i := 0; i < 2; i++ {
			other, _ := ready()
			_, err = other.Write([]byte{1})
			must(err)
			must(other.Close())
		}
		winner := finish(winnerDone)
		must(winner.err)
		if key == "matching-failed-validation" {
			// Even a validation error must recheck the competing receipt.
			cancel()
		} else {
			_, err = c.Write([]byte{1})
			must(err)
			must(c.Close())
			c, _ = ready()
			_, err = c.Write([]byte{1})
			must(err)
		}
		r = finish(done)
		must(c.Close())
		if e := syscall.Kill(pid, 0); !errors.Is(e, syscall.ESRCH) {
			t.Fatal("competing Git process survived")
		}
		if key != "conflicting" {
			if r.err != nil || r.job.ID != winner.job.ID || r.job.Digest != winner.job.Digest {
				t.Fatalf("matching winner not recovered: %+v %v", r.job, r.err)
			}
		} else if r.err == nil {
			t.Fatal("conflicting winner accepted")
		}
		must(q.store.db.QueryRow("SELECT count(*) FROM receipts WHERE key=?", key).Scan(&n))
		if n != 1 {
			t.Fatal("duplicate receipt")
		}
	}
	// Fresh success through the bounded runner and real Git (release each of the
	// two actual reads), then lose the response and remove the repository.
	done, cancel = start("offline", 5*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		c, _ = ready()
		_, err = c.Write([]byte{1})
		must(err)
		must(c.Close())
	}
	r = finish(done)
	must(r.err)
	must(os.Rename(repo, repo+"-moved"))
	recovered, err := w.SubmitPreparedAgent("offline", raw)
	must(err)
	if recovered.ID != r.job.ID || recovered.Digest != sha256Hex(raw) || recovered.State != "queued" || recovered.Validation != "not_run" {
		t.Fatal("offline exact receipt recovery changed")
	}
	reader, err := OpenReader(state)
	must(err)
	defer reader.Close()
	view, err := reader.AgentSubmissionReceipt("offline", raw)
	must(err)
	if view.Job.ID != recovered.ID {
		t.Fatal("receipt linkage mismatch")
	}
	if _, err = w.SubmitPreparedAgent("offline", conflict); err == nil {
		t.Fatal("offline conflict accepted")
	}
	must(q.store.db.QueryRow("SELECT count(*) FROM agent_operations").Scan(&n))
	if n != 0 {
		t.Fatal("execution side effect")
	}
	must(q.store.db.QueryRow("SELECT count(*) FROM agent_ownership").Scan(&n))
	if n != 4 {
		t.Fatal("ownership duplicated")
	}
	t.Log("PASS competing exact/conflicting immutable writers; failed validator recovers exact winner; bounded real Git success; lost-response offline recovery; one ownership per receipt, zero execution")
}
