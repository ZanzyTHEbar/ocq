package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/ZanzyTHEbar/faults-go"
)

type Process struct {
	PID   int    `json:"pid"`
	Start uint64 `json:"start_ticks"`
	Boot  string `json:"boot_id"`
}

func bootID() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", classifyDiagnostic(faults.CodeRuntimeFailed, err)
	}
	id := strings.TrimSpace(string(b))
	if id == "" {
		return "", diagnostic(faults.CodeRuntimeFailed, "empty boot identity")
	}
	return id, nil
}

func startTicks(pid int) (uint64, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, classifyDiagnostic(faults.CodeRuntimeFailed, err)
	}
	end := strings.LastIndex(string(b), ") ")
	if end < 0 {
		return 0, diagnostic(faults.CodeRuntimeFailed, "malformed process identity")
	}
	fields := strings.Fields(string(b)[end+2:])
	if len(fields) < 20 {
		return 0, diagnostic(faults.CodeRuntimeFailed, "incomplete process identity")
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	return start, classifyDiagnostic(faults.CodeRuntimeFailed, err)
}

// Refuse to release ownership if either the exact leader or its process group
// still exists. Process groups are not containment: commands must stay foreground.
func safeToResolve(p Process) error {
	boot, err := bootID()
	if err != nil {
		return err
	}
	if p.Boot == "" {
		return diagnostic(faults.CodeRuntimeFailed, "missing boot identity; resolution refused")
	}
	if p.Boot != "" && p.Boot != boot {
		return nil // Old-boot processes cannot still be running.
	}
	if p.PID == 0 || p.Start == 0 {
		return diagnostic(faults.CodeRuntimeFailed, "unrecorded launch identity on this boot: resolution refused; requires reboot before reconciliation")
	}
	start, err := startTicks(p.PID)
	if err == nil && start == p.Start {
		return diagnostic(faults.CodeRuntimeFailed, "owned process identity still exists; resolution refused")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return diagnosticWrap(faults.CodeRuntimeFailed, "cannot establish process exit", err)
	}
	if err := syscall.Kill(-p.PID, 0); err == nil || !errors.Is(err, syscall.ESRCH) {
		return diagnostic(faults.CodeRuntimeFailed, "process group still exists or is unobservable; resolution refused")
	}
	return nil
}

func memAvailable() (int64, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, classifyDiagnostic(faults.CodeRuntimeFailed, err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[0] == "MemAvailable:" && f[2] == "kB" {
			n, err := strconv.ParseInt(f[1], 10, 64)
			if err == nil && n >= 0 && n <= (1<<63-1)/1024 {
				return n * 1024, nil
			}
		}
	}
	return 0, diagnostic(faults.CodeRuntimeFailed, "Linux MemAvailable unavailable or invalid")
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	defer f.Close()
	return classifyDiagnostic(faults.CodeStorageFailed, f.Sync())
}

// Sync newly created directory entries, including ancestry on first use.
func privateDir(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		parent := filepath.Dir(path)
		if err := ensureParent(parent); err != nil {
			return err
		}
		if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return classifyDiagnostic(faults.CodeStorageFailed, err)
		}
		if err := syncDir(parent); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() || !info.IsDir() || info.Mode() != os.ModeDir|0700 {
		return diagnostic(faults.CodeConfigInvalid, fmt.Sprintf("state/artifact directory must be owned, nonsymlink, mode 0700: %s", path))
	}
	return nil
}

func ensureParent(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	return privateDir(path)
}

func privateFile(path string, flags int) (*os.File, error) {
	f, err := os.OpenFile(path, flags|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	info, err := f.Stat()
	if err == nil {
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(st.Uid) != os.Getuid() || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			err = diagnostic(faults.CodeConfigInvalid, fmt.Sprintf("file must be owned regular mode 0600: %s", path))
		}
	}
	if err != nil {
		f.Close()
		return nil, classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	return f, nil
}

func (s *store) lock() (*os.File, error) {
	f, err := privateFile(filepath.Join(s.dir, "dispatch.lock"), os.O_CREATE|os.O_RDWR)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, diagnosticWrap(faults.CodeResourceLimit, "dispatcher lock held (or unavailable)", err)
	}
	return f, nil
}
