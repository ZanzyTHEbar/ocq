package core

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ZanzyTHEbar/faults-go"
	"golang.org/x/sys/unix"
	"ocq/internal/core/dbquery"
)

type ObservationWaitRequest struct {
	Protocol      string                 `json:"protocol"`
	QueueIdentity string                 `json:"queue_identity"`
	StoreContext  string                 `json:"store_context"`
	BridgeContext string                 `json:"bridge_context"`
	Revision      string                 `json:"revision"`
	WaitSeconds   int                    `json:"wait_seconds"`
	Log           *ObservationLogCursor  `json:"log,omitempty"`
	Work          *WorkObservationCursor `json:"work,omitempty"`
}

type ObservationWaitOutput struct {
	Protocol        string                `json:"protocol"`
	Status          string                `json:"status"`
	QueueIdentity   string                `json:"queue_identity"`
	StoreContext    string                `json:"store_context"`
	Schema          int                   `json:"schema"`
	Revision        string                `json:"revision"`
	ReadAt          string                `json:"read_at"`
	Watch           string                `json:"watch"`
	DatabaseChanged bool                  `json:"database_changed"`
	LogChanged      bool                  `json:"log_changed"`
	WorkChanged     bool                  `json:"work_changed"`
	ResetScope      string                `json:"reset_scope,omitempty"`
	Log             *ObservationLogOutput `json:"log,omitempty"`
}

func DecodeObservationWaitRequest(raw []byte) (ObservationWaitRequest, error) {
	var req ObservationWaitRequest
	if _, err := decodeStrictJSONObject(raw, ObservationRequestBytes, &req); err != nil {
		return req, err
	}
	return req, req.validate()
}

func (req ObservationWaitRequest) validate() error {
	if req.Protocol != ObservationProtocol || req.WaitSeconds < 1 || req.WaitSeconds > 20 || (req.QueueIdentity != "" && !isValidHexLen(req.QueueIdentity, 64)) {
		return faults.New(faults.CodeValidationFailed, "invalid observation wait protocol, queue identity or 1..20 second lease")
	}
	_, err := observationDecimal(req.Revision)
	if !isValidHexLen(req.StoreContext, 64) || (req.BridgeContext != "" && !isValidHexLen(req.BridgeContext, 64)) {
		return faults.New(faults.CodeValidationFailed, "wait requires pinned store context")
	}
	if req.Log != nil {
		if e := req.Log.validate(); e != nil {
			return e
		}
	}
	if req.Work != nil {
		if e := req.Work.validate(true); e != nil {
			return e
		}
	}
	if req.QueueIdentity == "" && req.Revision != "0" {
		return faults.New(faults.CodeValidationFailed, "missing queue wait requires revision zero")
	}
	return err
}

// observationWatchTarget follows only the next path component. It does not
// create directories/lock files, recursively watch ancestors or equate EACCES
// with missing state. After a creation hint the caller advances the watch and
// rechecks the database, closing the install/check race at every level.
func observationWatchTarget(dir string) (string, string, error) {
	target := dir
	for {
		info, err := os.Stat(target)
		if err == nil {
			if !info.IsDir() {
				return "", "", errors.New("observation ancestor is not a directory")
			}
			if target == dir {
				return target, "", nil
			}
			rel, err := filepath.Rel(target, dir)
			if err != nil {
				return "", "", err
			}
			return target, strings.Split(rel, string(filepath.Separator))[0], nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", "", err
		}
		parent := filepath.Dir(target)
		if parent == target {
			return "", "", err
		}
		target = parent
	}
}

