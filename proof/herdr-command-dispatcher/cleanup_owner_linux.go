package main

// One bounded fixture owner, not a general supervisor: no restart, service,
// adoption by names/paths, or recovery of a fixture launched without this owner.
import (
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

func cleanupChildren() ([]int, error) {
	paths, e := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/children", os.Getpid()))
	if e != nil {
		return nil, e
	}
	if len(paths) == 0 || len(paths) > 1024 {
		return nil, errors.New("owned child inventory unavailable or thread bound exceeded")
	}
	seen := map[int]bool{}
	var children []int
	for _, path := range paths {
		b, e := os.ReadFile(path)
		if os.IsNotExist(e) {
			continue
		} // a Go runtime thread ended; wait4 is the final barrier
		if e != nil {
			return nil, e
		}
		for _, field := range strings.Fields(string(b)) {
			pid, e := strconv.Atoi(field)
			if e != nil {
				return nil, e
			}
			if !seen[pid] {
				seen[pid] = true
				children = append(children, pid)
			}
		}
	}
	if len(children) > 256 {
		return nil, errors.New("owned child bound exceeded")
	}
	return children, nil
}

// Called only after Cmd.Wait has reaped the one launched script. wait4(-1) is
// kernel-scoped to this owner, not a host process scan; it cannot reap a peer.
func cleanupReap() (bool, error) {
	for reaped := 0; reaped < 256; reaped++ {
		var status unix.WaitStatus
		pid, e := unix.Wait4(-1, &status, unix.WNOHANG, nil)
		if errors.Is(e, syscall.EINTR) {
			continue
		}
		if errors.Is(e, syscall.ECHILD) {
			return true, nil
		}
		if e != nil {
			return false, e
		}
		if pid == 0 {
			return false, nil
		}
	}
	return false, nil // yield to the bounded inventory/deadline loop during churn
}

func cleanupOwned(root string, c *exec.Cmd) (result error) {
	// Process-local kernel ownership MUST precede every fixture runtime launch.
	if e := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); e != nil {
		return e
	}
	save(filepath.Join(root, "cleanup-owner.json"), map[string]any{"pid": os.Getpid(), "start": ticks(os.Getpid()), "root": root, "ownership": "launch_time_subreaper"})
	env := c.Env
	if env == nil {
		env = os.Environ()
	}
	c.Env = nil
	for _, value := range env {
		if !strings.HasPrefix(value, "OCQ_FIXTURE_OWNER_PID=") {
			c.Env = append(c.Env, value)
		}
	}
	c.Env = append(c.Env, "OCQ_FIXTURE_OWNER_PID="+strconv.Itoa(os.Getpid()))
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	defer func() {
		if result != nil {
			children, inventoryErr := cleanupChildren()
			save(filepath.Join(root, "cleanup-uncertain.json"), map[string]any{"error": result.Error(), "children": children, "inventory_error": fmt.Sprint(inventoryErr), "complete": false})
		}
	}()
	if e := c.Run(); e != nil {
		// No success authorization from the native driver: it may still own a
		// live dispatcher. Do not force-close it or label its outcome complete.
		return fmt.Errorf("fixture driver failed; shutdown uncertain, no automatic signalling: %w", e)
	}
	end := time.Now().Add(10 * time.Second)
	for pass := 0; pass < 256 && time.Now().Before(end); pass++ {
		_, e := cleanupReap()
		if e != nil {
			return e
		}
		children, e := cleanupChildren()
		if e != nil {
			return e
		}
		if len(children) == 0 {
			empty, e := cleanupReap()
			if e != nil {
				return e
			}
			if empty {
				// No remaining ancestor can fork: all fixture descendants have
				// either been reaped here or were reaped by their own parent.
				save(filepath.Join(root, "cleanup-final.json"), map[string]any{"owner_pid": os.Getpid(), "children": []int{}, "wait4": "ECHILD", "complete": true})
				return nil
			}
		}
		var roots []cleanupIdentity
		for _, pid := range children {
			// A PID from this owner's kernel child list proves lineage. The
			// private environment is an additional check, never discovery.
			i, e := cleanupCapture(root, pid)
			if errors.Is(e, errCleanupExited) {
				continue
			}
			if e != nil {
				return e
			}
			roots = append(roots, i)
		}
		if len(roots) != 0 {
			records, e := cleanupInventory(root, roots)
			if e != nil {
				return e
			}
			save(filepath.Join(root, fmt.Sprintf("cleanup-pass-%03d.json", pass)), records)
			if e := cleanupExact(root, records, end); e != nil {
				return e
			}
		}
		// TERM can spawn a replacement; exit can reparent an unobserved child.
		// The kernel keeps both under this owner for the next inventory pass.
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("bounded fixture shutdown incomplete; descendants/ownership uncertain")
}

func cleanupLateCommand(root, mode string) *exec.Cmd {
	self, e := os.Executable()
	must(e)
	c := exec.Command(self, mode)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "HOME=") && !strings.HasPrefix(value, "XDG_RUNTIME_DIR=") {
			c.Env = append(c.Env, value)
		}
	}
	c.Env = append(c.Env, "HOME="+filepath.Join(root, "home"), "XDG_RUNTIME_DIR="+filepath.Join(root, "run"))
	return c
}

