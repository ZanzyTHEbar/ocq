package core

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/ZanzyTHEbar/faults-go"
	"golang.org/x/sys/unix"
)

const composeLimit = 128 << 10

// ComposeStatus contains no message text. A blocked or dispatch-intent state
// must not be treated as permission to submit another message.
type ComposeStatus struct {
	State       string `json:"state"`
	ID          string `json:"id,omitempty"`
	Observation string `json:"observation,omitempty"`
}

// ComposeIntent contains private user content and must never be logged or
// printed. Request is the exact retained JSON bytes; Key is generated once.
type ComposeIntent struct {
	Status  ComposeStatus
	Key     string
	Request []byte
}

// ComposeRendezvous is a private capability; do not log it or expose it to a
// different mod copy. It is only valid for its exact launch identifier.
type ComposeRendezvous struct {
	Launch     string `json:"launch"`
	Port       int    `json:"port"`
	Capability string `json:"capability"`
}

type composeRecord struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	Key     string `json:"key"`
	Device  string `json:"queue_device"`
	Inode   string `json:"queue_inode"`
	Digest  string `json:"request_sha256"`
	Request []byte `json:"request_base64"`
}

type composeMarker struct {
	Version          int     `json:"version"`
	ID               string  `json:"id"`
	Digest           string  `json:"request_sha256"`
	ExpectedLanePath *string `json:"expected_lane_path,omitempty"`
	FsforkPath       *string `json:"fsfork_path,omitempty"`
}

type composeCompletion struct {
	Version  int    `json:"version"`
	ID       string `json:"id"`
	Digest   string `json:"request_sha256"`
	Message  string `json:"message_id"`
	Sequence int64  `json:"sequence"`
}

type composeJournal struct {
	state     *os.File
	dir       *os.File
	lock      *os.File
	queueLock *os.File
	name      string
}