// ObserveWait is a bounded foreground lease, not a daemon or commit detector.
// Inotify is only a hint. Each decision comes from a fresh short read snapshot;
// there is no open SQLite connection or transaction while blocked in Poll.
// A final read at expiry covers dropped hints and unreliable notification mounts.
func ObserveWait(ctx context.Context, dir string, req ObservationWaitRequest) (ObservationWaitOutput, error) {
	out := ObservationWaitOutput{Protocol: ObservationProtocol, Status: "unavailable", Watch: "inotify"}
	if err := req.validate(); err != nil {
		return out, err
	}
	dir, err := resolveStateDir(dir)
	if err != nil {
		return out, err
	}
	// Notification expiry ends blocking, not authoritative reads. All reads share
	// the bounded completion context, including a hint arriving just before the
	// notification deadline. Reserve up to one second INSIDE that same budget for
	// fresh verification; short leases reserve one quarter of their remaining time.
	deadline := time.Now().Add(time.Duration(req.WaitSeconds) * time.Second)
	if outer, ok := ctx.Deadline(); ok && outer.Before(deadline) {
		deadline = outer
	}
	caller, finish := context.WithDeadline(ctx, deadline)
	defer finish()
	verification := time.Until(deadline) / 4
	if verification > time.Second {
		verification = time.Second
	}
	ctx, cancel := context.WithDeadline(caller, deadline.Add(-verification))
	defer cancel()
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		out.Watch = "degraded"
		return observationWaitExpiry(ctx, caller, dir, req, out)
	}
	defer unix.Close(fd)
	logFD := -1
	if req.Log != nil {
		logFD, err = unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
		if err != nil {
			out.Watch = "degraded"
			return observationWaitExpiry(ctx, caller, dir, req, out)
		}
		defer unix.Close(logFD)
	}
	logWD := -1
	workFD, workWD := -1, -1
	var workTarget, workFilter string
	if req.Work != nil {
		workFD, err = unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
		if err != nil {
			out.Watch = "degraded"
			return observationWaitExpiry(ctx, caller, dir, req, out)
		}
		defer unix.Close(workFD)
	}
	installWork := func() error {
		if req.Work == nil {
			return nil
		}
		next, component, e := observationWatchTarget(workObservationPath(dir, *req.Work))
		if e != nil {
			return e
		}
		if next == workTarget && component == workFilter && workWD >= 0 {
			return nil
		}
		if workWD >= 0 {
			unix.InotifyRmWatch(workFD, uint32(workWD))
			workWD = -1
		}
		w, e := unix.InotifyAddWatch(workFD, next, unix.IN_CREATE|unix.IN_DELETE|unix.IN_MOVED_FROM|unix.IN_MOVED_TO|unix.IN_MODIFY|unix.IN_CLOSE_WRITE|unix.IN_ATTRIB|unix.IN_DELETE_SELF|unix.IN_MOVE_SELF|unix.IN_UNMOUNT)
		if e != nil {
			return e
		}
		workWD, workTarget, workFilter = w, next, component
		return nil
	}
	var logTarget, logFilter string
	installLog := func() error {
		if req.Log == nil {
			return nil
		}
		path, e := observationLogPath(caller, dir, *req.Log)
		if e != nil {
			return e
		}
		if path == "" {
			return nil
		}
		next, component, e := observationWatchTarget(filepath.Dir(path))
		if e != nil {
			return e
		}
		if component == "" {
			component = filepath.Base(path)
		}
		if next == logTarget && component == logFilter && logWD >= 0 {
			return nil
		}
		if logWD >= 0 {
			unix.InotifyRmWatch(logFD, uint32(logWD))
			logWD = -1
		}
		w, e := unix.InotifyAddWatch(logFD, next, unix.IN_CREATE|unix.IN_DELETE|unix.IN_MOVED_FROM|unix.IN_MOVED_TO|unix.IN_MODIFY|unix.IN_CLOSE_WRITE|unix.IN_ATTRIB|unix.IN_DELETE_SELF|unix.IN_MOVE_SELF|unix.IN_UNMOUNT)
		if e != nil {
			return e
		}
		logWD = w
		logTarget = next
		logFilter = component
		return nil
	}
	wd := -1
	var target, filter string
	install := func() error {
		next, component, e := observationWatchTarget(dir)
		if e != nil {
			return e
		}
		if next == target && component == filter && wd >= 0 {
			return nil
		}
		if wd >= 0 {
			unix.InotifyRmWatch(fd, uint32(wd))
			wd = -1
		}
		w, e := unix.InotifyAddWatch(fd, next, unix.IN_CREATE|unix.IN_DELETE|unix.IN_MOVED_FROM|unix.IN_MOVED_TO|unix.IN_MODIFY|unix.IN_CLOSE_WRITE|unix.IN_ATTRIB|unix.IN_DELETE_SELF|unix.IN_MOVE_SELF|unix.IN_UNMOUNT)
		if e != nil {
			return e
		}
		wd = w
		target = next
		filter = component
		return nil
	}
	if err := install(); err != nil {
		out.Watch = "degraded"
		return observationWaitExpiry(ctx, caller, dir, req, out)
	}
	if err := installLog(); err != nil {
		out.Watch = "degraded"
		return observationWaitExpiry(ctx, caller, dir, req, out)
	}
	if err := installWork(); err != nil {
		out.Watch = "degraded"
		return observationWaitExpiry(ctx, caller, dir, req, out)
	}
	check := func(final bool) (bool, error) {
		head, e := observeSnapshot(caller, dir, ObservationRequest{})
		if e != nil {
			return false, e
		}
		out.QueueIdentity, out.Schema, out.Revision, out.ReadAt, out.StoreContext = head.QueueIdentity, head.Schema, head.Revision, head.ReadAt, head.StoreContext
		if head.StoreContext != req.StoreContext {
			out.Status = "reset"
			out.ResetScope = "context"
			return true, nil
		}
		switch head.Status {
		case "ready":
			if head.QueueIdentity != req.QueueIdentity {
				out.Status = "reset"
				return true, nil
			}
			if head.Revision != req.Revision {
				out.Status = "changed"
				out.DatabaseChanged = true
				current, _ := observationDecimal(head.Revision)
				previous, _ := observationDecimal(req.Revision)
				if current < previous {
					out.Status = "reset"
					out.ResetScope = "context"
				}
				return true, nil
			}
			if observationWaitLog(caller, dir, req, &out) {
				return true, nil
			}
			if observationWaitWork(dir, req, &out) {
				return true, nil
			}
			if final {
				out.Status = "unchanged"
				return true, nil
			}
		case "upgrade_required", "reset":
			out.Status = head.Status
			return true, nil
		default:
			// Only genuine absence is eligible for ancestor waiting. Inaccessible,
			// malformed or unsupported existing stores are unavailable, not empty.
			_, e := os.Lstat(filepath.Join(dir, "queue.db"))
			if !errors.Is(e, os.ErrNotExist) || req.QueueIdentity != "" || final {
				out.Status = "unavailable"
				return true, nil
			}
		}
		return false, nil
	}
	if done, e := check(false); done || e != nil {
		if e != nil {
			return out, fmt.Errorf("initial verification: %w", e)
		}
		return out, nil
	}
	buf := make([]byte, 64<<10)
	for {
		if ctx.Err() != nil {
			break
		}
		deadline, _ := ctx.Deadline()
		remaining := time.Until(deadline)
		ms := int(remaining.Milliseconds())
		if ms > 100 {
			ms = 100
		}
		if ms < 1 {
			ms = 1
		}
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if logFD >= 0 {
			poll = append(poll, unix.PollFd{Fd: int32(logFD), Events: unix.POLLIN})
		}
		if workFD >= 0 {
			poll = append(poll, unix.PollFd{Fd: int32(workFD), Events: unix.POLLIN})
		}
		_, e := unix.Poll(poll, ms)
		if e == unix.EINTR {
			continue
		}
		if e != nil {
			out.Watch = "degraded"
			break
		}
		if poll[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			out.Watch = "degraded"
			break
		}
		badPoll := false
		anyInput := false
		for _, p := range poll {
			badPoll = badPoll || p.Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0
			anyInput = anyInput || p.Revents&unix.POLLIN != 0
		}
		if badPoll {
			out.Watch = "degraded"
			break
		}
		if !anyInput {
			continue
		}
		hint, lost := false, false
		// One bounded drain; extra queued hints will be coalesced on the next pass.
		for _, p := range poll {
			if p.Revents&unix.POLLIN == 0 {
				continue
			}
			n, e := unix.Read(int(p.Fd), buf)
			if e == unix.EAGAIN {
				continue
			}
			if e != nil {
				lost = true
				break
			}
			activeWD, activeFilter := wd, filter
			if int(p.Fd) == logFD {
				activeWD, activeFilter = logWD, logFilter
			}
			isWork := int(p.Fd) == workFD
			if isWork {
				activeWD, activeFilter = workWD, workFilter
			}
			for offset := 0; offset+unix.SizeofInotifyEvent <= n; {
				eventWD := int32(binary.NativeEndian.Uint32(buf[offset:]))
				mask := binary.NativeEndian.Uint32(buf[offset+4:])
				length := binary.NativeEndian.Uint32(buf[offset+12:])
				end := offset + unix.SizeofInotifyEvent + int(length)
				if end > n {
					lost = true
					break
				}
				name := strings.TrimRight(string(buf[offset+unix.SizeofInotifyEvent:end]), "\x00")
				if mask&unix.IN_Q_OVERFLOW != 0 {
					lost = true
				}
				if int(eventWD) == activeWD {
					if mask&(unix.IN_IGNORED|unix.IN_UNMOUNT|unix.IN_DELETE_SELF|unix.IN_MOVE_SELF) != 0 {
						lost = true
					}
					if (activeFilter != "" && name == activeFilter) || (activeFilter == "" && (isWork || name == "queue.db" || name == "queue.db-wal")) {
						hint = true
					}
				}
				offset = end
			}
		}
		if lost {
			out.Watch = "degraded"
			// Revalidate before reporting lost watch continuity. Even identical
			// metadata is reset, never evidence that no commit was missed.
			if _, e := check(false); e != nil {
				return out, e
			}
			out.Status = "reset"
			out.ResetScope = "context"
			return out, nil
		}
		if !hint {
			continue
		}
		// Small bounded settling period coalesces writes; it holds no database.
		timer := time.NewTimer(35 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
		if ctx.Err() != nil {
			break
		}
		if err := install(); err != nil {
			out.Watch = "degraded"
			break
		}
		if err := installLog(); err != nil {
			out.Watch = "degraded"
			break
		}
		if err := installWork(); err != nil {
			out.Watch = "degraded"
			break
		}
		if done, e := check(false); done || e != nil {
			if e != nil {
				return out, fmt.Errorf("hint verification: %w", e)
			}
			return out, nil
		}
	}
	if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return out, ctx.Err()
	}
	// The notification lease elapsed; verify using its still-bounded completion budget.
	return observationWaitFinal(caller, dir, req, out)
}

