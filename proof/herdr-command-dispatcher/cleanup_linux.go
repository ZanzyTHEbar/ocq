package main

// Fixture-only cleanup. Retained startup roots and their observed descendants
// are the inventory; never discover signal targets by scanning the host.
import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type cleanupIdentity struct {
	Root, Boot, Start, Executable, DeviceInode, Argv string
	PID                                              int
}

// Only a polled, pinned descriptor can produce this result. Identity failures
// (including ENOENT from a live process's /proc entries) are never absence.
var errCleanupExited = errors.New("pinned fixture process exited")

func cleanupAlive(fd int) (bool, error) {
	p := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, e := unix.Poll(p, 0)
	return n == 0, e
}

func cleanupObserve(root string, pid int) (cleanupIdentity, error) {
	i := cleanupIdentity{Root: root, PID: pid}
	boot, e := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if e != nil {
		return i, e
	}
	i.Boot = strings.TrimSpace(string(boot))
	base := fmt.Sprintf("/proc/%d/", pid)
	process, e := os.Stat(base)
	if e != nil {
		return i, e
	}
	if process.Sys().(*syscall.Stat_t).Uid != uint32(os.Getuid()) {
		return i, errors.New("owned fixture process required")
	}
	stat, e := os.ReadFile(base + "stat")
	if e != nil {
		return i, e
	}
	fields := strings.Fields(string(stat)[strings.LastIndex(string(stat), ") ")+2:])
	if len(fields) < 20 {
		return i, errors.New("incomplete process identity")
	}
	i.Start = fields[19]
	env, e := os.ReadFile(base + "environ")
	if e != nil {
		return i, e
	}
	has := func(s string) bool {
		for _, v := range bytes.Split(env, []byte{0}) {
			if string(v) == s {
				return true
			}
		}
		return false
	}
	if !has("HOME="+filepath.Join(root, "home")) || !has("XDG_RUNTIME_DIR="+filepath.Join(root, "run")) {
		return i, errors.New("exact private fixture environment linkage required")
	}
	f, e := os.Open(base + "exe")
	if e != nil {
		return i, e
	}
	info, e := f.Stat()
	_ = f.Close()
	if e != nil {
		return i, e
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return i, errors.New("executable identity unavailable")
	}
	i.DeviceInode = fmt.Sprintf("%d:%d", st.Dev, st.Ino)
	i.Executable, e = os.Readlink(base + "exe")
	if e != nil {
		return i, e
	}
	args, e := os.ReadFile(base + "cmdline")
	if e != nil {
		return i, e
	}
	i.Argv = string(args)
	return i, nil
}

// Open first, then verify /proc against the retained identity and poll again.
// Even if /proc observes a reused numeric PID, this descriptor cannot signal it.
func cleanupOpen(root string, want cleanupIdentity) (int, error) {
	if want.Root != root || want.PID <= 1 {
		return -1, errors.New("invalid retained fixture linkage")
	}
	fd, e := unix.PidfdOpen(want.PID, 0)
	if e != nil {
		return -1, e
	}
	fail := func(e error) (int, error) { _ = unix.Close(fd); return -1, e }
	alive, e := cleanupAlive(fd)
	if e != nil {
		return fail(e)
	}
	if !alive {
		return fail(errCleanupExited)
	}
	got, e := cleanupObserve(root, want.PID)
	if e != nil {
		// Never retry a denied identity read. An exited pinned descriptor needs
		// no signal; a live or ambiguous one still preserves the original error.
		if alive, pollErr := cleanupAlive(fd); pollErr == nil && !alive {
			return fail(errCleanupExited)
		}
		return fail(e)
	}
	if got != want {
		return fail(errors.New("retained fixture process identity changed; signal refused"))
	}
	alive, e = cleanupAlive(fd)
	if e != nil {
		return fail(e)
	}
	if !alive {
		return fail(errCleanupExited)
	}
	return fd, nil
}

