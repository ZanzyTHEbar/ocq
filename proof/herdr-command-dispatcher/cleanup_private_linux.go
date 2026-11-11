package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// A real process-local identity-denial boundary. No host policy changes,
// alternate /proc reader or signal to the unreadable process is used.
func cleanupPrivateMode(root, mode string) {
	self, e := os.Executable()
	must(e)
	switch mode {
	case "cleanup-private-proof":
		must(os.Mkdir(filepath.Join(root, "home"), 0700))
		must(os.Mkdir(filepath.Join(root, "run"), 0700))
		must(cleanupOwned(root, exec.Command(self, "cleanup-private-scenario")))
		fmt.Println("PASS live pinned identity denial propagates; child exits naturally; empty inventory/ECHILD")
	case "cleanup-private-child":
		must(os.WriteFile(filepath.Join(root, "identity-ready"), nil, 0600))
		waitNative("identity restriction release", func() bool { return exists(filepath.Join(root, "identity-restrict")) })
		must(unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0))
		must(os.WriteFile(filepath.Join(root, "identity-unreadable"), nil, 0600))
		time.Sleep(2 * time.Second)
	case "cleanup-private-scenario":
		c := exec.Command(self, "cleanup-private-child")
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "HOME=") && !strings.HasPrefix(v, "XDG_RUNTIME_DIR=") {
				c.Env = append(c.Env, v)
			}
		}
		c.Env = append(c.Env, "HOME="+filepath.Join(root, "home"), "XDG_RUNTIME_DIR="+filepath.Join(root, "run"))
		must(c.Start())
		// Always wait for this bounded child; identity loss never authorizes TERM.
		defer func() { must(c.Wait()) }()
		waitNative("identity-ready", func() bool { return exists(filepath.Join(root, "identity-ready")) })
		i, e := cleanupCapture(root, c.Process.Pid)
		must(e)
		save(filepath.Join(root, "identity-owner.json"), i)
		fd, e := unix.PidfdOpen(c.Process.Pid, 0)
		must(e)
		defer unix.Close(fd)
		must(os.WriteFile(filepath.Join(root, "identity-restrict"), nil, 0600))
		waitNative("identity denial ready", func() bool { return exists(filepath.Join(root, "identity-unreadable")) })
		alive, e := cleanupAlive(fd)
		must(e)
		check(alive, "identity proof child exited too soon")
		_, captureErr := cleanupCapture(root, i.PID)
		_, openErr := cleanupOpen(root, i)
		_, inventoryErr := cleanupInventory(root, []cleanupIdentity{i})
		alive, e = cleanupAlive(fd)
		must(e)
		check(alive, "identity probe not live after denial")
		for _, err := range []error{captureErr, openErr, inventoryErr} {
			check(err != nil && !errors.Is(err, errCleanupExited), "live identity denial silently treated as exit")
		}
		save(filepath.Join(root, "identity-denial.json"), map[string]any{"pid": i.PID, "live_pinned_before_and_after": true, "capture": captureErr.Error(), "open": openErr.Error(), "inventory": inventoryErr.Error(), "signals": 0, "exit": "bounded_natural"})
	default:
		panic("unknown private identity proof")
	}
}