func observationWaitExpiry(ctx, caller context.Context, dir string, req ObservationWaitRequest, out ObservationWaitOutput) (ObservationWaitOutput, error) {
	// Notification support may be absent; still detect an already changed head
	// immediately and do not fetch any projection while idle.
	head, err := observeSnapshot(caller, dir, ObservationRequest{})
	if err != nil {
		return out, err
	}
	out.StoreContext = head.StoreContext
	if head.StoreContext != req.StoreContext {
		out.Status = "reset"
		out.ResetScope = "context"
		return out, nil
	}
	if head.Status == "ready" && (head.QueueIdentity != req.QueueIdentity || head.Revision != req.Revision) {
		out.QueueIdentity, out.Schema, out.Revision, out.ReadAt = head.QueueIdentity, head.Schema, head.Revision, head.ReadAt
		out.Status = "changed"
		out.DatabaseChanged = true
		if head.QueueIdentity != req.QueueIdentity {
			out.Status = "reset"
		}
		return out, nil
	}
	if head.Status == "ready" && observationWaitLog(caller, dir, req, &out) {
		return out, nil
	}
	if head.Status == "ready" && observationWaitWork(dir, req, &out) {
		return out, nil
	}
	if head.Status == "upgrade_required" || head.Status == "reset" {
		out.Status = head.Status
		out.Schema = head.Schema
		return out, nil
	}
	if head.Status == "unavailable" {
		_, statErr := os.Lstat(filepath.Join(dir, "queue.db"))
		if !errors.Is(statErr, os.ErrNotExist) {
			out.Status = "unavailable"
			return out, nil
		}
	}
	<-ctx.Done()
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return out, ctx.Err()
	}
	return observationWaitFinal(caller, dir, req, out)
}

