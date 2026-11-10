package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"syscall"

	"github.com/ZanzyTHEbar/faults-go"
	"golang.org/x/sys/unix"
)

// ProjectIntakeStatus never contains request bytes or runtime credentials.
type ProjectIntakeStatus struct {
	State       string `json:"state"`
	ID          string `json:"id,omitempty"`
	Observation string `json:"observation,omitempty"`
}

// ProjectIntakeIntent contains private, exact request bytes. Do not log it.
type ProjectIntakeIntent struct {
	Status  ProjectIntakeStatus
	Kind    string
	Key     string
	Request []byte
}

type intakeRecord struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Key     string `json:"key"`
	Device  string `json:"queue_device"`
	Inode   string `json:"queue_inode"`
	Digest  string `json:"request_sha256"`
	Request []byte `json:"request_base64"`
}

func intakeKey(kind string, raw []byte) (string, error) {
	switch kind {
	case "registration":
		req, _, _, err := parseRegistrationPayload(raw)
		return req.Key, err
	case "submission":
		parsed, err := parseSubmissionEnvelope(raw)
		return parsed.Envelope.Key, err
	default:
		return "", faults.New(faults.CodeValidationFailed, "invalid project intake kind")
	}
}

func intakeStatus(j *composeJournal) (ProjectIntakeStatus, intakeRecord) {
	var r intakeRecord
	b, err := j.read("intent.json", 2*composeLimit)
	if errors.Is(err, syscall.ENOENT) {
		for _, name := range []string{"claim.json", "attempt.json", "cancelled.json", "completed.json"} {
			if _, err := j.read(name, 4096); !errors.Is(err, syscall.ENOENT) {
				return ProjectIntakeStatus{State: "blocked"}, r
			}
		}
		return ProjectIntakeStatus{State: "idle"}, r
	}
	if err != nil || json.Unmarshal(b, &r) != nil || r.Version != 1 || !isValidHexLen(r.ID, 64) || !isValidHexLen(r.Digest, 64) || len(r.Request) == 0 || len(r.Request) > composeLimit || sha256Hex(r.Request) != r.Digest {
		return ProjectIntakeStatus{State: "blocked"}, intakeRecord{}
	}
	key, err := intakeKey(r.Kind, r.Request)
	dev, ino, identityErr := j.queueIdentity()
	if err != nil || key != r.Key || identityErr != nil || dev != r.Device || ino != r.Inode || j.syncFile("intent.json") != nil {
		return ProjectIntakeStatus{State: "blocked", ID: r.ID}, r
	}
	marker := func(name string) (bool, bool) {
		b, err := j.read(name, 4096)
		if errors.Is(err, syscall.ENOENT) {
			return false, true
		}
		var m composeMarker
		if err != nil || json.Unmarshal(b, &m) != nil || m.Version != 1 || m.ID != r.ID || m.Digest != r.Digest || j.syncFile(name) != nil {
			return true, false
		}
		return true, true
	}
	claim, claimOK := marker("claim.json")
	attempt, attemptOK := marker("attempt.json")
	cancel, cancelOK := marker("cancelled.json")
	complete, completeOK := marker("completed.json")
	if !claimOK || !attemptOK || !cancelOK || !completeOK || (attempt && !claim) || (cancel && (attempt || complete)) || (complete && !attempt) {
		return ProjectIntakeStatus{State: "blocked", ID: r.ID}, r
	}
	state := "prepared"
	switch {
	case complete:
		state = "completed"
	case cancel:
		state = "cancelled"
	case attempt:
		state = "attempted"
	case claim:
		state = "armed"
	}
	return ProjectIntakeStatus{State: state, ID: r.ID}, r
}

func openIntakeJournal(stateDir string) (*composeJournal, error) {
	return openLocalJournal(stateDir, "herdr-project-intake")
}

// LocalProjectIntakeStatus discovers an existing intent across process restarts.
func LocalProjectIntakeStatus(stateDir string) (ProjectIntakeStatus, error) {
	j, err := openIntakeJournal(stateDir)
	if err != nil {
		return ProjectIntakeStatus{}, err
	}
	defer j.close()
	if err := j.acquire(); err != nil {
		return ProjectIntakeStatus{}, err
	}
	status, _ := intakeStatus(j)
	return status, nil
}

// PrepareLocalProjectIntake retains the caller's exact bytes and key; it does
// not admit work. Only one unresolved intent is allowed per queue.
func PrepareLocalProjectIntake(stateDir, kind string, raw []byte) (ProjectIntakeStatus, error) {
	raw = append([]byte(nil), raw...)
	key, err := intakeKey(kind, raw)
	if err != nil {
		return ProjectIntakeStatus{}, faults.Wrap(faults.CodeValidationFailed, "invalid project intake request", err)
	}
	j, err := openIntakeJournal(stateDir)
	if err != nil {
		return ProjectIntakeStatus{}, err
	}
	defer j.close()
	if err := j.acquire(); err != nil {
		return ProjectIntakeStatus{}, err
	}
	return prepareIntakeLocked(j, kind, key, raw)
}