func cleanupCapture(root string, pid int) (cleanupIdentity, error) {
	fd, e := unix.PidfdOpen(pid, 0)
	if e != nil {
		return cleanupIdentity{}, e
	}
	defer unix.Close(fd)
	alive, e := cleanupAlive(fd)
	if e != nil {
		return cleanupIdentity{}, e
	}
	if !alive {
		return cleanupIdentity{}, errCleanupExited
	}
	i, e := cleanupObserve(root, pid)
	if e != nil {
		if alive, pollErr := cleanupAlive(fd); pollErr == nil && !alive {
			return i, errCleanupExited
		}
		return i, e
	}
	alive, e = cleanupAlive(fd)
	if e != nil {
		return i, e
	}
	if !alive {
		return i, errCleanupExited
	}
	return i, nil
}

func cleanupInventory(root string, roots []cleanupIdentity) ([]cleanupIdentity, error) {
	seen := map[int]bool{}
	var records []cleanupIdentity
	var visit func(cleanupIdentity, int) error
	visit = func(i cleanupIdentity, parent int) error {
		if seen[i.PID] {
			return nil
		}
		seen[i.PID] = true
		if len(seen) > 256 {
			return errors.New("fixture descendant bound exceeded")
		}
		fd, e := cleanupOpen(root, i)
		if errors.Is(e, errCleanupExited) {
			if parent == 0 {
				return errors.New("retained fixture root exited before descendant inventory; ownership incomplete")
			}
			return nil
		}
		if e != nil {
			return e
		}
		defer unix.Close(fd)
		if parent != 0 {
			b, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", i.PID))
			if e != nil {
				return e
			}
			fields := strings.Fields(string(b)[strings.LastIndex(string(b), ") ")+2:])
			if len(fields) < 2 || fields[1] != strconv.Itoa(parent) {
				return errors.New("exact fixture ancestry changed")
			}
		}
		paths, e := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/children", i.PID))
		if e != nil {
			return e
		}
		if len(paths) > 1024 {
			return errors.New("fixture thread bound exceeded")
		}
		for _, p := range paths {
			b, e := os.ReadFile(p)
			if os.IsNotExist(e) {
				continue
			}
			if e != nil {
				return e
			}
			for _, s := range strings.Fields(string(b)) {
				pid, e := strconv.Atoi(s)
				if e != nil {
					return e
				}
				child, e := cleanupCapture(root, pid)
				if errors.Is(e, errCleanupExited) {
					continue
				}
				if e != nil {
					return e
				}
				// The pinned parent must still exist after reading its child list.
				alive, e := cleanupAlive(fd)
				if e != nil {
					return e
				}
				if !alive {
					return errors.New("fixture ancestry changed during inventory")
				}
				if e = visit(child, i.PID); e != nil {
					return e
				}
			}
		}
		records = append(records, i)
		return nil // descendants before their parent
	}
	for _, i := range roots {
		if e := visit(i, 0); e != nil {
			return nil, e
		}
	}
	return records, nil
}