func observationWaitFinal(parent context.Context, dir string, req ObservationWaitRequest, out ObservationWaitOutput) (ObservationWaitOutput, error) {
	// Fresh verification shares the original completion deadline and external
	// cancellation. Do not detach it or accept a previously cached head.
	ctx := parent
	head, err := observeSnapshot(ctx, dir, ObservationRequest{})
	if err != nil {
		return out, fmt.Errorf("expiry verification: %w", err)
	}
	out.QueueIdentity, out.Schema, out.Revision, out.ReadAt = head.QueueIdentity, head.Schema, head.Revision, head.ReadAt
	out.StoreContext = head.StoreContext
	if head.StoreContext != req.StoreContext {
		out.Status = "reset"
		out.ResetScope = "context"
		return out, nil
	}
	out.Status = head.Status
	if head.Status == "ready" {
		switch {
		case head.QueueIdentity != req.QueueIdentity:
			out.Status = "reset"
		case head.Revision != req.Revision:
			out.Status = "changed"
			out.DatabaseChanged = true
		default:
			out.Status = "unchanged"
		}
		current, _ := observationDecimal(head.Revision)
		previous, _ := observationDecimal(req.Revision)
		if current < previous {
			out.Status = "reset"
			out.ResetScope = "context"
		}
		if out.Status == "unchanged" {
			observationWaitLog(ctx, dir, req, &out)
			if out.Status == "unchanged" {
				observationWaitWork(dir, req, &out)
			}
		}
	}
	return out, nil
}

