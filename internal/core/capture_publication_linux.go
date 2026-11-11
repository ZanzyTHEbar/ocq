package core

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"ocq/internal/core/dbquery"
)

// Capture scope contains only stored pointers, never requests or owner tokens.
// Unlike the CLI fallback, the UI requires an existing owned lane and worktree.
type CapturePublicationContext struct {
	ResultPublicationContext
	Lane     string `json:"lane_id"`
	Worktree string `json:"worktree"`
	Base     string `json:"base"`
}

type CapturePreparationRequest struct {
	Context           CapturePublicationContext `json:"context"`
	IgnoredExclusions []string                  `json:"ignored_exclusions"`
	Supersedes        string                    `json:"supersedes_id"`
}

type CapturePublicationStatus struct {
	ResultPublicationStatus
	CaptureID string `json:"capture_id"`
}

func captureJournalName(t ResultPublicationTarget) string { return t.journal() + "-capture" }

func captureContext(s *store, q sqlQueryer, t ResultPublicationTarget) (CapturePublicationContext, error) {
	out := CapturePublicationContext{ResultPublicationContext: ResultPublicationContext{Target: t}}
	var err error
	out.OwnershipDigest, err = publicationOwnership(s, q, t)
	if err != nil {
		return out, err
	}
	queries, ctx := dbquery.New(q), context.Background()
	lane, err := queries.LaneDetailForJob(ctx, dbquery.LaneDetailForJobParams{ProjectID: t.Project, OwnerJobID: t.Job})
	if err != nil {
		return out, err
	}
	j, err := queries.ProjectJobReceiptTargets(ctx, t.Job)
	if err != nil {
		return out, err
	}
	location, err := queries.AgentLocation(ctx, t.Job)
	if err != nil {
		return out, err
	}
	_, req, err := storedRequest(j.Canonical)
	if err != nil || req == nil || j.Role != "implementation" || lane.FsforkPath == "" || lane.FsforkPath != location.Worktree {
		return out, errors.New("exact owned implementation lane/worktree required")
	}
	lanes, err := queries.ProjectLanes(ctx, t.Project)
	if err != nil {
		return out, err
	}
	matched := false
	for _, owned := range lanes {
		if owned.LaneID == lane.LaneID && owned.OwnerJobID == t.Job && owned.Role == "implementation" && owned.BaseCommit == req.Base && owned.FsforkDriver == "git_worktree" {
			matched = true
		}
	}
	if !matched {
		return out, errors.New("capture lane owner/role/base mismatch")
	}
	out.Lane, out.Worktree, out.Base = lane.LaneID, location.Worktree, req.Base
	return out, nil
}

func (r *Reader) CapturePublicationContext(space, project, job string) (CapturePublicationContext, error) {
	base, err := r.ResultPublicationContext(space, project, job)
	if err != nil {
		return CapturePublicationContext{}, err
	}
	var out CapturePublicationContext
	err = r.store.readSchema5(func(q sqlQueryer) error { var e error; out, e = captureContext(r.store, q, base.Target); return e })
	return out, err
}

func captureFence(s *store, request CapturePreparationRequest) func(sqlQueryer) error {
	return func(q sqlQueryer) error {
		current, err := captureContext(s, q, request.Context.Target)
		if err != nil {
			return err
		}
		if current != request.Context {
			return errors.New("capture ownership/lane/base changed")
		}
		return nil
	}
}