// Exact-target primitive only: an exited snapshot is NOT proof of fixture-wide
// shutdown. Only the launch-time subreaper owner can establish that boundary.
func cleanupExact(root string, records []cleanupIdentity, end time.Time) error {
	var fds []int
	defer func() {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
	}()
	// Validate the entire retained inventory before issuing any signal.
	for _, i := range records {
		fd, e := cleanupOpen(root, i)
		if errors.Is(e, errCleanupExited) {
			continue
		}
		if e != nil {
			return e
		}
		fds = append(fds, fd)
	}
	for _, fd := range fds {
		if time.Now().After(end) {
			return errors.New("fixture shutdown deadline reached; ownership uncertain")
		}
		if e := unix.PidfdSendSignal(fd, unix.SIGTERM, nil, 0); e != nil && !errors.Is(e, syscall.ESRCH) {
			return e
		}
		// Observe each child's actual exit before stopping its parent: the HC
		// holder must not recover a core that cleanup has already terminated.
		for {
			alive, e := cleanupAlive(fd)
			if e != nil {
				return e
			}
			if !alive {
				break
			}
			if time.Now().After(end) {
				return errors.New("owned fixture process still alive; no force-kill fallback")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	return nil
}

func cleanupMode(root, mode string, args []string) {
	s, e := os.Lstat(root)
	must(e)
	check(filepath.IsAbs(root) && s.IsDir() && s.Mode().Perm() == 0700 && s.Sys().(*syscall.Stat_t).Uid == uint32(os.Getuid()), "private owned persistent fixture root required")
	switch mode {
	case "cleanup-private-proof", "cleanup-private-scenario", "cleanup-private-child":
		cleanupPrivateMode(root, mode)
	case "cleanup-record":
		check(len(args) == 2, "cleanup-record PID RECEIPT")
		pid, e := strconv.Atoi(args[0])
		must(e)
		i, e := cleanupCapture(root, pid)
		must(e)
		save(args[1], i)
	case "cleanup-inventory":
		check(len(args) == 4, "cleanup-inventory OUTPUT ROOT_RECEIPT ROOT_RECEIPT ROOT_RECEIPT")
		var roots []cleanupIdentity
		for _, path := range args[1:] {
			var i cleanupIdentity
			load(path, &i)
			roots = append(roots, i)
		}
		records, e := cleanupInventory(root, roots)
		must(e)
		save(args[0], records)
	case "cleanup-signal":
		panic("snapshot-only shutdown cannot account for late/reparented descendants; use launch-time cleanup-owner")
	case "cleanup-owner":
		check(len(args) == 4, "cleanup-owner NATIVE_SCRIPT APP HC NETWORK_GUARD")
		c := exec.Command("/bin/sh", args[0], "--owned", root, args[1], args[2], args[3])
		must(cleanupOwned(root, c))
		fmt.Println("PASS cleanup: verified owned lineage empty, kernel ECHILD; pidfd signals only")
	case "cleanup-late-proof":
		cleanupLateProof(root)
	case "cleanup-late-script", "cleanup-late-parent", "cleanup-late-child":
		cleanupLateProcess(root, mode)
	case "cleanup-probe":
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		defer signal.Stop(ch)
		save(filepath.Join(root, "cleanup-probe-ready.json"), os.Getpid())
		select {
		case s := <-ch:
			save(filepath.Join(root, "cleanup-probe-signal.json"), s.String())
		case <-time.After(15 * time.Second):
		}
	case "cleanup-proof":
		must(os.Mkdir(filepath.Join(root, "home"), 0700))
		must(os.Mkdir(filepath.Join(root, "run"), 0700))
		self, e := os.Executable()
		must(e)
		c := exec.Command(self, "cleanup-probe")
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "HOME=") && !strings.HasPrefix(v, "XDG_RUNTIME_DIR=") {
				c.Env = append(c.Env, v)
			}
		}
		c.Env = append(c.Env, "HOME="+filepath.Join(root, "home"), "XDG_RUNTIME_DIR="+filepath.Join(root, "run"))
		must(c.Start())
		waitNative("harmless cleanup probe ready", func() bool { return exists(filepath.Join(root, "cleanup-probe-ready.json")) })
		i, e := cleanupCapture(root, c.Process.Pid)
		must(e)
		save(filepath.Join(root, "cleanup-exact.json"), []cleanupIdentity{i})
		wrong := i
		wrong.Start = "0"
		save(filepath.Join(root, "cleanup-mismatch.json"), []cleanupIdentity{wrong})
		check(cleanupExact(root, []cleanupIdentity{wrong}, time.Now().Add(5*time.Second)) != nil, "mismatched identity signalled")
		check(!exists(filepath.Join(root, "cleanup-probe-signal.json")), "signal delivered on mismatch")
		fd, e := cleanupOpen(root, i)
		must(e)
		must(unix.Close(fd))
		must(cleanupExact(root, []cleanupIdentity{i}, time.Now().Add(5*time.Second)))
		must(c.Wait())
		check(exists(filepath.Join(root, "cleanup-probe-signal.json")), "exact cleanup TERM not observed")
		fmt.Println("PASS one real harmless process: retained start mismatch refused without signal; exact boot/start/executable/argv/private-fixture match received pidfd TERM and exited0")
	default:
		panic("unsupported cleanup mode")
	}
}
