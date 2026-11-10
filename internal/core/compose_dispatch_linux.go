package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/ZanzyTHEbar/faults-go"
	"golang.org/x/sys/unix"

	"ocq/internal/core/dbquery"
)

func (j *composeJournal) record(id string) (composeRecord, error) {
	var record composeRecord
	b, err := j.read("intent.json", 2*composeLimit)
	if err != nil {
		return record, classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	if json.Unmarshal(b, &record) != nil || record.ID != id || sha256Hex(record.Request) != record.Digest {
		return composeRecord{}, faults.New(faults.CodeInvariantFailed, "compose intent changed")
	}
	return record, nil
}

func (j *composeJournal) identityMatches(r composeRecord) bool {
	dev, ino, err := j.queueIdentity()
	return err == nil && dev == r.Device && ino == r.Inode
}

// The supplied state path may traverse a retargeted parent symlink between
// opening the journal and opening SQLite. Check the actual store path as well
// as the journal's pinned state directory before and after each database use.
func composeStoreMatches(s *store, r composeRecord) bool {
	f, err := privateFile(filepath.Join(s.dir, "queue.db"), os.O_RDONLY)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Nlink == 1 && strconv.FormatUint(uint64(st.Dev), 10) == r.Device && strconv.FormatUint(st.Ino, 10) == r.Inode
}

func composeOptionalMatches(actual *string, expected string) bool {
	if expected == "" {
		return actual == nil
	}
	return actual != nil && *actual == expected
}

func composeMailMatches(m ProjectInboxMessage, r composeRecord, req MailRequest, path string) bool {
	return m.ID == deterministicMessageID(r.Key) && m.Sequence > 0 && m.Key == r.Key &&
		m.ProjectID == req.ProjectID && m.Sender == req.From && m.Recipient == req.To &&
		m.LaneID == req.LaneID && m.JobID == req.JobID && m.Kind == req.Kind &&
		m.Subject == req.Subject && m.Body == req.Body && m.FsforkPath == path &&
		composeOptionalMatches(m.ManifestDigest, req.Manifest) && m.AssocID == nil && composeOptionalMatches(m.ReplyToID, req.ReplyTo)
}

// observeComposeLocked never calls Send. An absent, mismatched or unavailable
// row remains uncertain even when a prior client reported an error.
func observeComposeLocked(j *composeJournal, stateDir string, record composeRecord, status ComposeStatus, allowCompletion bool) (ComposeStatus, error) {
	var req MailRequest
	if json.Unmarshal(record.Request, &req) != nil {
		return ComposeStatus{State: "blocked", ID: record.ID}, nil
	}
	b, err := j.read("dispatch.json", 4096)
	if err != nil {
		return ComposeStatus{State: "blocked", ID: record.ID}, nil
	}
	var marker composeMarker
	if json.Unmarshal(b, &marker) != nil || marker.ID != record.ID || marker.Digest != record.Digest {
		return ComposeStatus{State: "blocked", ID: record.ID}, nil
	}
	path := req.FsforkPath
	if (marker.Version == 2 || marker.Version == 3) && marker.FsforkPath != nil && marker.ExpectedLanePath != nil {
		path = *marker.FsforkPath
		if (req.FsforkPath != "" && path != req.FsforkPath) || (req.FsforkPath == "" && path != *marker.ExpectedLanePath) {
			return ComposeStatus{State: "blocked", ID: record.ID}, nil
		}
	} else if marker.Version != 1 || path == "" {
		// Legacy markers did not capture the effective default lane path.
		return ComposeStatus{State: "dispatch_intent", ID: record.ID, Observation: "path_unknown"}, nil
	}
	if !j.identityMatches(record) {
		return ComposeStatus{State: "blocked", ID: record.ID}, nil
	}
	reader, err := OpenReader(stateDir)
	if err != nil {
		return ComposeStatus{State: "dispatch_intent", ID: record.ID, Observation: "unavailable"}, nil
	}
	if !composeStoreMatches(reader.store, record) {
		_ = reader.Close()
		return ComposeStatus{State: "blocked", ID: record.ID}, nil
	}
	message, readErr := reader.MailByKey(req.ProjectID, record.Key)
	storeMatches := composeStoreMatches(reader.store, record)
	closeErr := reader.Close()
	if !storeMatches || !j.identityMatches(record) {
		return ComposeStatus{State: "blocked", ID: record.ID}, nil
	}
	if readErr != nil || closeErr != nil {
		observation := "unavailable"
		if faults.IsCode(readErr, faults.CodeNotFound) && closeErr == nil {
			observation = "not_observed"
		}
		return ComposeStatus{State: "dispatch_intent", ID: record.ID, Observation: observation}, nil
	}
	if !composeMailMatches(message, record, req, path) {
		return ComposeStatus{State: "dispatch_intent", ID: record.ID, Observation: "mismatch"}, nil
	}
	if !allowCompletion {
		return ComposeStatus{State: "dispatch_intent", ID: record.ID, Observation: "matched_requires_reconcile"}, nil
	}
	if status.State == "completed" {
		b, err := j.read("completed.json", 4096)
		var complete composeCompletion
		if err != nil || json.Unmarshal(b, &complete) != nil || complete.Message != message.ID || complete.Sequence != message.Sequence {
			return ComposeStatus{State: "blocked", ID: record.ID}, nil
		}
		return ComposeStatus{State: "completed", ID: record.ID, Observation: "matched"}, nil
	}
	if err := j.create("completed.json", composeCompletion{Version: 1, ID: record.ID, Digest: record.Digest, Message: message.ID, Sequence: message.Sequence}); err != nil {
		return ComposeStatus{State: "dispatch_intent", ID: record.ID, Observation: "completion_not_durable"}, classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	return ComposeStatus{State: "completed", ID: record.ID, Observation: "matched"}, nil
}

// ArmLocalComposeIntent durably records confirmation before a send process may
// be launched. A claim alone never sends and can be revoked before an attempt.
func ArmLocalComposeIntent(stateDir, id string) (ComposeStatus, error) {
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
	record, err := j.record(id)
	if err != nil {
		return ComposeStatus{}, err
	}
	var req MailRequest
	if json.Unmarshal(record.Request, &req) != nil {
		return ComposeStatus{}, faults.New(faults.CodeInvariantFailed, "invalid retained compose request")
	}
	req.Key = record.Key
	sender, err := OpenExistingMailSender(stateDir)
	if err != nil {
		return ComposeStatus{}, err
	}
	defer sender.Close()
	if !j.identityMatches(record) || !composeStoreMatches(sender.store, record) {
		return ComposeStatus{State: "blocked", ID: id}, nil
	}
	owner, err := dbquery.New(sender.store.db).LaneOwnerAndPath(context.Background(), dbquery.LaneOwnerAndPathParams{ProjectID: req.ProjectID, LaneID: req.LaneID})
	if errors.Is(err, sql.ErrNoRows) {
		return ComposeStatus{}, faults.New(faults.CodeNotFound, "compose lane not found")
	}
	if err != nil {
		return ComposeStatus{}, classifyDiagnostic(faults.CodeDatabaseRead, err)
	}
	if owner.OwnerJobID != req.JobID {
		return ComposeStatus{}, faults.New(faults.CodeValidationFailed, "compose lane owner mismatch")
	}
	path := req.FsforkPath
	if path == "" {
		path = owner.FsforkPath
	}
	if !j.identityMatches(record) || !composeStoreMatches(sender.store, record) {
		return ComposeStatus{State: "blocked", ID: id}, nil
	}
	marker := composeMarker{Version: 3, ID: id, Digest: record.Digest, ExpectedLanePath: &owner.FsforkPath, FsforkPath: &path}
	if err := j.create("dispatch.json", marker); err != nil {
		return ComposeStatus{}, classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	return ComposeStatus{State: "armed", ID: id}, nil
}

// DispatchLocalComposeIntent makes at most one attempt for an exact, still
// armed claim. It journals the attempt before Send and holds the lock throughout.
func DispatchLocalComposeIntent(stateDir, id string) (ComposeStatus, error) {
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
	if err != nil || status.State != "armed" || status.ID != id {
		return ComposeStatus{}, faults.New(faults.CodeValidationFailed, "exact armed compose intent unavailable; do not retry")
	}
	record, err := j.record(id)
	if err != nil {
		return ComposeStatus{}, err
	}
	var req MailRequest
	if json.Unmarshal(record.Request, &req) != nil {
		return ComposeStatus{}, faults.New(faults.CodeInvariantFailed, "invalid retained compose request")
	}
	req.Key = record.Key
	b, err := j.read("dispatch.json", 4096)
	var marker composeMarker
	if err != nil || json.Unmarshal(b, &marker) != nil || marker.Version != 3 || marker.ID != id || marker.Digest != record.Digest || marker.ExpectedLanePath == nil || marker.FsforkPath == nil || (req.FsforkPath != "" && req.FsforkPath != *marker.FsforkPath) || (req.FsforkPath == "" && *marker.ExpectedLanePath != *marker.FsforkPath) {
		return ComposeStatus{State: "blocked", ID: id}, nil
	}
	sender, err := OpenExistingMailSender(stateDir)
	if err != nil {
		return ComposeStatus{}, err
	}
	defer sender.Close()
	if !j.identityMatches(record) || !composeStoreMatches(sender.store, record) {
		return ComposeStatus{State: "blocked", ID: id}, nil
	}
	if err := j.create("attempt.json", composeMarker{Version: 1, ID: id, Digest: record.Digest}); err != nil {
		return ComposeStatus{}, classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	status, err = j.status()
	if err != nil || status.State != "dispatch_intent" || status.ID != id || !composeStoreMatches(sender.store, record) {
		return ComposeStatus{State: "blocked", ID: id}, nil
	}
	// This is the only Send call. Even an error may follow a committed write.
	_, sendErr := sender.sendAtLanePath(req, *marker.ExpectedLanePath)
	return observeComposeLocked(j, stateDir, record, status, sendErr == nil)
}

// ReconcileLocalComposeIntent performs only an exact stored read-back; it never
// sends or retries. A missing or mismatched row does not release the intent.
func ReconcileLocalComposeIntent(stateDir, id string) (ComposeStatus, error) {
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
	if err != nil || status.ID != id || (status.State != "dispatch_intent" && status.State != "completed") {
		return ComposeStatus{}, faults.New(faults.CodeValidationFailed, "exact dispatched compose intent unavailable")
	}
	record, err := j.record(id)
	if err != nil {
		return ComposeStatus{}, err
	}
	return observeComposeLocked(j, stateDir, record, status, true)
}

// CancelLocalComposeIntent revokes a prepared or armed intent before an attempt.
func CancelLocalComposeIntent(stateDir, id string) (ComposeStatus, error) {
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
	if err != nil || (status.State != "prepared" && status.State != "armed") || status.ID != id {
		return ComposeStatus{}, faults.New(faults.CodeValidationFailed, "only an exact unattempted compose intent can be cancelled")
	}
	record, err := j.record(id)
	if err != nil {
		return ComposeStatus{}, err
	}
	if err := j.create("cancelled.json", composeMarker{Version: 1, ID: id, Digest: record.Digest}); err != nil {
		return ComposeStatus{}, classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	if status.State == "armed" {
		return ComposeStatus{State: "cancelled", ID: id, Observation: "revoked_before_attempt"}, nil
	}
	return ComposeStatus{State: "cancelled", ID: id}, nil
}

// RetireLocalComposeIntent atomically moves the complete journal into a retained
// private archive. Interrupted moves fail closed and do not delete user content.
func RetireLocalComposeIntent(stateDir, id string) error {
	if !isValidHexLen(id, 64) {
		return faults.New(faults.CodeValidationFailed, "invalid compose intent identifier")
	}
	j, err := openComposeJournal(stateDir)
	if err != nil {
		return err
	}
	defer j.close()
	if err := j.acquire(); err != nil {
		return err
	}
	status, err := j.status()
	if err != nil || status.ID != id || (status.State != "cancelled" && status.State != "completed") {
		return faults.New(faults.CodeValidationFailed, "only a cancelled or verified completed compose intent can be retired")
	}
	record, err := j.record(id)
	if err != nil {
		return err
	}
	if status.State == "completed" {
		verified, err := observeComposeLocked(j, stateDir, record, status, true)
		if err != nil || verified.State != "completed" {
			return faults.New(faults.CodeValidationFailed, "completed compose receipt could not be reverified")
		}
	}
	if !j.identityMatches(record) {
		return faults.New(faults.CodeValidationFailed, "compose queue identity changed")
	}
	var current unix.Stat_t
	if err := unix.Fstatat(int(j.state.Fd()), "herdr-compose", &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	info, err := j.dir.Stat()
	if err != nil {
		return classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	opened := info.Sys().(*syscall.Stat_t)
	if uint64(current.Dev) != uint64(opened.Dev) || current.Ino != opened.Ino || current.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return faults.New(faults.CodeValidationFailed, "compose journal identity changed")
	}
	destination := "herdr-compose-retired-" + id
	if err := unix.Renameat2(int(j.state.Fd()), "herdr-compose", int(j.state.Fd()), destination, unix.RENAME_NOREPLACE); err != nil {
		return classifyDiagnostic(faults.CodeStorageFailed, fmt.Errorf("retire compose journal: %w", err))
	}
	return classifyDiagnostic(faults.CodeStorageFailed, j.state.Sync())
}