func cleanupLateProcess(root, mode string) {
	if mode == "cleanup-late-script" {
		c := cleanupLateCommand(root, "cleanup-late-parent")
		must(c.Start())
		waitNative("late parent ready", func() bool { return exists(filepath.Join(root, "late-parent-ready.json")) })
		i, e := cleanupCapture(root, c.Process.Pid)
		must(e)
		save(filepath.Join(root, "late-initial-inventory.json"), []cleanupIdentity{i})
		// Exit deliberately without waiting: the launch-time owner adopts the
		// live parent before initiating TERM. No child exists in this snapshot.
		return
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)
	defer signal.Stop(ch)
	if mode == "cleanup-late-parent" {
		save(filepath.Join(root, "late-parent-ready.json"), os.Getpid())
		select {
		case <-ch:
			check(exists(filepath.Join(root, "late-initial-inventory.json")), "TERM before initial inventory")
			save(filepath.Join(root, "late-parent-term.json"), os.Getpid())
			c := cleanupLateCommand(root, "cleanup-late-child")
			must(c.Start())
			waitNative("late child readiness", func() bool { return exists(filepath.Join(root, "late-child-ready.json")) })
			// Child stays alive and is reparented to the owner when we exit.
		case <-time.After(15 * time.Second):
		}
		return
	}
	i, e := cleanupCapture(root, os.Getpid())
	must(e)
	save(filepath.Join(root, "late-child-ready.json"), i)
	select {
	case <-ch:
		save(filepath.Join(root, "late-child-term.json"), os.Getpid())
	case <-time.After(15 * time.Second):
	}
}

func cleanupLateProof(root string) {
	must(os.Mkdir(filepath.Join(root, "home"), 0700))
	must(os.Mkdir(filepath.Join(root, "run"), 0700))
	must(cleanupOwned(root, cleanupLateCommand(root, "cleanup-late-script")))
	var initial []cleanupIdentity
	load(filepath.Join(root, "late-initial-inventory.json"), &initial)
	var child cleanupIdentity
	load(filepath.Join(root, "late-child-ready.json"), &child)
	check(len(initial) == 1 && initial[0].PID != child.PID, "late child already in initial snapshot")
	passes, e := filepath.Glob(filepath.Join(root, "cleanup-pass-*.json"))
	must(e)
	detected := false
	for _, path := range passes {
		var records []cleanupIdentity
		load(path, &records)
		for _, i := range records {
			if i == child {
				detected = true
			}
		}
	}
	check(detected, "late child missing from verified shutdown inventories")
	check(exists(filepath.Join(root, "late-parent-term.json")) && exists(filepath.Join(root, "late-child-term.json")), "late child was not safely signalled")
	children, e := cleanupChildren()
	must(e)
	empty, e := cleanupReap()
	must(e)
	check(len(children) == 0 && empty, "late-child fixture leaked a runtime")
	fmt.Println("PASS synchronized real late child: born after initial inventory during TERM, reparented to launch-time owner, pidfd-cleaned; final inventory empty and kernel ECHILD")
}