func prepareIntakeLocked(j *composeJournal, kind, key string, raw []byte) (ProjectIntakeStatus, error) {
	status, _ := intakeStatus(j)
	if status.State != "idle" {
		return ProjectIntakeStatus{}, faults.New(faults.CodeValidationFailed, "unresolved project intake blocks preparation")
	}
	dev, ino, err := j.queueIdentity()
	if err != nil {
		return ProjectIntakeStatus{}, err
	}
	id, err := composeRandom()
	if err != nil {
		return ProjectIntakeStatus{}, err
	}
	r := intakeRecord{Version: 1, ID: id, Kind: kind, Key: key, Device: dev, Inode: ino, Digest: sha256Hex(raw), Request: raw}
	if err := j.create("intent.json", r); err != nil {
		return ProjectIntakeStatus{}, classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	return ProjectIntakeStatus{State: "prepared", ID: id}, nil
}

// ReadLocalProjectIntake returns private retained bytes for an exact preview.
func ReadLocalProjectIntake(stateDir, id string) (ProjectIntakeIntent, error) {
	j, err := openIntakeJournal(stateDir)
	if err != nil {
		return ProjectIntakeIntent{}, err
	}
	defer j.close()
	if err := j.acquire(); err != nil {
		return ProjectIntakeIntent{}, err
	}
	status, r := intakeStatus(j)
	if status.ID != id || status.State == "blocked" || status.State == "idle" {
		return ProjectIntakeIntent{}, faults.New(faults.CodeNotFound, "exact project intake intent unavailable")
	}
	return ProjectIntakeIntent{Status: status, Kind: r.Kind, Key: r.Key, Request: r.Request}, nil
}

// ArmLocalProjectIntake records explicit caller confirmation. A caller must
// await its successful response before launching a dispatch process.
func ArmLocalProjectIntake(stateDir, id string) (ProjectIntakeStatus, error) {
	return markIntake(stateDir, id, "prepared", "claim.json", "armed")
}

func markIntake(stateDir, id, expected, name, next string) (ProjectIntakeStatus, error) {
	j, err := openIntakeJournal(stateDir)
	if err != nil {
		return ProjectIntakeStatus{}, err
	}
	defer j.close()
	if err := j.acquire(); err != nil {
		return ProjectIntakeStatus{}, err
	}
	status, r := intakeStatus(j)
	if status.ID != id || status.State != expected {
		return ProjectIntakeStatus{}, faults.New(faults.CodeValidationFailed, "exact project intake state unavailable")
	}
	if err := j.create(name, composeMarker{Version: 1, ID: id, Digest: r.Digest}); err != nil {
		return ProjectIntakeStatus{}, classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	return ProjectIntakeStatus{State: next, ID: id}, nil
}

// CancelLocalProjectIntake revokes only a prepared or armed, unattempted claim.
func CancelLocalProjectIntake(stateDir, id string) (ProjectIntakeStatus, error) {
	j, err := openIntakeJournal(stateDir)
	if err != nil {
		return ProjectIntakeStatus{}, err
	}
	defer j.close()
	if err := j.acquire(); err != nil {
		return ProjectIntakeStatus{}, err
	}
	status, r := intakeStatus(j)
	if status.ID != id || (status.State != "prepared" && status.State != "armed") {
		return ProjectIntakeStatus{}, faults.New(faults.CodeValidationFailed, "only an unattempted project intake can be cancelled")
	}
	if err := j.create("cancelled.json", composeMarker{Version: 1, ID: id, Digest: r.Digest}); err != nil {
		return ProjectIntakeStatus{}, classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	return ProjectIntakeStatus{State: "cancelled", ID: id}, nil
}

func intakeIdentity(j *composeJournal, r intakeRecord, s *store) bool {
	dev, ino, err := j.queueIdentity()
	return err == nil && dev == r.Device && ino == r.Inode && composeStoreMatches(s, composeRecord{Device: r.Device, Inode: r.Inode})
}

func observeIntake(j *composeJournal, stateDir string, r intakeRecord, allowCompletion bool) (ProjectIntakeStatus, error) {
	status := ProjectIntakeStatus{State: "attempted", ID: r.ID}
	reader, err := OpenReader(stateDir)
	if err != nil {
		status.Observation = "unavailable"
		return status, nil
	}
	if !intakeIdentity(j, r, reader.store) {
		_ = reader.Close()
		return ProjectIntakeStatus{State: "blocked", ID: r.ID}, nil
	}
	if r.Kind == "registration" {
		_, err = reader.RegistrationReceipt(r.Request)
	} else {
		_, err = reader.SubmissionReceipt(r.Request)
	}
	matches := intakeIdentity(j, r, reader.store)
	closeErr := reader.Close()
	if !matches {
		return ProjectIntakeStatus{State: "blocked", ID: r.ID}, nil
	}
	if err != nil || closeErr != nil {
		status.Observation = "unavailable"
		if faults.IsCode(err, faults.CodeNotFound) && closeErr == nil {
			status.Observation = "not_observed"
		}
		return status, nil
	}
	if !allowCompletion {
		status.Observation = "matched_requires_reconcile"
		return status, nil
	}
	current, _ := intakeStatus(j)
	if current.State == "completed" {
		return ProjectIntakeStatus{State: "completed", ID: r.ID, Observation: "matched"}, nil
	}
	if current.State != "attempted" {
		return ProjectIntakeStatus{State: "blocked", ID: r.ID}, nil
	}
	if err := j.create("completed.json", composeMarker{Version: 1, ID: r.ID, Digest: r.Digest}); err != nil {
		return status, classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	return ProjectIntakeStatus{State: "completed", ID: r.ID, Observation: "matched"}, nil
}

// DispatchLocalProjectIntake attempts an exact armed request once. The lock is
// held from claim validation through admission and receipt observation.
func DispatchLocalProjectIntake(stateDir, id string) (ProjectIntakeStatus, error) {
	j, err := openIntakeJournal(stateDir)
	if err != nil {
		return ProjectIntakeStatus{}, err
	}
	defer j.close()
	return dispatchIntakeWithJournal(j, stateDir, id)
}

func dispatchIntakeWithJournal(j *composeJournal, stateDir, id string) (ProjectIntakeStatus, error) {
	if err := j.acquire(); err != nil {
		return ProjectIntakeStatus{}, err
	}
	status, r := intakeStatus(j)
	if status.ID != id || status.State != "armed" {
		return ProjectIntakeStatus{}, faults.New(faults.CodeValidationFailed, "exact armed project intake unavailable; do not retry")
	}
	intake, err := OpenExistingProjectIntake(stateDir)
	if err != nil {
		return ProjectIntakeStatus{}, err
	}
	defer intake.Close()
	if !intakeIdentity(j, r, intake.store) {
		return ProjectIntakeStatus{State: "blocked", ID: id}, nil
	}
	if err := j.create("attempt.json", composeMarker{Version: 1, ID: id, Digest: r.Digest}); err != nil {
		return ProjectIntakeStatus{}, classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	if !intakeIdentity(j, r, intake.store) {
		return ProjectIntakeStatus{State: "blocked", ID: id}, nil
	}
	if r.Kind == "registration" {
		_, err = intake.RegisterProject(r.Request)
	} else {
		_, err = intake.SubmitProject(r.Request)
	}
	return observeIntake(j, stateDir, r, err == nil)
}

// ReconcileLocalProjectIntake only reads the exact receipt; absence is uncertain.
func ReconcileLocalProjectIntake(stateDir, id string) (ProjectIntakeStatus, error) {
	j, err := openIntakeJournal(stateDir)
	if err != nil {
		return ProjectIntakeStatus{}, err
	}
	defer j.close()
	if err := j.acquire(); err != nil {
		return ProjectIntakeStatus{}, err
	}
	status, r := intakeStatus(j)
	if status.ID != id || (status.State != "attempted" && status.State != "completed") {
		return ProjectIntakeStatus{}, faults.New(faults.CodeValidationFailed, "exact attempted project intake unavailable")
	}
	return observeIntake(j, stateDir, r, true)
}

// RetireLocalProjectIntake preserves evidence and refuses unresolved attempts.
func RetireLocalProjectIntake(stateDir, id string) error {
	j, err := openIntakeJournal(stateDir)
	if err != nil {
		return err
	}
	defer j.close()
	if err := j.acquire(); err != nil {
		return err
	}
	status, r := intakeStatus(j)
	if status.ID != id || (status.State != "cancelled" && status.State != "completed") {
		return faults.New(faults.CodeValidationFailed, "only cancelled or verified completed intake can be retired")
	}
	if status.State == "completed" {
		verified, err := observeIntake(j, stateDir, r, true)
		if err != nil || verified.State != "completed" {
			return faults.New(faults.CodeValidationFailed, "project intake receipt could not be reverified")
		}
	}
	var current unix.Stat_t
	info, err := j.dir.Stat()
	if err != nil {
		return err
	}
	opened, ok := info.Sys().(*syscall.Stat_t)
	if !ok || unix.Fstatat(int(j.state.Fd()), j.name, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || uint64(current.Dev) != uint64(opened.Dev) || current.Ino != opened.Ino || current.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return faults.New(faults.CodeStorageFailed, "project intake journal identity changed")
	}
	if err := unix.Renameat2(int(j.state.Fd()), j.name, int(j.state.Fd()), j.name+"-retired-"+id, unix.RENAME_NOREPLACE); err != nil {
		return classifyDiagnostic(faults.CodeStorageFailed, fmt.Errorf("retire project intake: %w", err))
	}
	return classifyDiagnostic(faults.CodeStorageFailed, j.state.Sync())
}
