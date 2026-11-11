package core

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const commandDispatcherJournal = "herdr-command-dispatcher"
const commandDispatcherFloor int64 = 6 << 30

// Pane selection is an explicit native observation, never inferred from names or
// a last-used terminal. The trusted native mod validates it again before open.
type CommandDispatcherTarget struct {
	Scope    CommandSubmissionScope `json:"scope"`
	Pane     string                 `json:"pane"`
	Terminal string                 `json:"terminal"`
	Tab      string                 `json:"tab"`
	Folder   string                 `json:"space_folder"`
}

func (t CommandDispatcherTarget) valid() bool {
	return t.Scope.valid() && filepath.IsAbs(t.Folder) && !strings.ContainsAny(t.Folder, "\x00\r\n") && strings.HasPrefix(t.Pane, "p") && isValidHexLen(strings.TrimPrefix(t.Pane, "p"), 32) && strings.HasPrefix(t.Tab, "tb") && isValidHexLen(strings.TrimPrefix(t.Tab, "tb"), 32) && dispatcherTerminal(t.Terminal)
}
func dispatcherTerminal(s string) bool {
	if len(s) < 2 || len(s) > 128 || s[0] != 't' {
		return false
	}
	for _, c := range s[1:] {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

type dispatcherIntent struct {
	Version      int                     `json:"version"`
	ID           string                  `json:"id"`
	Target       CommandDispatcherTarget `json:"target"`
	QueueDir     string                  `json:"queue_dir"`
	Executable   string                  `json:"executable"`
	Device       string                  `json:"executable_device"`
	Inode        string                  `json:"executable_inode"`
	Image        string                  `json:"executable_sha256"`
	Capability   string                  `json:"capability"`
	Slots        int                     `json:"slots"`
	Floor        int64                   `json:"memory_floor_bytes"`
	CommandsOnly bool                    `json:"commands_only"`
}
type CommandDispatcherOwner struct {
	PID      int    `json:"pid"`
	Start    string `json:"start"`
	Boot     string `json:"boot"`
	Terminal string `json:"terminal"`
}
type dispatcherRecord struct {
	Version    int                    `json:"version"`
	ID         string                 `json:"id"`
	Digest     string                 `json:"intent_sha256"`
	Kind       string                 `json:"kind"`
	Owner      CommandDispatcherOwner `json:"owner"`
	Pane       string                 `json:"pane,omitempty"`
	Tab        string                 `json:"tab,omitempty"`
	LockDevice string                 `json:"lock_device,omitempty"`
	LockInode  string                 `json:"lock_inode,omitempty"`
	Outcome    string                 `json:"outcome,omitempty"`
	At         string                 `json:"at"`
	MAC        string                 `json:"mac"`
}

// Safe stored/process observations only. Capabilities and signed private records
// never leave Go. Process exit is NOT a job outcome or release of capacity.
type CommandDispatcherStatus struct {
	Target       CommandDispatcherTarget `json:"target"`
	State        string                  `json:"state"`
	ID           string                  `json:"id,omitempty"`
	Digest       string                  `json:"intent_sha256,omitempty"`
	QueueDir     string                  `json:"queue_dir"`
	Executable   string                  `json:"executable,omitempty"`
	Slots        int                     `json:"slots"`
	Floor        string                  `json:"memory_floor_bytes"`
	CommandsOnly bool                    `json:"commands_only"`
	Owner        *CommandDispatcherOwner `json:"owner,omitempty"`
	HelperPane   string                  `json:"helper_pane,omitempty"`
	HelperTab    string                  `json:"helper_tab,omitempty"`
	Process      string                  `json:"process"`
	Busy         bool                    `json:"busy"`
	CanRetire    bool                    `json:"can_retire"`
	Outcome      string                  `json:"dispatcher_outcome,omitempty"`
}

func dispatcherFileIdentity(f *os.File) (string, string, error) {
	s, e := f.Stat()
	if e != nil {
		return "", "", e
	}
	st, ok := s.Sys().(*syscall.Stat_t)
	if !ok {
		return "", "", errors.New("file identity unavailable")
	}
	return strconv.FormatUint(uint64(st.Dev), 10), strconv.FormatUint(st.Ino, 10), nil
}
func dispatcherImage(path string) (string, string, string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", "", "", e
	}
	defer f.Close()
	dev, ino, e := dispatcherFileIdentity(f)
	if e != nil {
		return "", "", "", e
	}
	h := sha256.New()
	_, e = io.Copy(h, f)
	return dev, ino, hex.EncodeToString(h.Sum(nil)), e
}
func dispatcherRead(dir *os.File, name string, out any) error {
	f, e := composePrivateAt(dir, name, syscall.O_RDONLY)
	if e != nil {
		return e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 16385))
	if e != nil {
		return e
	}
	_, e = decodeStrictJSONObject(b, 16384, out)
	return e
}
func dispatcherLoad(dir *os.File) (dispatcherIntent, string, error) {
	var i dispatcherIntent
	e := dispatcherRead(dir, "intent.json", &i)
	if e != nil {
		return i, "", e
	}
	b, e := json.Marshal(i)
	if e != nil || i.Version != 1 || !isValidHexLen(i.ID, 64) || !isValidHexLen(i.Capability, 64) || !i.Target.valid() || !filepath.IsAbs(i.Executable) || !filepath.IsAbs(i.QueueDir) || i.Slots != 2 || i.Floor != commandDispatcherFloor || !i.CommandsOnly || !isValidHexLen(i.Image, 64) {
		return i, "", errors.New("invalid dispatcher intent")
	}
	return i, sha256Hex(b), nil
}
func dispatcherMarker(dir *os.File, name, id, digest string) (bool, error) {
	var m composeMarker
	e := dispatcherRead(dir, name, &m)
	if errors.Is(e, syscall.ENOENT) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if m.Version != 1 || m.ID != id || m.Digest != digest {
		return false, errors.New("dispatcher marker linkage changed")
	}
	return true, nil
}
func dispatcherSign(i dispatcherIntent, r dispatcherRecord) dispatcherRecord {
	r.MAC = ""
	b, _ := json.Marshal(r)
	key, _ := hex.DecodeString(i.Capability)
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(b)
	r.MAC = hex.EncodeToString(h.Sum(nil))
	return r
}
func dispatcherReceipt(dir *os.File, i dispatcherIntent, digest, kind string) (dispatcherRecord, error) {
	var r dispatcherRecord
	e := dispatcherRead(dir, kind+".json", &r)
	if e != nil {
		return r, e
	}
	expected := dispatcherSign(i, r)
	if r.Version != 1 || r.ID != i.ID || r.Digest != digest || r.Kind != kind || !hmac.Equal([]byte(r.MAC), []byte(expected.MAC)) {
		return r, errors.New("dispatcher receipt linkage changed")
	}
	return r, nil
}
func dispatcherWrite(j *composeJournal, i dispatcherIntent, digest, kind string, owner CommandDispatcherOwner, pane, tab, dev, ino, outcome string) error {
	r := dispatcherRecord{Version: 1, ID: i.ID, Digest: digest, Kind: kind, Owner: owner, Pane: pane, Tab: tab, LockDevice: dev, LockInode: ino, Outcome: outcome, At: now()}
	return j.create(kind+".json", dispatcherSign(i, r))
}
func dispatcherUnlock(j *composeJournal) {
	_ = syscall.Flock(int(j.lock.Fd()), syscall.LOCK_UN)
	_ = syscall.Flock(int(j.queueLock.Fd()), syscall.LOCK_UN)
}
func dispatcherOpen(dir string, t CommandDispatcherTarget) (*composeJournal, error) {
	if !t.valid() || !filepath.IsAbs(dir) {
		return nil, errors.New("exact local model-pane target required")
	}
	r, e := OpenReader(dir)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	s, e := r.CommandSubmissionContext(t.Scope.Space)
	if e != nil || s != t.Scope {
		return nil, errors.New("queue identity changed")
	}
	j, e := openLocalJournal(dir, commandDispatcherJournal)
	if e != nil {
		return nil, e
	}
	if !commandSubmissionStoreMatches(j, r.store) {
		j.close()
		return nil, errors.New("queue file identity changed")
	}
	return j, nil
}
func dispatcherBusy(state *os.File) (bool, error) {
	f, e := composePrivateAt(state, "dispatch.lock", syscall.O_RDONLY)
	if errors.Is(e, syscall.ENOENT) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	defer f.Close()
	e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(e, syscall.EWOULDBLOCK) {
		return true, nil
	}
	if e != nil {
		return false, e
	}
	return false, syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// A pidfd is opened BEFORE checking start/boot/image/argv. No numeric signalling.
func dispatcherProcess(i dispatcherIntent, o CommandDispatcherOwner) (int, bool, error) {
	boot, e := bootID()
	if e != nil {
		return -1, false, e
	}
	if boot != o.Boot {
		return -1, false, nil
	}
	fd, e := unix.PidfdOpen(o.PID, 0)
	if errors.Is(e, syscall.ESRCH) {
		return -1, false, nil
	}
	if e != nil {
		return -1, false, e
	}
	fail := func(e error) (int, bool, error) { unix.Close(fd); return -1, false, e }
	poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, e := unix.Poll(poll, 0)
	if e != nil {
		return fail(e)
	}
	if n != 0 {
		unix.Close(fd)
		return -1, false, nil
	}
	start, e := startTicks(o.PID)
	if e != nil || strconv.FormatUint(start, 10) != o.Start {
		return fail(errors.New("dispatcher process identity changed"))
	}
	f, e := os.Open(fmt.Sprintf("/proc/%d/exe", o.PID))
	if e != nil {
		return fail(e)
	}
	dev, ino, e := dispatcherFileIdentity(f)
	_ = f.Close()
	if e != nil || dev != i.Device || ino != i.Inode {
		return fail(errors.New("dispatcher executable identity changed"))
	}
	argv, e := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", o.PID))
	expected := strings.Join([]string{i.Executable, "dispatcher-helper", i.ID, ""}, "\x00")
	if e != nil || string(argv) != expected {
		return fail(errors.New("dispatcher argv changed"))
	}
	return fd, true, nil
}
func dispatcherOwnedLock(state *os.File, o CommandDispatcherOwner, r dispatcherRecord) error {
	f, e := composePrivateAt(state, "dispatch.lock", syscall.O_RDONLY)
	if e != nil {
		return e
	}
	defer f.Close()
	dev, ino, e := dispatcherFileIdentity(f)
	if e != nil || dev != r.LockDevice || ino != r.LockInode {
		return errors.New("dispatcher lock identity changed")
	}
	var p Process
	e = json.NewDecoder(io.LimitReader(f, 4096)).Decode(&p)
	if e != nil || p.PID != o.PID || strconv.FormatUint(p.Start, 10) != o.Start || p.Boot != o.Boot {
		return errors.New("dispatcher lock owner changed")
	}
	return nil
}
func dispatcherStatus(state, dir *os.File, queueDir string, t CommandDispatcherTarget) (CommandDispatcherStatus, error) {
	out := CommandDispatcherStatus{Target: t, State: "none", QueueDir: queueDir, Slots: 2, Floor: strconv.FormatInt(commandDispatcherFloor, 10), CommandsOnly: true, Process: "not_observed"}
	busy, e := dispatcherBusy(state)
	if e != nil {
		return out, e
	}
	out.Busy = busy
	if dir == nil {
		if busy {
			out.State = "busy_unowned"
		}
		return out, nil
	}
	i, digest, e := dispatcherLoad(dir)
	if errors.Is(e, syscall.ENOENT) {
		for _, name := range []string{"claim.json", "attempt.json", "cancelled.json", "born.json", "bound.json", "ready.json", "exit.json", "drain-prepared.json", "drain-claim.json", "drain-attempt.json"} {
			var marker map[string]any
			if err := dispatcherRead(dir, name, &marker); !errors.Is(err, syscall.ENOENT) {
				return out, errors.New("dispatcher evidence without intent; ownership unknown")
			}
		}
		if busy {
			out.State = "busy_unowned"
		}
		return out, nil
	}
	if e != nil {
		return out, e
	}
	out.ID, out.Digest, out.Executable = i.ID, digest, i.Executable
	out.Target = i.Target
	if i.QueueDir != queueDir || i.Target.Scope.Queue != t.Scope.Queue {
		return out, errors.New("dispatcher queue linkage changed")
	}
	if i.Target != t {
		out.State = "other_context"
		return out, nil
	}
	claim, e := dispatcherMarker(dir, "claim.json", i.ID, digest)
	if e != nil {
		return out, e
	}
	attempt, e := dispatcherMarker(dir, "attempt.json", i.ID, digest)
	if e != nil {
		return out, e
	}
	cancel, e := dispatcherMarker(dir, "cancelled.json", i.ID, digest)
	if e != nil {
		return out, e
	}
	if attempt && !claim || cancel && attempt {
		return out, errors.New("dispatcher marker order changed")
	}
	out.State = "prepared"
	if claim {
		out.State = "armed"
	}
	if cancel {
		out.State = "cancelled"
		out.CanRetire = !busy
		return out, nil
	}
	if !attempt {
		return out, nil
	}
	out.State = "unknown"
	b, e := dispatcherReceipt(dir, i, digest, "born")
	if errors.Is(e, syscall.ENOENT) {
		return out, nil
	}
	if e != nil {
		return out, e
	}
	out.Owner = &b.Owner
	fd, alive, e := dispatcherProcess(i, b.Owner)
	if fd >= 0 {
		unix.Close(fd)
	}
	if e != nil {
		return out, e
	}
	out.Process = "exited"
	if alive {
		out.Process = "alive"
	}
	exit, exitErr := dispatcherReceipt(dir, i, digest, "exit")
	if exitErr == nil {
		if exit.Owner != b.Owner {
			return out, errors.New("exit owner changed")
		}
		out.Outcome = exit.Outcome
		out.State = "exit_recorded"
		if !alive {
			out.State = "stopped"
			out.CanRetire = !busy
		}
		return out, nil
	}
	if !errors.Is(exitErr, syscall.ENOENT) {
		return out, exitErr
	}
	if !alive {
		return out, nil
	}
	out.State = "born"
	bound, e := dispatcherReceipt(dir, i, digest, "bound")
	if errors.Is(e, syscall.ENOENT) {
		return out, nil
	}
	if e != nil || bound.Owner != b.Owner {
		return out, errors.New("dispatcher native binding changed")
	}
	out.HelperPane, out.HelperTab = bound.Pane, bound.Tab
	ready, e := dispatcherReceipt(dir, i, digest, "ready")
	if errors.Is(e, syscall.ENOENT) {
		out.State = "bound"
		return out, nil
	}
	if e != nil || ready.Owner != b.Owner || !busy {
		return out, errors.New("dispatcher readiness/lock unavailable")
	}
	if e = dispatcherOwnedLock(state, b.Owner, ready); e != nil {
		return out, e
	}
	out.State = "ready"
	for _, phase := range []struct{ file, state string }{{"drain-prepared.json", "drain_prepared"}, {"drain-claim.json", "drain_armed"}, {"drain-attempt.json", "draining"}} {
		yes, e := dispatcherMarker(dir, phase.file, i.ID, digest)
		if e != nil {
			return out, e
		}
		if yes {
			out.State = phase.state
		}
	}
	return out, nil
}

// Noninitializing read-only status: no journal/lock file creation, queue writes,
// launch, signals, migration or acknowledgement.
func InspectCommandDispatcher(dir string, t CommandDispatcherTarget) (CommandDispatcherStatus, error) {
	storedOnly := t.Scope.valid() && t.Pane == "" && t.Terminal == "" && t.Tab == "" && t.Folder == ""
	if (!t.valid() && !storedOnly) || !filepath.IsAbs(dir) {
		return CommandDispatcherStatus{}, errors.New("exact local model-pane target required")
	}
	r, e := OpenReader(dir)
	if e != nil {
		return CommandDispatcherStatus{}, e
	}
	defer r.Close()
	s, e := r.CommandSubmissionContext(t.Scope.Space)
	if e != nil || s != t.Scope {
		return CommandDispatcherStatus{}, errors.New("queue identity changed")
	}
	fd, e := syscall.Open(r.store.dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if e != nil {
		return CommandDispatcherStatus{}, e
	}
	state := os.NewFile(uintptr(fd), r.store.dir)
	defer state.Close()
	j, e := composeOpenPrivateDirAt(state, commandDispatcherJournal)
	if errors.Is(e, syscall.ENOENT) {
		return dispatcherStatus(state, nil, r.store.dir, t)
	}
	if e != nil {
		return CommandDispatcherStatus{}, e
	}
	defer j.Close()
	if storedOnly {
		i, _, err := dispatcherLoad(j)
		if err == nil && i.Target.Scope == t.Scope {
			t = i.Target
		} else if err != nil && !errors.Is(err, syscall.ENOENT) {
			return CommandDispatcherStatus{}, err
		}
	}
	return dispatcherStatus(state, j, r.store.dir, t)
}
func PrepareCommandDispatcher(dir string, t CommandDispatcherTarget) (CommandDispatcherStatus, error) {
	j, e := dispatcherOpen(dir, t)
	if e != nil {
		return CommandDispatcherStatus{}, e
	}
	defer j.close()
	if e = j.acquire(); e != nil {
		return CommandDispatcherStatus{}, e
	}
	// openLocalJournal obtained this path from a validated existing Reader.
	// Ancestor aliases must not become a different lexical helper identity.
	actual := j.state.Name()
	out, e := dispatcherStatus(j.state, j.dir, actual, t)
	if e != nil {
		return out, e
	}
	if out.State != "none" || out.Busy {
		return out, errors.New("dispatcher lifetime already retained or singleton busy; no start")
	}
	exe, e := os.Executable()
	if e != nil {
		return out, e
	}
	exe, e = filepath.EvalSymlinks(exe)
	if e != nil {
		return out, e
	}
	dev, ino, image, e := dispatcherImage(exe)
	if e != nil {
		return out, e
	}
	id, e := composeRandom()
	if e != nil {
		return out, e
	}
	cap, e := composeRandom()
	if e != nil {
		return out, e
	}
	i := dispatcherIntent{1, id, t, actual, exe, dev, ino, image, cap, 2, commandDispatcherFloor, true}
	if e = j.create("intent.json", i); e != nil {
		return out, e
	}
	return dispatcherStatus(j.state, j.dir, actual, t)
}

// Fixed lifecycle actions only; no imported payload can choose a helper, policy,
// signal or command. Claims are acknowledged separately from one-shot effects.
func MarkCommandDispatcher(dir string, t CommandDispatcherTarget, id, action string) (CommandDispatcherStatus, error) {
	j, e := dispatcherOpen(dir, t)
	if e != nil {
		return CommandDispatcherStatus{}, e
	}
	defer j.close()
	if e = j.acquire(); e != nil {
		return CommandDispatcherStatus{}, e
	}
	i, digest, e := dispatcherLoad(j.dir)
	if e != nil {
		return CommandDispatcherStatus{}, e
	}
	out, e := dispatcherStatus(j.state, j.dir, i.QueueDir, t)
	if e != nil {
		return out, e
	}
	if i.ID != id || i.Target != t {
		return out, errors.New("exact dispatcher intent/context required")
	}
	marker := func(name string) error { return j.create(name, composeMarker{Version: 1, ID: id, Digest: digest}) }
	switch action {
	case "arm":
		if out.State != "prepared" {
			return out, errors.New("prepared dispatcher required")
		}
		e = marker("claim.json")
	case "attempt":
		if out.State != "armed" || out.Busy {
			return out, errors.New("acknowledged claim and free singleton required; never retry launch")
		}
		e = marker("attempt.json")
	case "cancel":
		if out.State != "prepared" && out.State != "armed" {
			return out, errors.New("attempted launch cannot be cancelled or retried")
		}
		e = marker("cancelled.json")
	case "drain-prepare":
		if out.State != "ready" {
			return out, errors.New("positively owned ready dispatcher required")
		}
		e = marker("drain-prepared.json")
	case "drain-arm":
		if out.State != "drain_prepared" {
			return out, errors.New("separate drain confirmation required")
		}
		e = marker("drain-claim.json")
	case "drain-attempt":
		if out.State != "drain_armed" || out.Owner == nil {
			return out, errors.New("exact acknowledged owned drain required")
		}
		fd, alive, err := dispatcherProcess(i, *out.Owner)
		if err != nil {
			return out, err
		}
		if fd >= 0 {
			defer unix.Close(fd)
		}
		if !alive {
			return out, errors.New("owned process exit; signal refused")
		}
		ready, err := dispatcherReceipt(j.dir, i, digest, "ready")
		if err != nil {
			return out, err
		}
		if err = dispatcherOwnedLock(j.state, *out.Owner, ready); err != nil {
			return out, err
		}
		if e = marker("drain-attempt.json"); e == nil {
			e = unix.PidfdSendSignal(fd, unix.SIGTERM, nil, 0)
		}
	case "retire":
		if !out.CanRetire {
			return out, errors.New("uncertain or live dispatcher cannot be retired")
		}
		if e = unix.Renameat2(int(j.state.Fd()), commandDispatcherJournal, int(j.state.Fd()), commandDispatcherJournal+"-retired-"+id, unix.RENAME_NOREPLACE); e == nil {
			e = j.state.Sync()
		}
		if e != nil {
			return out, e
		}
		out.State = "retired"
		return out, nil
	default:
		return out, errors.New("unsupported concrete dispatcher action")
	}
	if e != nil {
		return out, e
	}
	return dispatcherStatus(j.state, j.dir, i.QueueDir, t)
}

// The trusted native mod calls this once as part of the confirmed launch, only
// after matching direct terminal PID/argv/space/tab/pane to the born receipt.
// Status/reload NEVER performs this admission-enabling mutation.
func BindCommandDispatcher(dir string, t CommandDispatcherTarget, id, terminal, pane, tab, pid, start string) (CommandDispatcherStatus, error) {
	j, e := dispatcherOpen(dir, t)
	if e != nil {
		return CommandDispatcherStatus{}, e
	}
	defer j.close()
	if e = j.acquire(); e != nil {
		return CommandDispatcherStatus{}, e
	}
	i, digest, e := dispatcherLoad(j.dir)
	if e != nil {
		return CommandDispatcherStatus{}, e
	}
	out, e := dispatcherStatus(j.state, j.dir, i.QueueDir, t)
	if e != nil {
		return out, e
	}
	if i.ID != id || i.Target != t || out.State != "born" || out.Owner == nil || out.Owner.Terminal != terminal || strconv.Itoa(out.Owner.PID) != pid || out.Owner.Start != start || tab != t.Tab || pane == t.Pane || !strings.HasPrefix(pane, "p") || !isValidHexLen(strings.TrimPrefix(pane, "p"), 32) {
		return out, errors.New("exact direct native born/pane binding required")
	}
	if e = dispatcherWrite(j, i, digest, "bound", *out.Owner, pane, tab, "", "", ""); e != nil {
		return out, e
	}
	return dispatcherStatus(j.state, j.dir, i.QueueDir, t)
}

// Dedicated visible foreground program, never an exec child or daemon. It does
// not dispatch until the private capability-owned birth AND exact native binding
// are durable. Queue.Dispatch remains the sole scheduler/queue authority.
func RunCommandDispatcher(dir, id string) (resultErr error) {
	if !filepath.IsAbs(dir) {
		return errors.New("explicit existing queue path required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	defer stop()
	reader, err := OpenReader(dir)
	if err != nil {
		return err
	}
	_ = reader.Close()
	j, e := openLocalJournal(dir, commandDispatcherJournal)
	if e != nil {
		return e
	}
	defer j.close()
	if e = j.acquire(); e != nil {
		return e
	}
	i, digest, e := dispatcherLoad(j.dir)
	if e != nil {
		return e
	}
	if i.ID != id {
		return errors.New("dispatcher operation changed")
	}
	claimed, e := dispatcherMarker(j.dir, "claim.json", id, digest)
	if e != nil {
		return e
	}
	attempted, e := dispatcherMarker(j.dir, "attempt.json", id, digest)
	if e != nil || !claimed || !attempted {
		return errors.New("acknowledged one-shot native launch required")
	}
	if !dispatcherTerminal(os.Getenv("HC_TERMINAL_ID")) {
		return errors.New("native terminal identity required")
	}
	cwd, err := os.Getwd()
	if err != nil || cwd != i.Target.Folder {
		return errors.New("native helper folder changed")
	}
	expected := []string{i.Executable, "dispatcher-helper", id}
	if strings.Join(os.Args, "\x00") != strings.Join(expected, "\x00") {
		return errors.New("exact helper argv required")
	}
	dev, ino, image, e := dispatcherImage("/proc/self/exe")
	if e != nil || dev != i.Device || ino != i.Inode || image != i.Image {
		return errors.New("helper executable changed")
	}
	pg, e := syscall.Getpgid(0)
	if e != nil {
		return e
	}
	sid, e := unix.Getsid(0)
	if e != nil {
		return e
	}
	fg, e := unix.IoctlGetInt(0, unix.TIOCGPGRP)
	if e != nil || pg != os.Getpid() || sid != pg || fg != pg {
		return errors.New("direct foreground native terminal required; shell/tab refused")
	}
	s, e := openExistingWritableStore(dir)
	if e != nil {
		return e
	}
	s.existingSchemaOnly = true
	defer s.database.Close()
	queueID, e := notificationQueueIdentity(s)
	if e != nil || queueID != i.Target.Scope.Queue || s.dir != i.QueueDir || !commandSubmissionStoreMatches(j, s) {
		return errors.New("helper queue identity changed")
	}
	start, e := startTicks(os.Getpid())
	if e != nil {
		return e
	}
	boot, e := bootID()
	if e != nil {
		return e
	}
	owner := CommandDispatcherOwner{os.Getpid(), strconv.FormatUint(start, 10), boot, os.Getenv("HC_TERMINAL_ID")}
	if e = dispatcherWrite(j, i, digest, "born", owner, "", "", "", "", ""); e != nil {
		return e
	}
	dispatcherUnlock(j)
	defer func() {
		if e := dispatcherWrite(j, i, digest, "exit", owner, "", "", "", "", func() string {
			if resultErr != nil {
				return "dispatcher_error_not_job_outcome"
			}
			return "dispatcher_returned_not_job_outcome"
		}()); e != nil {
			resultErr = errors.Join(resultErr, e)
		}
	}()
	fmt.Printf("Owned COMMANDS-ONLY dispatcher %s; terminal %s PID %d start %s. Fixed 2 slots / 6 GiB. Closure can force-kill; use explicit Drain. Waiting for exact native binding.\n", id, owner.Terminal, owner.PID, owner.Start)
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		var binding dispatcherRecord
		if e = j.acquire(); e == nil {
			binding, e = dispatcherReceipt(j.dir, i, digest, "bound")
			dispatcherUnlock(j)
		} else {
			// Bounded read-side waiting for the in-flight native binder, not a
			// mutation retry. No dispatch occurs while ownership is unavailable.
			dispatcherUnlock(j)
			e = syscall.ENOENT
		}
		if e == nil {
			if binding.Owner != owner || binding.Tab != i.Target.Tab || binding.Pane == i.Target.Pane {
				return errors.New("native binding owner changed")
			}
			break
		}
		if !errors.Is(e, syscall.ENOENT) {
			return e
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("native binding unavailable; no dispatch, never retry launch")
		case <-tick.C:
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var readyErr error
	q := &Queue{store: s}
	e = q.Dispatch(ctx, 2, commandDispatcherFloor, true, func() {
		if readyErr = j.acquire(); readyErr == nil {
			defer dispatcherUnlock(j)
			identity, err := notificationQueueIdentity(s)
			if err != nil || identity != i.Target.Scope.Queue || !commandSubmissionStoreMatches(j, s) {
				readyErr = errors.New("queue changed before readiness")
				stop()
				return
			}
			var lock *os.File
			lock, readyErr = composePrivateAt(j.state, "dispatch.lock", syscall.O_RDONLY)
			if readyErr == nil {
				var dev, ino string
				dev, ino, readyErr = dispatcherFileIdentity(lock)
				_ = lock.Close()
				if readyErr == nil {
					readyErr = dispatcherWrite(j, i, digest, "ready", owner, "", "", dev, ino, "")
				}
			}
		}
		if readyErr != nil {
			stop()
		} // callback precedes the first core admission
	})
	return errors.Join(e, readyErr)
}
