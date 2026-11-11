package core

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/ZanzyTHEbar/faults-go"
	"golang.org/x/sys/unix"
)

// One budget covers both standalone repo/base Git reads. A caller's shorter
// context (including the acceptance fixture) wins. CLI/project gitRead semantics
// are deliberately unchanged. This bounds subprocesses, not kernel filesystem IO.
const standaloneAgentGitTimeout = 15 * time.Second
const standaloneAgentGitWaitDelay = 250 * time.Millisecond

func canonicalStandaloneAgentSubmission(ctx context.Context, in AgentSubmission) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, standaloneAgentGitTimeout)
	defer cancel()
	return canonicalAgentSubmissionUsing(in, func(dir string, args ...string) (string, error) {
		// Fail closed on Git versions without --no-lazy-fetch; never silently
		// fall back to a validation read that may fetch from a promisor remote.
		if err := ctx.Err(); err != nil {
			return "", err
		}
		// Do not let Cmd.Wait (including CommandContext's watcher) reap the
		// leader before group cleanup. An unreaped leader reserves its PID/PGID,
		// even after a nonzero exit, so the group cannot be reused underneath us.
		c := exec.Command("git", append([]string{"--no-lazy-fetch"}, args...)...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		var output bytes.Buffer
		c.Stdout = &output
		// Git diagnostics may contain private paths/configuration. Drain them,
		// but retain only the existing safe exit/deadline diagnostic below.
		c.Stderr = io.Discard
		c.WaitDelay = standaloneAgentGitWaitDelay
		if err := c.Start(); err != nil {
			return "", diagnosticWrap(faults.CodeRuntimeFailed, "start standalone Git read validation", err)
		}
		exited := make(chan error, 1)
		go func() { exited <- standaloneGitWaitable(c.Process.Pid, unix.WEXITED|unix.WNOWAIT) }()
		var observeErr, stopErr error
		select {
		case observeErr = <-exited:
		case <-ctx.Done():
			stopErr = stopStandaloneGitGroup(c.Process.Pid)
			observeErr = <-exited
		}
		// ALL parent outcomes require cleanup, independently of the error that
		// Wait returns. In particular ExitError masks an inherited-pipe timeout.
		// No Wait has reaped the leader yet, and the ownership check never reaps.
		stopErr = errors.Join(stopErr, stopStandaloneGitGroup(c.Process.Pid))
		err := errors.Join(observeErr, stopErr, c.Wait())
		if err != nil {
			return "", diagnosticWrap(faults.CodeRuntimeFailed, "standalone Git read validation failed (bounded, no lazy fetch)", errors.Join(ctx.Err(), err))
		}
		if err = ctx.Err(); err != nil {
			return "", err
		}
		return strings.TrimSpace(output.String()), nil
	})
}

// The runner exclusively owns reaping this direct child. WNOWAIT leaves its
// identity reserved until the single Cmd.Wait after group cleanup. Retry EINTR;
// other ownership/observation errors are surfaced, never treated as clean exit.
func standaloneGitWaitable(pid, options int) error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, options, nil)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

func stopStandaloneGitGroup(pid int) error {
	// Verify that this is still our unreaped child before a numeric group
	// signal. On unexpected ownership loss (ECHILD), do NOT signal a reused
	// group. There is no delayed cancellation callback after Cmd.Wait.
	if err := standaloneGitWaitable(pid, unix.WEXITED|unix.WNOWAIT|unix.WNOHANG); err != nil {
		return err
	}
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil // reserved leader has no remaining signalable group members
	}
	return err
}
