package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

const observationLifecycleProtocol = "ocq-observation-lifecycle-v1"
const observationReceiptLimit = 4096

// Resource evidence for one foreground read, never job/capacity authority.
// Retention is capped and fail-closed; unresolved evidence is never evicted.
type observationReadKey struct {
	Protocol string `json:"protocol"`
	ID       string `json:"read_id"`
	Command  string `json:"command"`
	Request  string `json:"request_sha256"`
	Queue    string `json:"queue_identity"`
	Store    string `json:"store_context"`
	Bridge   string `json:"bridge_context"`
	Image    string `json:"image_sha256"`
	Space    string `json:"space"`
	Folder   string `json:"folder"`
}

type observationReadReceipt struct {
	Key       observationReadKey `json:"key"`
	PID       int                `json:"pid"`
	Start     string             `json:"start_ticks"`
	Boot      string             `json:"boot_id"`
	Namespace string             `json:"pid_namespace"`
	Outcome   string             `json:"outcome"`
}

func observationProcessStart(pid int) (string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	end := strings.LastIndex(string(raw), ") ")
	if end < 0 {
		return "", errors.New("process identity malformed")
	}
	fields := strings.Fields(string(raw)[end+2:])
	if len(fields) < 20 {
		return "", errors.New("process identity incomplete")
	}
	n, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || n == 0 {
		return "", errors.New("process start unavailable")
	}
	return strconv.FormatUint(n, 10), nil
}

func observationHost() (string, string, error) {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", "", err
	}
	ns, err := os.Readlink("/proc/self/ns/pid")
	if err != nil || len(strings.TrimSpace(string(boot))) != 36 || !strings.HasPrefix(ns, "pid:[") {
		return "", "", errors.New("observation host identity unavailable")
	}
	return strings.TrimSpace(string(boot)), ns, nil
}

func observationPrivate(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	mode := os.FileMode(0600)
	if directory {
		mode = os.ModeDir | 0700
	}
	if !ok || int(st.Uid) != os.Getuid() || info.Mode() != mode {
		return errors.New("observation receipt storage must be private, owned and nonsymlink")
	}
	return nil
}

func observationReceiptDir(create bool) (string, error) {
	dir, err := existingBridgeStateDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "observation-reads-v1")
	if create {
		if err := os.Mkdir(dir, 0700); err == nil {
			parent, err := os.Open(filepath.Dir(dir))
			if err != nil {
				return "", err
			}
			err = parent.Sync()
			parent.Close()
			if err != nil {
				return "", err
			}
		} else if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return dir, observationPrivate(dir, true)
}