func composeRandom() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func composePrivateAt(dir *os.File, name string, flags int) (*os.File, error) {
	fd, err := syscall.Openat(int(dir.Fd()), name, flags|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	info, err := f.Stat()
	if err == nil {
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != uint32(os.Getuid()) || st.Nlink != 1 || !info.Mode().IsRegular() || info.Mode() != 0600 {
			err = errors.New("compose file must be owned, unlinked elsewhere, regular and mode 0600")
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func composePrivateDirAt(dir *os.File, name string) (*os.File, error) {
	if err := syscall.Mkdirat(int(dir.Fd()), name, 0700); err != nil && !errors.Is(err, syscall.EEXIST) {
		return nil, err
	}
	if err := dir.Sync(); err != nil {
		return nil, err
	}
	return composeOpenPrivateDirAt(dir, name)
}

// Read-only journal inspection must not create a directory or synchronize it.
func composeOpenPrivateDirAt(dir *os.File, name string) (*os.File, error) {
	fd, err := syscall.Openat(int(dir.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	info, err := f.Stat()
	if err == nil {
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != uint32(os.Getuid()) || info.Mode() != os.ModeDir|0700 {
			err = errors.New("compose directory must be owned, nonsymlink and mode 0700")
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func openComposeJournal(stateDir string) (_ *composeJournal, resultErr error) {
	return openLocalJournal(stateDir, "herdr-compose")
}

// Each named journal has a stable queue-wide lock across retirement and reopen.
func openLocalJournal(stateDir, name string) (_ *composeJournal, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeStorageFailed, resultErr) }()
	reader, err := OpenReader(stateDir)
	if err != nil {
		return nil, err
	}
	dir := reader.store.dir
	if err := reader.Close(); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	state := os.NewFile(uintptr(fd), dir)
	info, err := state.Stat()
	if err == nil {
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != uint32(os.Getuid()) || info.Mode() != os.ModeDir|0700 {
			err = errors.New("queue directory must be owned, nonsymlink and mode 0700")
		}
	}
	if err != nil {
		state.Close()
		return nil, err
	}
	queueLock, err := composePrivateAt(state, name+".lock", syscall.O_CREAT|syscall.O_RDWR)
	if err != nil {
		state.Close()
		return nil, err
	}
	if err := state.Sync(); err != nil {
		queueLock.Close()
		state.Close()
		return nil, err
	}
	journal, err := composePrivateDirAt(state, name)
	if err != nil {
		queueLock.Close()
		state.Close()
		return nil, err
	}
	lock, err := composePrivateAt(journal, "lock", syscall.O_CREAT|syscall.O_RDWR)
	if err != nil {
		journal.Close()
		queueLock.Close()
		state.Close()
		return nil, err
	}
	if err := journal.Sync(); err != nil {
		lock.Close()
		journal.Close()
		queueLock.Close()
		state.Close()
		return nil, err
	}
	return &composeJournal{state: state, dir: journal, lock: lock, queueLock: queueLock, name: name}, nil
}

func (j *composeJournal) close() {
	j.lock.Close()
	j.dir.Close()
	j.queueLock.Close()
	j.state.Close()
}

func (j *composeJournal) acquire() error {
	if err := syscall.Flock(int(j.queueLock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return faults.Wrap(faults.CodeResourceLimit, "another compose operation is active", err)
	}
	if err := syscall.Flock(int(j.lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return faults.Wrap(faults.CodeResourceLimit, "another compose operation is active", err)
	}
	var current unix.Stat_t
	info, err := j.dir.Stat()
	if err != nil {
		return err
	}
	opened, ok := info.Sys().(*syscall.Stat_t)
	if !ok || unix.Fstatat(int(j.state.Fd()), j.name, &current, unix.AT_SYMLINK_NOFOLLOW) != nil ||
		uint64(current.Dev) != uint64(opened.Dev) || current.Ino != opened.Ino || current.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return faults.New(faults.CodeStorageFailed, "compose journal identity changed")
	}
	return nil
}

func (j *composeJournal) queueIdentity() (string, string, error) {
	f, err := composePrivateAt(j.state, "queue.db", syscall.O_RDONLY)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", "", err
	}
	st := info.Sys().(*syscall.Stat_t)
	return strconv.FormatUint(uint64(st.Dev), 10), strconv.FormatUint(st.Ino, 10), nil
}

func (j *composeJournal) read(name string, limit int64) ([]byte, error) {
	f, err := composePrivateAt(j.dir, name, syscall.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(b)) > limit {
		return nil, errors.New("compose record exceeds size limit")
	}
	return b, err
}

func (j *composeJournal) create(name string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := composePrivateAt(j.dir, name, syscall.O_CREAT|syscall.O_EXCL|syscall.O_WRONLY)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(b)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return j.dir.Sync()
}

func (j *composeJournal) syncFile(name string) error {
	f, err := composePrivateAt(j.dir, name, syscall.O_RDWR)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return err
	}
	return j.dir.Sync()
}

func (j *composeJournal) status() (ComposeStatus, error) {
	b, err := j.read("intent.json", 2*composeLimit)
	if errors.Is(err, syscall.ENOENT) {
		// An orphan marker is never an idle or safe-to-retry state.
		for _, name := range []string{"dispatch.json", "attempt.json", "completed.json", "cancelled.json"} {
			if _, markerErr := j.read(name, 4096); !errors.Is(markerErr, syscall.ENOENT) {
				return ComposeStatus{State: "blocked"}, nil
			}
		}
		return ComposeStatus{State: "idle"}, nil
	}
	if err != nil {
		return ComposeStatus{State: "blocked"}, nil
	}
	var r composeRecord
	if json.Unmarshal(b, &r) != nil || r.Version != 1 || !isValidHexLen(r.ID, 64) || !isValidHexLen(r.Key, 64) || !isValidHexLen(r.Digest, 64) || len(r.Request) == 0 || len(r.Request) > composeLimit || sha256Hex(r.Request) != r.Digest || !validComposeRequest(r.Request) {
		return ComposeStatus{State: "blocked"}, nil
	}
	// A prior creator may have failed or died before synchronization. Never
	// expose this record as prepared until its bytes and directory entry sync.
	if j.syncFile("intent.json") != nil {
		return ComposeStatus{State: "blocked", ID: r.ID}, nil
	}
	dev, ino, err := j.queueIdentity()
	if err != nil || r.Device != dev || r.Inode != ino {
		return ComposeStatus{State: "blocked", ID: r.ID}, nil
	}
	b, err = j.read("dispatch.json", 4096)
	dispatch := !errors.Is(err, syscall.ENOENT)
	var marker composeMarker
	if dispatch && (err != nil || json.Unmarshal(b, &marker) != nil || (marker.Version != 1 && marker.Version != 2 && marker.Version != 3) || marker.ID != r.ID || marker.Digest != r.Digest || (marker.Version >= 2 && (marker.ExpectedLanePath == nil || marker.FsforkPath == nil))) {
		return ComposeStatus{State: "blocked", ID: r.ID}, nil
	}
	if dispatch && j.syncFile("dispatch.json") != nil {
		return ComposeStatus{State: "blocked", ID: r.ID}, nil
	}
	attemptBytes, attemptErr := j.read("attempt.json", 4096)
	attempted := !errors.Is(attemptErr, syscall.ENOENT)
	var attempt composeMarker
	if attempted && (attemptErr != nil || json.Unmarshal(attemptBytes, &attempt) != nil || attempt.Version != 1 || attempt.ID != r.ID || attempt.Digest != r.Digest || !dispatch || marker.Version != 3) {
		return ComposeStatus{State: "blocked", ID: r.ID}, nil
	}
	if attempted && j.syncFile("attempt.json") != nil {
		return ComposeStatus{State: "blocked", ID: r.ID}, nil
	}
	cancelBytes, cancelErr := j.read("cancelled.json", 4096)
	cancelled := !errors.Is(cancelErr, syscall.ENOENT)
	var cancel composeMarker
	if cancelled && (cancelErr != nil || json.Unmarshal(cancelBytes, &cancel) != nil || cancel.Version != 1 || cancel.ID != r.ID || cancel.Digest != r.Digest || attempted || (dispatch && marker.Version != 3)) {
		return ComposeStatus{State: "blocked", ID: r.ID}, nil
	}
	if cancelled && j.syncFile("cancelled.json") != nil {
		return ComposeStatus{State: "blocked", ID: r.ID}, nil
	}
	completeBytes, completeErr := j.read("completed.json", 4096)
	completed := !errors.Is(completeErr, syscall.ENOENT)
	var complete composeCompletion
	if completed && (completeErr != nil || json.Unmarshal(completeBytes, &complete) != nil || complete.Version != 1 || complete.ID != r.ID || complete.Digest != r.Digest || !isValidInboxMessageID(complete.Message) || complete.Sequence <= 0 || !dispatch || cancelled || (marker.Version == 3 && !attempted)) {
		return ComposeStatus{State: "blocked", ID: r.ID}, nil
	}
	if completed && j.syncFile("completed.json") != nil {
		return ComposeStatus{State: "blocked", ID: r.ID}, nil
	}
	if completed {
		return ComposeStatus{State: "completed", ID: r.ID}, nil
	}
	if cancelled {
		if dispatch {
			return ComposeStatus{State: "cancelled", ID: r.ID, Observation: "revoked_before_attempt"}, nil
		}
		return ComposeStatus{State: "cancelled", ID: r.ID}, nil
	}
	if dispatch {
		if marker.Version == 3 && !attempted {
			return ComposeStatus{State: "armed", ID: r.ID}, nil
		}
		return ComposeStatus{State: "dispatch_intent", ID: r.ID}, nil
	}
	return ComposeStatus{State: "prepared", ID: r.ID}, nil
}

// LocalComposeStatus reports a single queue-wide intent without consuming it.
func LocalComposeStatus(stateDir string) (ComposeStatus, error) {
	j, err := openComposeJournal(stateDir)
	if err != nil {
		return ComposeStatus{}, err
	}
	defer j.close()
	if err := j.acquire(); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return ComposeStatus{State: "busy"}, nil
		}
		return ComposeStatus{}, err
	}
	return j.status()
}

// ReadLocalComposeIntent reads an exact intent for a future in-process
// reconciler. It neither dispatches nor exposes content through the bridge CLI.
func ReadLocalComposeIntent(stateDir, id string) (ComposeIntent, error) {
	if !isValidHexLen(id, 64) {
		return ComposeIntent{}, faults.New(faults.CodeValidationFailed, "invalid compose intent identifier")
	}
	j, err := openComposeJournal(stateDir)
	if err != nil {
		return ComposeIntent{}, err
	}
	defer j.close()
	if err := j.acquire(); err != nil {
		return ComposeIntent{}, err
	}
	status, err := j.status()
	if err != nil || status.ID != id || (status.State != "prepared" && status.State != "armed" && status.State != "dispatch_intent" && status.State != "completed" && status.State != "cancelled") {
		return ComposeIntent{}, faults.New(faults.CodeNotFound, "exact valid compose intent not found")
	}
	b, err := j.read("intent.json", 2*composeLimit)
	if err != nil {
		return ComposeIntent{}, classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	var r composeRecord
	if json.Unmarshal(b, &r) != nil || r.ID != id || sha256Hex(r.Request) != r.Digest {
		return ComposeIntent{}, faults.New(faults.CodeInvariantFailed, "compose intent changed")
	}
	return ComposeIntent{Status: status, Key: r.Key, Request: r.Request}, nil
}

// LocalComposeRendezvous reads the capability only for an exact launch ID.
// It does not initiate work. A stale rendezvous is not proof of a live peer.
func LocalComposeRendezvous(stateDir, launch string) (ComposeRendezvous, error) {
	var out ComposeRendezvous
	if !isValidHexLen(launch, 64) {
		return out, faults.New(faults.CodeValidationFailed, "invalid compose launch identifier")
	}
	j, err := openComposeJournal(stateDir)
	if err != nil {
		return out, err
	}
	defer j.close()
	b, err := j.read("rendezvous.json", 4096)
	if errors.Is(err, syscall.ENOENT) {
		return out, faults.Wrap(faults.CodeNotFound, "exact compose rendezvous not found", err)
	}
	if err != nil {
		return out, classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	if json.Unmarshal(b, &out) != nil || out.Launch != launch || out.Port < 1 || out.Port > 65535 || !isValidHexLen(out.Capability, 64) {
		return ComposeRendezvous{}, faults.New(faults.CodeNotFound, "exact compose rendezvous not found")
	}
	return out, nil
}

// MarkLocalComposeDispatchIntent durably marks an exact prepared intent before
// a future explicit send. It never sends mail and cannot clear uncertainty.
func MarkLocalComposeDispatchIntent(stateDir, id string) (ComposeStatus, error) {
	if !isValidHexLen(id, 64) {
		return ComposeStatus{}, faults.New(faults.CodeValidationFailed, "invalid compose intent identifier")
	}
	j, err := openComposeJournal(stateDir)
	if err != nil {
		return ComposeStatus{}, err
	}
	defer j.close()
	if err := j.acquire(); err != nil {
		return ComposeStatus{}, err
	}
	status, err := j.status()
	if err != nil || status.State != "prepared" || status.ID != id {
		return ComposeStatus{}, faults.New(faults.CodeValidationFailed, "exact prepared compose intent unavailable; do not retry")
	}
	b, err := j.read("intent.json", 2*composeLimit)
	if err != nil {
		return ComposeStatus{}, classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	var r composeRecord
	if json.Unmarshal(b, &r) != nil || r.ID != id {
		return ComposeStatus{}, faults.New(faults.CodeInvariantFailed, "compose intent changed")
	}
	if err := j.create("dispatch.json", composeMarker{Version: 1, ID: id, Digest: r.Digest}); err != nil {
		return ComposeStatus{}, classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	return ComposeStatus{State: "dispatch_intent", ID: id}, nil
}

func composeProof(capability, nonce, role string) string {
	key, _ := hex.DecodeString(capability)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("ocq-compose-v1:" + role + ":" + nonce))
	return hex.EncodeToString(mac.Sum(nil))
}

func composeLine(r *bufio.Reader, limit int) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil || len(line) > limit || !utf8.ValidString(line) {
		return "", errors.New("invalid or incomplete compose frame")
	}
	return strings.TrimSuffix(line, "\n"), nil
}

// Read authentication frames without buffering subsequent payload bytes.
func composeAuthLine(r io.Reader) (string, error) {
	var frame [65]byte
	if _, err := io.ReadFull(r, frame[:]); err != nil || frame[64] != '\n' {
		return "", errors.New("invalid compose authentication")
	}
	return string(frame[:64]), nil
}

// withComposeConnection publishes one private rendezvous while its caller holds
// the journal lock. It authenticates both peers before invoking exchange.
func (j *composeJournal) withComposeConnection(launch string, exchange func(net.Conn) error) error {
	if err := syscall.Unlinkat(int(j.dir.Fd()), "rendezvous.json"); err != nil && !errors.Is(err, syscall.ENOENT) {
		return classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return classifyDiagnostic(faults.CodeRuntimeFailed, err)
	}
	defer listener.Close()
	deadline := time.Now().Add(20 * time.Second)
	if err := listener.SetDeadline(deadline); err != nil {
		return err
	}
	capability, err := composeRandom()
	if err != nil {
		return err
	}
	entry := ComposeRendezvous{Launch: launch, Port: listener.Addr().(*net.TCPAddr).Port, Capability: capability}
	if err := j.create("rendezvous.json", entry); err != nil {
		return classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	defer func() { _ = syscall.Unlinkat(int(j.dir.Fd()), "rendezvous.json"); _ = j.dir.Sync() }()
	conn, err := listener.AcceptTCP()
	if err != nil {
		return faults.New(faults.CodeRuntimeFailed, "compose connection timed out or failed")
	}
	defer conn.Close()
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	nonce, err := composeAuthLine(conn)
	if err != nil || !isValidHexLen(nonce, 64) {
		return faults.New(faults.CodeValidationFailed, "invalid compose authentication")
	}
	if _, err := fmt.Fprintln(conn, composeProof(capability, nonce, "server")); err != nil {
		return faults.New(faults.CodeRuntimeFailed, "compose authentication failed")
	}
	proof, err := composeAuthLine(conn)
	want := composeProof(capability, nonce, "client")
	if err != nil || subtle.ConstantTimeCompare([]byte(proof), []byte(want)) != 1 {
		return faults.New(faults.CodeValidationFailed, "invalid compose authentication")
	}
	if _, err := fmt.Fprintln(conn, "READY"); err != nil {
		return faults.New(faults.CodeRuntimeFailed, "compose connection failed")
	}
	return exchange(conn)
}

// The payload uses MailRequest's exported field names, but excludes Key: the
// receiver generates it. Reject aliases and duplicate fields so a later decoder
// cannot interpret the same retained bytes as a different request.
func validComposeRequest(payload []byte) bool {
	if !utf8.Valid(payload) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(payload))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return false
	}
	allowed := map[string]bool{"ProjectID": true, "From": true, "To": true, "LaneID": true, "JobID": true, "Kind": true, "Subject": true, "Body": true, "Manifest": true, "ReplyTo": true, "FsforkPath": true}
	seen := make(map[string]bool)
	for d.More() {
		tok, err := d.Token()
		name, ok := tok.(string)
		if err != nil || !ok || !allowed[name] || seen[name] {
			return false
		}
		seen[name] = true
		var raw json.RawMessage
		if d.Decode(&raw) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return false
		}
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return false
		}
	}
	if tok, err = d.Token(); err != nil || tok != json.Delim('}') {
		return false
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return false
	}
	var request MailRequest
	if json.Unmarshal(payload, &request) != nil || !isValidProjectID(request.ProjectID) || !isValidLaneID(request.LaneID) || !isValidJobID(request.JobID) || request.Subject == "" || len(request.Subject) > 512 || len(request.Body) > 16<<10 || strings.ContainsRune(request.Subject, 0) || strings.ContainsRune(request.Body, 0) {
		return false
	}
	if request.Kind == "lane.blocked" {
		if (!strings.HasPrefix(request.From, "lane:") && !strings.HasPrefix(request.From, "job:")) || request.To != "integrator:"+request.ProjectID {
			return false
		}
	} else if request.Kind == "lane.assigned" {
		if !strings.HasPrefix(request.From, "integrator:") || !strings.HasPrefix(request.To, "lane:") {
			return false
		}
	} else {
		return false
	}
	return validateInboxSender(request.From, request.LaneID, request.JobID) == nil && validateInboxRecipient(request.To, request.ProjectID) == nil && (request.Manifest == "" || isValidHexLen(request.Manifest, 64)) && (request.ReplyTo == "" || isValidInboxMessageID(request.ReplyTo))
}

// ReceiveLocalComposeIntent runs one bounded loopback receiver. Authentication
// precedes payload input; this function persists an intent but never sends mail.
func ReceiveLocalComposeIntent(stateDir, launch string) (ComposeStatus, error) {
	if !isValidHexLen(launch, 64) {
		return ComposeStatus{}, faults.New(faults.CodeValidationFailed, "invalid compose launch identifier")
	}
	j, err := openComposeJournal(stateDir)
	if err != nil {
		return ComposeStatus{}, err
	}
	defer j.close()
	if err := j.acquire(); err != nil {
		return ComposeStatus{}, err
	}
	status, err := j.status()
	if err != nil || status.State != "idle" {
		return ComposeStatus{}, faults.New(faults.CodeValidationFailed, "unresolved compose intent blocks new preparation")
	}
	err = j.withComposeConnection(launch, func(conn net.Conn) error {
		r := bufio.NewReader(io.LimitReader(conn, composeLimit+1))
		payload, err := composeLine(r, composeLimit+1)
		if err != nil || len(payload) == 0 || !validComposeRequest([]byte(payload)) {
			return faults.New(faults.CodeValidationFailed, "invalid compose request")
		}
		dev, ino, err := j.queueIdentity()
		if err != nil {
			return classifyDiagnostic(faults.CodeStorageFailed, err)
		}
		id, err := composeRandom()
		if err != nil {
			return err
		}
		key, err := composeRandom()
		if err != nil {
			return err
		}
		record := composeRecord{Version: 1, ID: id, Key: key, Device: dev, Inode: ino, Digest: sha256Hex([]byte(payload)), Request: []byte(payload)}
		if err := j.create("intent.json", record); err != nil {
			return classifyDiagnostic(faults.CodeStorageFailed, err)
		}
		// Failure to deliver this acknowledgement does not remove the intent.
		_, _ = fmt.Fprintln(conn, "PREPARED "+id)
		status = ComposeStatus{State: "prepared", ID: id}
		return nil
	})
	if err != nil {
		return ComposeStatus{}, err
	}
	return status, nil
}

// PreviewLocalComposeIntent exposes exact prepared or armed request bytes only
// to an authenticated loopback peer. It holds the journal lock and never dispatches.
func PreviewLocalComposeIntent(stateDir, id, launch string) (ComposeStatus, error) {
	if !isValidHexLen(id, 64) || !isValidHexLen(launch, 64) {
		return ComposeStatus{}, faults.New(faults.CodeValidationFailed, "invalid compose preview identifier")
	}
	j, err := openComposeJournal(stateDir)
	if err != nil {
		return ComposeStatus{}, err
	}
	defer j.close()
	if err := j.acquire(); err != nil {
		return ComposeStatus{}, err
	}
	status, err := j.status()
	if err != nil || (status.State != "prepared" && status.State != "armed") || status.ID != id {
		return ComposeStatus{}, faults.New(faults.CodeValidationFailed, "exact unattempted compose intent unavailable")
	}
	err = j.withComposeConnection(launch, func(conn net.Conn) error {
		status, err := j.status()
		if err != nil || (status.State != "prepared" && status.State != "armed") || status.ID != id {
			return faults.New(faults.CodeValidationFailed, "exact unattempted compose intent unavailable")
		}
		record, err := j.record(id)
		if err != nil {
			return err
		}
		if !j.identityMatches(record) {
			return faults.New(faults.CodeValidationFailed, "compose queue identity changed")
		}
		if _, err := conn.Write(append(record.Request, '\n')); err != nil {
			return faults.New(faults.CodeRuntimeFailed, "compose preview connection failed")
		}
		return nil
	})
	if err != nil {
		return ComposeStatus{}, err
	}
	return status, nil
}