func captureReadDir(r *Reader, t ResultPublicationTarget) (*os.File, error) {
	fd, err := syscall.Open(r.store.dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	root := os.NewFile(uintptr(fd), r.store.dir)
	defer root.Close()
	return composeOpenPrivateDirAt(root, captureJournalName(t))
}

// A started record is immutable and precedes the filesystem read. A missing
// completed record means prepare is uncertain, NEVER permission to recapture.
func captureLoad(j *os.File, t ResultPublicationTarget, id string) (CapturePublicationStatus, ResultPublicationPayload, CapturePreparationRequest, preparedCapture) {
	out := CapturePublicationStatus{ResultPublicationStatus: ResultPublicationStatus{State: "blocked", Target: t}, CaptureID: id}
	var p ResultPublicationPayload
	var request CapturePreparationRequest
	var prepared preparedCapture
	b, err := publicationRead(j, id+".started.json")
	if errors.Is(err, syscall.ENOENT) {
		for _, suffix := range []string{".intent.json", ".armed.json", ".attempt.json"} {
			if _, e := publicationRead(j, id+suffix); !errors.Is(e, syscall.ENOENT) {
				return out, p, request, prepared
			}
		}
		out.State = "none"
		return out, p, request, prepared
	}
	if err != nil {
		return out, p, request, prepared
	}
	if _, err = decodeStrictJSONObject(b, composeLimit, &request); err != nil || request.Context.Target != t {
		return out, p, request, prepared
	}
	b, err = publicationRead(j, id+".intent.json")
	if errors.Is(err, syscall.ENOENT) {
		out.State = "preparing"
		return out, p, request, prepared
	}
	var record publicationRecord
	if err != nil || json.Unmarshal(b, &record) != nil || record.Version != 1 || sha256Hex(record.Raw) != record.Digest {
		return out, p, request, prepared
	}
	if _, err = decodeStrictJSONObject(record.Raw, composeLimit, &p); err != nil || !p.valid() || p.Target != t || p.OwnershipDigest != request.Context.OwnershipDigest || p.Supersedes != request.Supersedes {
		return out, p, request, prepared
	}
	m, err := parseManifestBytes([]byte(p.Manifest), "result", t.Project)
	if err != nil || m.Source != "machine_capture" || m.Result == nil {
		return out, p, request, prepared
	}
	r := m.Result
	if r.JobID != t.Job || r.Worktree != request.Context.Worktree || r.Base != request.Context.Base || r.ClaimLabel != "machine_verified_snapshot" || r.ObservedAt == nil || r.BeforeStatusDigest != r.AfterStatusDigest || r.BeforeContentDigest != r.AfterContentDigest || !isValidHexLen(r.AfterStatusDigest, 64) || !isValidHexLen(r.AfterContentDigest, 64) || r.ClaimedValidation.Status != "not_run" || len(r.ClaimedValidation.Commands) != 0 || r.ClaimedValidation.Results != "not_run" || !reflect.DeepEqual(r.IgnoredExclusions, request.IgnoredExclusions) {
		return out, p, request, prepared
	}
	prepared = preparedCapture{*r, p.Supersedes}
	canonical, err := marshalCanonicalManifestJSON(*r)
	if err != nil || string(canonical) != p.Manifest {
		return out, p, request, prepared
	}
	out.State, out.ManifestDigest, out.PayloadDigest, out.OwnershipDigest, out.Kind, out.Source, out.Supersedes = "prepared", m.Digest, record.Digest, p.OwnershipDigest, "result", "machine_capture", p.Supersedes
	for _, step := range []struct{ suffix, state string }{{".armed.json", "armed"}, {".attempt.json", "uncertain"}} {
		b, err = publicationRead(j, id+step.suffix)
		if errors.Is(err, syscall.ENOENT) {
			continue
		}
		var marker publicationMarker
		if err != nil || json.Unmarshal(b, &marker) != nil || marker.Version != 1 || marker.Digest != record.Digest || step.state == "uncertain" && out.State != "armed" {
			out.State = "blocked"
			return out, p, request, prepared
		}
		out.State = step.state
	}
	return out, p, request, prepared
}

func captureInspect(r *Reader, j *os.File, t ResultPublicationTarget, id string) CapturePublicationStatus {
	out, p, request, _ := captureLoad(j, t, id)
	if out.State == "prepared" || out.State == "armed" || out.State == "uncertain" {
		if a, err := publicationReceipt(r, p); err == nil && a.Source == "machine_capture" && a.Verification == "machine_verified_snapshot" {
			out.State = "recorded_observed"
			out.Association = a
			return out
		}
		if out.State != "uncertain" && r.store.readSchema5(captureFence(r.store, request)) != nil {
			out.State = "changed"
		}
	}
	return out
}

func InspectCapturePublication(dir string, t ResultPublicationTarget, id string) (CapturePublicationStatus, error) {
	out := CapturePublicationStatus{ResultPublicationStatus: ResultPublicationStatus{State: "blocked", Target: t}, CaptureID: id}
	if !t.valid() || !isValidHexLen(id, 64) {
		return out, errors.New("invalid capture pointer")
	}
	r, err := OpenReader(dir)
	if err != nil {
		return out, err
	}
	defer r.Close()
	j, err := captureReadDir(r, t)
	if errors.Is(err, syscall.ENOENT) {
		out.State = "none"
		return out, nil
	}
	if err != nil {
		return out, err
	}
	defer j.Close()
	return captureInspect(r, j, t, id), nil
}

func ListCapturePublications(dir string, t ResultPublicationTarget) ([]CapturePublicationStatus, error) {
	if !t.valid() {
		return nil, errors.New("invalid capture target")
	}
	r, err := OpenReader(dir)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	j, err := captureReadDir(r, t)
	if errors.Is(err, syscall.ENOENT) {
		return []CapturePublicationStatus{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer j.Close()
	entries, err := j.ReadDir(1025)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > 1024 {
		return nil, errors.New("capture listing bound; inspect exact pointer")
	}
	ids := []string{}
	for _, e := range entries {
		id := strings.TrimSuffix(e.Name(), ".started.json")
		if id != e.Name() && isValidHexLen(id, 64) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := []CapturePublicationStatus{}
	for _, id := range ids {
		out = append(out, captureInspect(r, j, t, id))
	}
	return out, nil
}

// Only options cross this authenticated input boundary, NEVER manifest bytes.
func PrepareCapturePublication(dir string, t ResultPublicationTarget, id, launch string) (CapturePublicationStatus, error) {
	if !t.valid() || !isValidHexLen(id, 64) || !isValidHexLen(launch, 64) {
		return CapturePublicationStatus{}, errors.New("invalid capture preparation")
	}
	j, err := openLocalJournal(dir, captureJournalName(t))
	if err != nil {
		return CapturePublicationStatus{}, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return CapturePublicationStatus{}, err
	}
	if status, _, _, _ := captureLoad(j.dir, t, id); status.State != "none" {
		return CapturePublicationStatus{}, errors.New("capture already started; inspect only")
	}
	err = j.withComposeConnection(launch, func(conn net.Conn) error {
		// Decimal frame, identical to other private intake boundaries.
		var digits strings.Builder
		var one [1]byte
		for {
			if _, err := io.ReadFull(conn, one[:]); err != nil {
				return err
			}
			if one[0] == '\n' {
				break
			}
			if one[0] < '0' || one[0] > '9' || digits.Len() >= 6 {
				return errors.New("invalid capture frame")
			}
			digits.WriteByte(one[0])
		}
		n, err := strconv.Atoi(digits.String())
		if err != nil || n < 1 || n > composeLimit || strconv.Itoa(n) != digits.String() {
			return errors.New("invalid capture options size")
		}
		raw := make([]byte, n)
		if _, err = io.ReadFull(conn, raw); err != nil {
			return err
		}
		var request CapturePreparationRequest
		if _, err = decodeStrictJSONObject(raw, composeLimit, &request); err != nil || request.Context.Target != t || request.IgnoredExclusions == nil {
			return errors.New("invalid capture options")
		}
		r, err := OpenReader(dir)
		if err != nil {
			return err
		}
		defer r.Close()
		if err = r.store.readSchema5(captureFence(r.store, request)); err != nil {
			return err
		}
		// Persist and sync BEFORE the only filesystem read. Failure leaves a
		// recoverable preparing pointer, not an invitation to run capture again.
		if err = j.create(id+".started.json", request); err != nil {
			return err
		}
		prepared, err := r.store.prepareCapture(CaptureRequest{ProjectID: t.Project, JobID: t.Job, Worktree: request.Context.Worktree, Supersedes: request.Supersedes, IgnoredExclusions: request.IgnoredExclusions})
		if err != nil {
			return err
		}
		var association ProjectResultAssociation
		if err = r.store.publishCapture(prepared, &association, captureFence(r.store, request), true, false); err != nil {
			return err
		}
		manifest, err := marshalCanonicalManifestJSON(prepared.manifest)
		if err != nil {
			return err
		}
		p := ResultPublicationPayload{t, request.Context.OwnershipDigest, string(manifest), request.Supersedes}
		payload, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if err = j.create(id+".intent.json", publicationRecord{1, sha256Hex(payload), payload}); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(conn, "PREPARED "+id)
		return nil
	})
	if err != nil {
		return CapturePublicationStatus{}, err
	}
	return InspectCapturePublication(dir, t, id)
}

func CapturePublicationRendezvous(dir string, t ResultPublicationTarget, launch string) (ComposeRendezvous, error) {
	if !t.valid() || !isValidHexLen(launch, 64) {
		return ComposeRendezvous{}, errors.New("invalid capture rendezvous")
	}
	r, err := OpenReader(dir)
	if err != nil {
		return ComposeRendezvous{}, err
	}
	defer r.Close()
	j, err := captureReadDir(r, t)
	if err != nil {
		return ComposeRendezvous{}, err
	}
	defer j.Close()
	b, err := publicationRead(j, "rendezvous.json")
	var entry ComposeRendezvous
	if err != nil || json.Unmarshal(b, &entry) != nil || entry.Launch != launch || entry.Port < 1 || entry.Port > 65535 || !isValidHexLen(entry.Capability, 64) {
		return entry, errors.New("capture rendezvous unavailable")
	}
	return entry, nil
}

func PreviewCapturePublication(dir string, t ResultPublicationTarget, id, payloadDigest, launch string) (CapturePublicationStatus, error) {
	if !t.valid() || !isValidHexLen(id, 64) || !isValidHexLen(payloadDigest, 64) || !isValidHexLen(launch, 64) {
		return CapturePublicationStatus{}, errors.New("invalid capture preview")
	}
	j, err := openLocalJournal(dir, captureJournalName(t))
	if err != nil {
		return CapturePublicationStatus{}, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return CapturePublicationStatus{}, err
	}
	status, _, _, _ := captureLoad(j.dir, t, id)
	if status.State == "none" || status.State == "blocked" || status.PayloadDigest != payloadDigest {
		return status, errors.New("exact retained capture unavailable")
	}
	b, err := publicationRead(j.dir, id+".intent.json")
	if err != nil {
		return status, err
	}
	var record publicationRecord
	if err = json.Unmarshal(b, &record); err != nil {
		return status, err
	}
	err = j.withComposeConnection(launch, func(conn net.Conn) error {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], uint32(len(record.Raw)))
		if _, err := conn.Write(header[:]); err != nil {
			return err
		}
		_, err := conn.Write(record.Raw)
		return err
	})
	if err != nil {
		return status, err
	}
	return InspectCapturePublication(dir, t, id)
}

func ArmCapturePublication(dir string, t ResultPublicationTarget, id, payloadDigest string) (CapturePublicationStatus, error) {
	return sendCapturePublication(dir, t, id, payloadDigest, false)
}
func AttemptCapturePublication(dir string, t ResultPublicationTarget, id, payloadDigest string) (CapturePublicationStatus, error) {
	return sendCapturePublication(dir, t, id, payloadDigest, true)
}
func sendCapturePublication(dir string, t ResultPublicationTarget, id, payloadDigest string, attempt bool) (CapturePublicationStatus, error) {
	if !t.valid() || !isValidHexLen(id, 64) || !isValidHexLen(payloadDigest, 64) {
		return CapturePublicationStatus{}, errors.New("invalid capture claim")
	}
	j, err := openLocalJournal(dir, captureJournalName(t))
	if err != nil {
		return CapturePublicationStatus{}, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return CapturePublicationStatus{}, err
	}
	status, _, request, prepared := captureLoad(j.dir, t, id)
	expected, suffix := "prepared", ".armed.json"
	if attempt {
		expected, suffix = "armed", ".attempt.json"
	}
	if status.State != expected || status.PayloadDigest != payloadDigest {
		return status, errors.New("capture claim consumed or mismatched; check only")
	}
	r, err := OpenReader(dir)
	if err != nil {
		return status, err
	}
	var association ProjectResultAssociation
	err = r.store.publishCapture(prepared, &association, captureFence(r.store, request), true, false)
	err = errors.Join(err, r.Close())
	if err != nil {
		return status, err
	}
	if err = j.syncFile(id + ".intent.json"); err != nil {
		return status, err
	}
	if attempt {
		if err = j.syncFile(id + ".armed.json"); err != nil {
			return status, err
		}
	}
	if err = j.create(id+suffix, publicationMarker{1, payloadDigest}); err != nil {
		return status, err
	}
	if attempt {
		// Existing-store writer only; exact reviewed bytes, NO filesystem read.
		w, err := OpenExistingResultPublisher(dir)
		if err == nil {
			_ = w.store.publishCapture(prepared, &association, captureFence(w.store, request), false, false)
			_ = w.Close()
		}
	}
	return InspectCapturePublication(dir, t, id)
}