func observationReadIdentity(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func observationReadBinding(k observationReadKey, b modCompatibility) bool {
	return k.Protocol == observationLifecycleProtocol && observationReadIdentity(k.ID) &&
		observationReadIdentity(k.Request) && (k.Command == "watch-wait" || k.Command == "view-snapshot") &&
		k.Queue != "" && k.Queue == b.Queue && k.Store == b.ObservationStoreContext &&
		k.Bridge == b.ObservationContext && k.Image == b.Image && k.Space == b.Space && k.Folder == b.Folder
}

func writeObservationReceipt(f *os.File, r observationReadReceipt) error {
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > 8192 {
		return errors.New("observation receipt exceeds bound")
	}
	if err = f.Truncate(0); err != nil {
		return err
	}
	if _, err = f.WriteAt(raw, 0); err != nil {
		return err
	}
	return f.Sync()
}

func retainObservationRead(cmd *cobra.Command, raw string, b modCompatibility, id string) (*os.File, observationReadReceipt, error) {
	r := observationReadReceipt{Key: observationReadKey{
		Protocol: observationLifecycleProtocol, ID: id, Command: cmd.Name(),
		Request: compatHash([]byte(cmd.Name() + "\n" + raw)), Queue: b.Queue, Store: b.ObservationStoreContext,
		Bridge: b.ObservationContext, Image: b.Image, Space: b.Space, Folder: b.Folder,
	}, PID: os.Getpid(), Outcome: "running"}
	if !observationReadBinding(r.Key, b) {
		return nil, r, errors.New("observation receipt requires exact existing queue/context")
	}
	var err error
	r.Boot, r.Namespace, err = observationHost()
	if err != nil {
		return nil, r, err
	}
	r.Start, err = observationProcessStart(r.PID)
	if err != nil {
		return nil, r, err
	}
	dir, err := observationReceiptDir(true)
	if err != nil {
		return nil, r, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, r, err
	}
	defer lock.Close()
	if err = observationPrivate(lock.Name(), false); err != nil {
		return nil, r, err
	}
	// No waiting on another reader: ambiguous or concurrent allocation blocks.
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, r, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	d, err := os.Open(dir)
	if err != nil {
		return nil, r, err
	}
	defer d.Close()
	names, err := d.Readdirnames(observationReceiptLimit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, r, err
	}
	if len(names) >= observationReceiptLimit+1 {
		return nil, r, errors.New("observation receipt retention full; explicit cleanup required")
	}
	f, err := os.OpenFile(filepath.Join(dir, id+".json"), os.O_CREATE|os.O_EXCL|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, r, err
	} // ID reuse is never an execution retry.
	if err = writeObservationReceipt(f, r); err == nil {
		err = d.Sync()
	}
	if err != nil {
		f.Close()
		return nil, r, err
	}
	return f, r, nil
}

func withObservationReceipt(run func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) (result error) {
		id, err := cmd.Flags().GetString("observation-read-id")
		if err != nil {
			return err
		}
		if id == "" {
			return run(cmd, args)
		} // Old clients remain unrecoverable.
		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		defer cancel()
		b, err := observationBinding(ctx, cmd)
		if err != nil {
			return err
		}
		f, receipt, err := retainObservationRead(cmd, args[0], b, id)
		if err != nil {
			return err
		}
		defer f.Close()
		defer func() {
			if err := writeObservationReceipt(f, receipt); err != nil {
				result = errors.New("terminal observation receipt unavailable")
			}
		}()
		receipt.Outcome = "error"
		result = run(cmd, args)
		if result == nil {
			receipt.Outcome = "returned"
		}
		return result
	}
}

func observationReadStatus() *cobra.Command {
	return &cobra.Command{Use: "observation-read-status EXACT_KEY_JSON", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if len(args[0]) > 8192 {
			return errors.New("observation status exceeds bound")
		}
		var key observationReadKey
		decoder := json.NewDecoder(strings.NewReader(args[0]))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&key); err != nil {
			return errors.New("invalid observation status key")
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return errors.New("invalid observation status suffix")
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Second)
		defer cancel()
		b, err := observationBinding(ctx, cmd)
		if err != nil || !observationReadBinding(key, b) {
			return errors.New("observation status context mismatch")
		}
		dir, err := observationReceiptDir(false) // Never creates storage or a lock.
		if err != nil {
			return errors.New("observation receipt unavailable")
		}
		path := filepath.Join(dir, key.ID+".json")
		if err = observationPrivate(path, false); err != nil {
			return errors.New("observation receipt unavailable")
		}
		f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return errors.New("observation receipt unavailable")
		}
		defer f.Close()
		raw, err := io.ReadAll(io.LimitReader(f, 8193))
		if err != nil || len(raw) > 8192 {
			return errors.New("observation receipt exceeds bound")
		}
		var receipt observationReadReceipt
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(&receipt) != nil || d.Decode(&extra) != io.EOF || receipt.Key != key ||
			receipt.PID <= 0 || receipt.Start == "" || (receipt.Outcome != "returned" && receipt.Outcome != "error") {
			return errors.New("terminal observation receipt not established")
		}
		ticks, err := strconv.ParseUint(receipt.Start, 10, 64)
		if err != nil || ticks == 0 || strconv.FormatUint(ticks, 10) != receipt.Start {
			return errors.New("invalid retained process start")
		}
		boot, ns, err := observationHost()
		if err != nil || boot != receipt.Boot || ns != receipt.Namespace {
			return errors.New("observation host changed or unknown")
		}
		start, err := observationProcessStart(receipt.PID)
		if err == nil && start == receipt.Start {
			return errors.New("original observation process still present")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("original observation process unobservable")
		}
		if errors.Is(err, os.ErrNotExist) && !errors.Is(syscall.Kill(receipt.PID, 0), syscall.ESRCH) {
			return errors.New("original observation process absence not established")
		}
		// A zombie with the original start identity stays blocked too.
		return writeObservation(cmd, struct {
			Protocol string             `json:"protocol"`
			Key      observationReadKey `json:"key"`
			Status   string             `json:"status"`
		}{observationLifecycleProtocol, key, "original_process_exited"})
	}}
}
