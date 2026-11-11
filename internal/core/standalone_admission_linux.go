package core

import (
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// Two fixed standalone admission adapters share ONLY their proven private
// journal/framing state machine. The adapter is bound by the concrete Go entry
// point, never selected by imported payloads. No project/lifecycle/tool bus.
type standaloneAdmission uint8

const (
	commandAdmission standaloneAdmission = iota
	agentAdmission
)

type CommandSubmissionStatus struct {
	Scope       CommandSubmissionScope `json:"scope"`
	State       string                 `json:"state"`
	ID          string                 `json:"id,omitempty"`
	Digest      string                 `json:"payload_sha256,omitempty"`
	Observation string                 `json:"observation,omitempty"`
	Receipt     *QueueJobOutput        `json:"receipt,omitempty"`
}

func (k standaloneAdmission) open(dir string, s CommandSubmissionScope) (*composeJournal, error) {
	if !s.valid() || (k != commandAdmission && k != agentAdmission) {
		return nil, errors.New("exact local command scope required")
	}
	name := "herdr-command-submit"
	if k == agentAdmission {
		name = "herdr-agent-submit"
	}
	j, e := openLocalJournal(dir, name)
	if e != nil {
		return nil, e
	}
	r, e := OpenReader(dir)
	if e == nil {
		var actual CommandSubmissionScope
		actual, e = r.CommandSubmissionContext(s.Space)
		if e == nil && actual != s {
			e = errors.New("queue identity changed")
		}
		if e == nil && !commandSubmissionStoreMatches(j, r.store) {
			e = errors.New("queue file identity changed")
		}
		closeErr := r.Close()
		if e == nil {
			e = closeErr
		}
	}
	if e != nil {
		j.close()
		return nil, e
	}
	return j, nil
}

func commandSubmissionStoreMatches(j *composeJournal, s *store) bool {
	dev, ino, e := j.queueIdentity()
	return e == nil && composeStoreMatches(s, composeRecord{Device: dev, Inode: ino})
}

func (k standaloneAdmission) status(j *composeJournal, s CommandSubmissionScope) (CommandSubmissionStatus, composeRecord) {
	out := CommandSubmissionStatus{Scope: s, State: "blocked"}
	var r composeRecord
	b, e := j.read("intent.json", 2*composeLimit)
	if errors.Is(e, syscall.ENOENT) {
		for _, n := range []string{"claim.json", "attempt.json", "completed.json", "cancelled.json"} {
			if _, e := j.read(n, 4096); !errors.Is(e, syscall.ENOENT) {
				return out, r
			}
		}
		out.State = "idle"
		return out, r
	}
	if e != nil || json.Unmarshal(b, &r) != nil || r.Version != 1 || !isValidHexLen(r.ID, 64) || sha256Hex(r.Request) != r.Digest || j.syncFile("intent.json") != nil {
		return out, r
	}
	p, e := k.parse(r.Request)
	dev, ino, identityErr := j.queueIdentity()
	if e != nil || identityErr != nil || dev != r.Device || ino != r.Inode || p.Key != r.Key {
		return out, r
	}
	out.ID, out.Digest = r.ID, r.Digest
	// Another native space can inspect status but never review/claim/admit this
	// request. Return the retained scope explicitly; no silent reassignment.
	if p.Scope != s {
		out.Scope = p.Scope
		out.State = "other_context"
		return out, r
	}
	marker := func(n string) (bool, bool) {
		b, e := j.read(n, 4096)
		if errors.Is(e, syscall.ENOENT) {
			return false, true
		}
		var m composeMarker
		return true, e == nil && json.Unmarshal(b, &m) == nil && m.Version == 1 && m.ID == r.ID && m.Digest == r.Digest && j.syncFile(n) == nil
	}
	claim, cok := marker("claim.json")
	attempt, aok := marker("attempt.json")
	done, dok := marker("completed.json")
	cancel, xok := marker("cancelled.json")
	if !cok || !aok || !dok || !xok || attempt && !claim || done && !attempt || cancel && (attempt || done) {
		return out, r
	}
	out.State = "prepared"
	if claim {
		out.State = "armed"
	}
	if attempt {
		out.State = "attempted"
	}
	if done {
		out.State = "completed"
	}
	if cancel {
		out.State = "cancelled"
	}
	return out, r
}

func (k standaloneAdmission) inspect(dir string, s CommandSubmissionScope) (CommandSubmissionStatus, error) {
	j, e := k.open(dir, s)
	if e != nil {
		return CommandSubmissionStatus{}, e
	}
	defer j.close()
	if e = j.acquire(); e != nil {
		return CommandSubmissionStatus{}, e
	}
	out, _ := k.status(j, s)
	return out, nil
}

func (k standaloneAdmission) prepare(dir string, s CommandSubmissionScope, raw []byte) (CommandSubmissionStatus, error) {
	raw = append([]byte(nil), raw...)
	j, e := k.open(dir, s)
	if e != nil {
		return CommandSubmissionStatus{}, e
	}
	defer j.close()
	if e = j.acquire(); e != nil {
		return CommandSubmissionStatus{}, e
	}
	return k.prepareLocked(j, s, raw)
}
func (k standaloneAdmission) prepareLocked(j *composeJournal, s CommandSubmissionScope, raw []byte) (CommandSubmissionStatus, error) {
	if _, e := k.review(raw); e != nil {
		return CommandSubmissionStatus{}, e
	}
	p, e := k.parse(raw)
	if e != nil || p.Scope != s {
		return CommandSubmissionStatus{}, errors.New("exact command payload scope required")
	}
	status, _ := k.status(j, s)
	if status.State != "idle" {
		return status, errors.New("unresolved command admission blocks preparation")
	}
	dev, ino, e := j.queueIdentity()
	if e != nil {
		return status, e
	}
	id, e := composeRandom()
	if e != nil {
		return status, e
	}
	r := composeRecord{Version: 1, ID: id, Key: p.Key, Device: dev, Inode: ino, Digest: sha256Hex(raw), Request: raw}
	if e = j.create("intent.json", r); e != nil {
		return status, e
	}
	out, _ := k.status(j, s)
	return out, nil
}

func (k standaloneAdmission) mark(dir string, s CommandSubmissionScope, id, action string) (CommandSubmissionStatus, error) {
	j, e := k.open(dir, s)
	if e != nil {
		return CommandSubmissionStatus{}, e
	}
	defer j.close()
	if e = j.acquire(); e != nil {
		return CommandSubmissionStatus{}, e
	}
	out, r := k.status(j, s)
	name := "claim.json"
	allowed := out.State == "prepared"
	if action == "cancel" {
		name = "cancelled.json"
		allowed = allowed || out.State == "armed"
	}
	if !isValidHexLen(id, 64) || out.ID != id || !allowed {
		return out, errors.New("exact unattempted command intent required")
	}
	if e = j.create(name, composeMarker{Version: 1, ID: id, Digest: r.Digest}); e != nil {
		return out, e
	}
	out, _ = k.status(j, s)
	return out, nil
}
func (k standaloneAdmission) observe(j *composeJournal, dir string, s CommandSubmissionScope, r composeRecord, complete bool) (CommandSubmissionStatus, error) {
	out, _ := k.status(j, s)
	reader, e := OpenReader(dir)
	if e != nil {
		out.Observation = "unavailable"
		return out, nil
	}
	defer reader.Close()
	if !commandSubmissionStoreMatches(j, reader.store) {
		out.State = "blocked"
		return out, nil
	}
	p, e := k.parse(r.Request)
	if e != nil {
		return out, e
	}
	receipt, e := k.receipt(reader, p)
	if e != nil {
		out.Observation = "unavailable_or_conflicting"
		out.State = "attempted"
		if errors.Is(e, sql.ErrNoRows) {
			out.Observation = "not_observed"
		}
		return out, nil
	}
	if receipt.QueueIdentity != s.Queue || !commandSubmissionStoreMatches(j, reader.store) {
		out.State = "blocked"
		return out, nil
	}
	if !complete {
		out.Observation = "matched_requires_reconcile"
		return out, nil
	}
	if out.State == "attempted" {
		if e = j.create("completed.json", composeMarker{Version: 1, ID: r.ID, Digest: r.Digest}); e != nil {
			return out, e
		}
	}
	out.State = "completed"
	out.Observation = "matched"
	out.Receipt = &receipt
	return out, nil
}

// One admission call under journal lock, never a dispatcher start or mutation
// retry. Missing/conflicting receipts after an attempt remain uncertain.
func (k standaloneAdmission) attempt(dir string, s CommandSubmissionScope, id string) (CommandSubmissionStatus, error) {
	j, e := k.open(dir, s)
	if e != nil {
		return CommandSubmissionStatus{}, e
	}
	defer j.close()
	if e = j.acquire(); e != nil {
		return CommandSubmissionStatus{}, e
	}
	out, r := k.status(j, s)
	if out.ID != id || out.State != "armed" {
		return out, errors.New("exact armed command required; never retry admission")
	}
	store, e := openExistingWritableStore(dir)
	if e != nil {
		return out, e
	}
	store.existingSchemaOnly = true
	defer store.database.Close()
	if !commandSubmissionStoreMatches(j, store) {
		return out, errors.New("queue identity changed")
	}
	p, e := k.parse(r.Request)
	if e != nil {
		return out, e
	}
	if e = j.create("attempt.json", composeMarker{Version: 1, ID: id, Digest: r.Digest}); e != nil {
		return out, e
	}
	if !commandSubmissionStoreMatches(j, store) {
		return out, errors.New("queue identity changed")
	}
	if k == agentAdmission {
		_, e = (&AgentSubmitter{store: store}).SubmitPreparedAgent(p.Key, []byte(p.Canonical))
	} else {
		_, e = (&CommandSubmitter{store: store}).SubmitPreparedCommand(p.Key, []byte(p.Canonical))
	}
	return k.observe(j, dir, s, r, e == nil)
}

func (k standaloneAdmission) reconcile(dir string, s CommandSubmissionScope, id string) (CommandSubmissionStatus, error) {
	j, e := k.open(dir, s)
	if e != nil {
		return CommandSubmissionStatus{}, e
	}
	defer j.close()
	if e = j.acquire(); e != nil {
		return CommandSubmissionStatus{}, e
	}
	out, r := k.status(j, s)
	if out.ID != id || (out.State != "attempted" && out.State != "completed") {
		return out, errors.New("exact attempted command required")
	}
	return k.observe(j, dir, s, r, true)
}

func (k standaloneAdmission) retire(dir string, s CommandSubmissionScope, id string) (CommandSubmissionStatus, error) {
	j, e := k.open(dir, s)
	if e != nil {
		return CommandSubmissionStatus{}, e
	}
	defer j.close()
	if e = j.acquire(); e != nil {
		return CommandSubmissionStatus{}, e
	}
	out, r := k.status(j, s)
	if out.ID != id || (out.State != "completed" && out.State != "cancelled") {
		return out, errors.New("only completed/cancelled command intake may retire")
	}
	if out.State == "completed" {
		verified, e := k.observe(j, dir, s, r, true)
		if e != nil || verified.Observation != "matched" {
			return out, errors.New("exact command receipt unavailable")
		}
	}
	// acquire rechecks the current directory inode immediately before retirement.
	if e = j.acquire(); e != nil {
		return out, e
	}
	if e = unix.Renameat2(int(j.state.Fd()), j.name, int(j.state.Fd()), j.name+"-retired-"+id, unix.RENAME_NOREPLACE); e != nil {
		return out, e
	}
	if e = j.state.Sync(); e != nil {
		return out, e
	}
	return CommandSubmissionStatus{Scope: s, State: "idle"}, nil
}

func (k standaloneAdmission) rendezvous(dir string, s CommandSubmissionScope, launch string) (ComposeRendezvous, error) {
	if !isValidHexLen(launch, 64) {
		return ComposeRendezvous{}, errors.New("invalid launch")
	}
	j, e := k.open(dir, s)
	if e != nil {
		return ComposeRendezvous{}, e
	}
	defer j.close()
	b, e := j.read("rendezvous.json", 4096)
	var entry ComposeRendezvous
	if e != nil || json.Unmarshal(b, &entry) != nil || entry.Launch != launch || entry.Port < 1 || entry.Port > 65535 || !isValidHexLen(entry.Capability, 64) {
		return entry, errors.New("exact command rendezvous unavailable")
	}
	return entry, nil
}

// Three explicit private routes sharing existing mutual-authentication only.
// Input is ASCII length + UTF-8 bytes; output is uint32 length + private bytes.
func (k standaloneAdmission) exchange(dir string, s CommandSubmissionScope, mode, id, launch string) (CommandSubmissionStatus, error) {
	if !isValidHexLen(launch, 64) || (mode != "draft" && mode != "receive" && mode != "preview" && !(k == agentAdmission && mode == "import")) {
		return CommandSubmissionStatus{}, errors.New("invalid command exchange")
	}
	j, e := k.open(dir, s)
	if e != nil {
		return CommandSubmissionStatus{}, e
	}
	defer j.close()
	if e = j.acquire(); e != nil {
		return CommandSubmissionStatus{}, e
	}
	out, r := k.status(j, s)
	if mode == "preview" {
		if out.ID != id || !commandSubmissionPreviewable(out.State) {
			return out, errors.New("exact retained command unavailable")
		}
	} else if out.State != "idle" {
		return out, errors.New("unresolved command intake")
	}
	e = j.withComposeConnection(launch, func(c net.Conn) error {
		var raw []byte
		if mode == "preview" {
			raw = r.Request
		} else {
			digits := ""
			var b [1]byte
			for {
				if _, e := io.ReadFull(c, b[:]); e != nil {
					return e
				}
				if b[0] == '\n' {
					break
				}
				if b[0] < '0' || b[0] > '9' || len(digits) >= 6 {
					return errors.New("invalid command frame")
				}
				digits += string(b[:])
			}
			n, e := strconv.Atoi(digits)
			if e != nil || n < 1 || n > composeLimit || strconv.Itoa(n) != digits {
				return errors.New("invalid command frame size")
			}
			raw = make([]byte, n)
			if _, e = io.ReadFull(c, raw); e != nil {
				return e
			}
			if mode == "draft" || mode == "import" {
				raw, e = k.draft(mode, s, raw)
				if e != nil {
					return errors.New("command draft invalid; check fields and paths")
				}
			} else {
				out, e = k.prepareLocked(j, s, raw)
				if e != nil {
					return e
				}
				_, e = fmt.Fprintln(c, "PREPARED "+out.ID)
				return e
			}
		}
		projection, e := k.review(raw)
		if e != nil {
			return e
		}
		var h [4]byte
		binary.BigEndian.PutUint32(h[:], uint32(len(projection)))
		if _, e = c.Write(h[:]); e != nil {
			return e
		}
		_, e = c.Write(projection)
		return e
	})
	if e != nil {
		return CommandSubmissionStatus{}, errors.New("private command exchange unavailable")
	}
	if mode == "draft" || mode == "import" {
		out.State = "draft"
	}
	return out, nil
}
func commandSubmissionPreviewable(state string) bool {
	return state == "prepared" || state == "armed" || state == "attempted" || state == "completed" || state == "cancelled"
}

func commandSubmissionReview(raw []byte) ([]byte, error) {
	p, e := parsePreparedCommandSubmission(raw)
	if e != nil {
		return nil, e
	}
	r, e := preparedCommandBytes([]byte(p.Canonical))
	if e != nil {
		return nil, e
	}
	type field struct {
		Label string `json:"label"`
		Value string `json:"value"`
	}
	fields := []field{{"Machine", p.Scope.Machine}, {"Space", p.Scope.Space}, {"Queue identity", p.Scope.Queue}, {"Immutable key", p.Key}, {"Resolved cwd", r.Cwd}, {"Memory bytes (exact int64)", strconv.FormatInt(r.MemoryBytes, 10)}, {"Argv length", strconv.Itoa(len(r.Argv))}}
	for n, arg := range r.Argv {
		fields = append(fields, field{fmt.Sprintf("argv[%d]", n), arg})
	}
	fields = append(fields, field{"Exact v1 canonical request", p.Canonical}, field{"Canonical SHA-256", sha256Hex([]byte(p.Canonical))}, field{"Exact retained envelope", string(raw)}, field{"Envelope SHA-256", sha256Hex(raw)})
	b, e := json.Marshal(struct {
		Raw             string  `json:"raw"`
		Digest          string  `json:"sha256"`
		CanonicalDigest string  `json:"canonical_sha256"`
		Fields          []field `json:"fields"`
	}{string(raw), sha256Hex(raw), sha256Hex([]byte(p.Canonical)), fields})
	if e != nil || len(b) > 8*composeLimit {
		return nil, errors.New("command review exceeds private bound")
	}
	return b, nil
}