func observationWaitWork(dir string, req ObservationWaitRequest, out *ObservationWaitOutput) bool {
	if req.Work == nil {
		return false
	}
	journal, err := ProjectWorkJournalObservation(dir, req.Work.ProjectID)
	if err != nil {
		out.Status = "unavailable"
		return true
	}
	if journal.Fingerprint != req.Work.Fingerprint {
		out.Status = "changed"
		out.WorkChanged = true
		return true
	}
	return false
}

func observationWaitLog(ctx context.Context, dir string, req ObservationWaitRequest, out *ObservationWaitOutput) bool {
	if req.Log == nil {
		return false
	}
	log := observeLogAt(ctx, dir, *req.Log)
	out.Log = &log
	switch log.Status {
	case "reset":
		out.Status = "reset"
		out.ResetScope = "log"
		out.LogChanged = true
		return true
	case "unavailable":
		out.Status = "unavailable"
		return true
	case "not_found":
		return false
	case "ready":
		offset, _ := observationDecimal(req.Log.Offset)
		size, _ := observationDecimal(log.Cursor.ObservedSize)
		if log.Cursor.FileIdentity != req.Log.FileIdentity || log.Cursor.ObservedSize != req.Log.ObservedSize || log.Cursor.ModifiedNS != req.Log.ModifiedNS || offset < size {
			out.Status = "changed"
			out.LogChanged = true
			return true
		}
	}
	return false
}

// Resolve ONLY retained metadata, never a caller-provided path or a full job.
// The directory watch is installed before the authoritative log metadata check.
func observationLogPath(ctx context.Context, dir string, c ObservationLogCursor) (string, error) {
	s, err := openReadOnlyStoreContext(ctx, dir)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer s.database.Close()
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return "", err
	}
	if version != 6 {
		return "", nil
	}
	row, err := dbquery.New(s.db).RetainedJobLog(ctx, c.JobID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	path := row.Stdout
	if c.Stream == "stderr" {
		path = row.Stderr
	}
	if path == "" {
		return "", nil
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("retained log path must be absolute")
	}
	// If present, enforce the same owned regular-file/final-nonsymlink boundary
	// before watching its parent. Missing files are followed via their ancestor.
	f, err := privateFile(path, os.O_RDONLY)
	if err == nil {
		f.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return path, nil
}
