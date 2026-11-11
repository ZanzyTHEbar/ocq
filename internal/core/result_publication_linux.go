package core

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"ocq/internal/core/dbquery"
)

// ResultPublisher is deliberately narrower than Queue: no initialization,
// execution, capture, permission answers or integration authorization.
type ResultPublisher struct{ store *store }

func OpenExistingResultPublisher(dir string) (*ResultPublisher, error) {
	s, err := openExistingWritableStore(dir)
	if err != nil {
		return nil, err
	}
	s.existingSchemaOnly = true
	return &ResultPublisher{s}, nil
}
func (w *ResultPublisher) Close() error { return w.store.database.Close() }

type ResultPublicationTarget struct {
	CommandResolutionTarget
	Project string `json:"project_id"`
}

func (t ResultPublicationTarget) valid() bool {
	return t.CommandResolutionTarget.valid() && isValidProjectID(t.Project)
}
func (t ResultPublicationTarget) journal() string {
	// Space/copy, manifest contents and supersedes do not create a second
	// authorization. Each manifest has immutable files in this job's journal.
	return "herdr-result-publish-" + sha256Hex([]byte(t.Queue+"\n"+t.Project+"\n"+t.Job+"\n"+t.Sequence))
}

// Raw manifest/evidence is private: only the authenticated preview returns it.
type ResultPublicationPayload struct {
	Target          ResultPublicationTarget `json:"target"`
	OwnershipDigest string                  `json:"ownership_sha256"`
	Manifest        string                  `json:"manifest"`
	Supersedes      string                  `json:"supersedes_id"`
}

func (p ResultPublicationPayload) valid() bool {
	return p.Target.valid() && isValidHexLen(p.OwnershipDigest, 64) && len(p.Manifest) > 0 && len(p.Manifest) <= maxManifestBytes && utf8.ValidString(p.Manifest) && (p.Supersedes == "" || isValidAssociationID(p.Supersedes))
}

type ResultPublicationContext struct {
	Target          ResultPublicationTarget `json:"target"`
	OwnershipDigest string                  `json:"ownership_sha256"`
}

// Fingerprint receipt, project linkage, location and lane ownership in the
// caller's SQLite snapshot. The fingerprint never exposes request/token bytes.
func publicationOwnership(s *store, q sqlQueryer, t ResultPublicationTarget) (string, error) {
	identity, err := notificationQueueIdentity(s)
	if err != nil {
		return "", err
	}
	queries := dbquery.New(q)
	ctx := context.Background()
	j, err := queries.QueueJobByID(ctx, t.Job)
	if err != nil {
		return "", err
	}
	if identity != t.Queue || j.Kind != "agent" || strconv.FormatInt(j.Sequence, 10) != t.Sequence {
		return "", errors.New("exact publication queue/receipt changed")
	}
	meta, err := queries.ProjectJobReceiptTargets(ctx, t.Job)
	if err != nil {
		return "", err
	}
	if meta.ProjectID != t.Project {
		return "", errors.New("publication project/job mismatch")
	}
	location, err := queries.AgentLocation(ctx, t.Job)
	if err != nil {
		return "", err
	}
	lane, err := queries.LaneDetailForJob(ctx, dbquery.LaneDetailForJobParams{ProjectID: t.Project, OwnerJobID: t.Job})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	// Seal digest changes do not change ownership or silently select a newer
	// result: exact supersedes linkage is separately checked by RecordResult.
	lane.SealedManifestDigest = sql.NullString{}
	b, err := json.Marshal(struct {
		Meta     dbquery.ProjectJobReceiptTargetsRow
		Location dbquery.AgentLocationRow
		Lane     dbquery.LaneDetailForJobRow
	}{meta, location, lane})
	if err != nil {
		return "", err
	}
	return sha256Hex(b), nil
}
func (r *Reader) ResultPublicationContext(space, project, job string) (ResultPublicationContext, error) {
	var out ResultPublicationContext
	err := r.store.readSchema5(func(q sqlQueryer) error {
		identity, err := notificationQueueIdentity(r.store)
		if err != nil {
			return err
		}
		j, err := dbquery.New(q).QueueJobByID(context.Background(), job)
		if err != nil {
			return err
		}
		out.Target = ResultPublicationTarget{CommandResolutionTarget{Machine: "local", Space: space, Queue: identity, Job: job, Sequence: strconv.FormatInt(j.Sequence, 10)}, project}
		if !out.Target.valid() {
			return errors.New("invalid publication context")
		}
		out.OwnershipDigest, err = publicationOwnership(r.store, q, out.Target)
		return err
	})
	return out, err
}
func publicationFence(s *store, p ResultPublicationPayload) func(sqlQueryer) error {
	return func(q sqlQueryer) error {
		current, err := publicationOwnership(s, q, p.Target)
		if err != nil {
			return err
		}
		if current != p.OwnershipDigest {
			return errors.New("exact publication ownership changed")
		}
		return nil
	}
}
func (w *ResultPublisher) Publish(p ResultPublicationPayload) (ProjectResultAssociation, error) {
	if !p.valid() {
		return ProjectResultAssociation{}, errors.New("invalid publication payload")
	}
	return w.store.recordResult(p.Target.Project, p.Target.Job, []byte(p.Manifest), p.Supersedes, publicationFence(w.store, p), false)
}
func validatePublication(r *Reader, p ResultPublicationPayload) (ProjectResultAssociation, error) {
	if !p.valid() {
		return ProjectResultAssociation{}, errors.New("invalid publication payload")
	}
	return r.store.recordResult(p.Target.Project, p.Target.Job, []byte(p.Manifest), p.Supersedes, publicationFence(r.store, p), true)
}

type PublicationAssociation struct {
	ID             string  `json:"association_id"`
	Project        string  `json:"project_id"`
	Job            string  `json:"job_id"`
	Kind           string  `json:"kind"`
	ManifestDigest string  `json:"manifest_sha256"`
	Supersedes     *string `json:"supersedes_id"`
	Sequence       string  `json:"sequence"`
	RecordedAt     string  `json:"recorded_at"`
	Source         string  `json:"source"`
	Verification   string  `json:"verification"`
}

func publicationAssociation(a ProjectResultAssociation) PublicationAssociation {
	return PublicationAssociation{a.ID, a.ProjectID, a.JobID, a.Kind, a.ManifestDigest, a.SupersedesID, strconv.FormatInt(a.Sequence, 10), a.RecordedAt, a.Source, a.Verification}
}

type ResultPublicationStatus struct {
	State           string                  `json:"state"`
	Target          ResultPublicationTarget `json:"target"`
	ManifestDigest  string                  `json:"manifest_sha256"`
	PayloadDigest   string                  `json:"payload_sha256,omitempty"`
	OwnershipDigest string                  `json:"ownership_sha256,omitempty"`
	Kind            string                  `json:"kind,omitempty"`
	Source          string                  `json:"source,omitempty"`
	Supersedes      string                  `json:"supersedes_id"`
	Association     *PublicationAssociation `json:"association,omitempty"`
}
type publicationRecord struct {
	Version int    `json:"version"`
	Digest  string `json:"payload_sha256"`
	Raw     []byte `json:"payload_base64"`
}
type publicationMarker struct {
	Version int    `json:"version"`
	Digest  string `json:"payload_sha256"`
}

const publicationRecordLimit = 256 << 10

func publicationRead(dir *os.File, name string) ([]byte, error) {
	f, err := composePrivateAt(dir, name, syscall.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, publicationRecordLimit+1))
	if len(b) > publicationRecordLimit {
		return nil, errors.New("publication record exceeds limit")
	}
	return b, err
}
func publicationLoad(dir *os.File, t ResultPublicationTarget, digest string) (ResultPublicationStatus, ResultPublicationPayload) {
	out := ResultPublicationStatus{State: "blocked", Target: t, ManifestDigest: digest}
	var p ResultPublicationPayload
	b, err := publicationRead(dir, digest+".intent.json")
	if errors.Is(err, syscall.ENOENT) {
		for _, suffix := range []string{".armed.json", ".attempt.json"} {
			if _, e := publicationRead(dir, digest+suffix); !errors.Is(e, syscall.ENOENT) {
				return out, p
			}
		}
		out.State = "none"
		return out, p
	}
	var rec publicationRecord
	if err != nil || json.Unmarshal(b, &rec) != nil || rec.Version != 1 || sha256Hex(rec.Raw) != rec.Digest {
		return out, p
	}
	if _, err = decodeStrictJSONObject(rec.Raw, composeLimit, &p); err != nil || !p.valid() || p.Target != t || sha256Hex([]byte(p.Manifest)) != digest {
		return out, p
	}
	m, err := parseManifestBytes([]byte(p.Manifest), "", t.Project)
	if err != nil || (m.Kind != "result" && m.Kind != "review_result") || m.Source == "machine_capture" {
		return out, p
	}
	out.State = "prepared"
	out.PayloadDigest = rec.Digest
	out.OwnershipDigest = p.OwnershipDigest
	out.Kind = m.Kind
	out.Source = m.Source
	out.Supersedes = p.Supersedes
	for _, step := range []struct{ suffix, state string }{{".armed.json", "armed"}, {".attempt.json", "uncertain"}} {
		b, err = publicationRead(dir, digest+step.suffix)
		if errors.Is(err, syscall.ENOENT) {
			continue
		}
		var marker publicationMarker
		if err != nil || json.Unmarshal(b, &marker) != nil || marker.Version != 1 || marker.Digest != rec.Digest || step.state == "uncertain" && out.State != "armed" {
			out.State = "blocked"
			return out, p
		}
		out.State = step.state
	}
	return out, p
}

// Exact stored association/content/lineage, not a job terminal state and not
// proof of caller causality. No seal/mail repair, acknowledgement or retry.
func publicationReceipt(r *Reader, p ResultPublicationPayload) (*PublicationAssociation, error) {
	var out *PublicationAssociation
	err := r.store.readSchema5(func(q sqlQueryer) error {
		identity, err := notificationQueueIdentity(r.store)
		if err != nil {
			return err
		}
		j, err := dbquery.New(q).QueueJobByID(context.Background(), p.Target.Job)
		if err != nil {
			return err
		}
		if identity != p.Target.Queue || strconv.FormatInt(j.Sequence, 10) != p.Target.Sequence {
			return errors.New("exact queue/receipt changed")
		}
		m, err := parseManifestBytes([]byte(p.Manifest), "", p.Target.Project)
		if err != nil {
			return err
		}
		row, err := dbquery.New(q).ExistingAssociation(context.Background(), dbquery.ExistingAssociationParams{ProjectID: p.Target.Project, JobID: p.Target.Job, Kind: m.Kind, ManifestDigest: m.Digest})
		if err != nil {
			return err
		}
		var sup *string
		if p.Supersedes != "" {
			sup = &p.Supersedes
		}
		if !nullStringEqual(row.SupersedesID, sup) || row.ID != deterministicAssociationID(p.Target.Project, p.Target.Job, m.Kind, m.Digest, p.Supersedes) {
			return errors.New("exact publication lineage differs")
		}
		if err := verifyStoredManifest(context.Background(), q, m); err != nil {
			return err
		}
		a := publicationAssociation(summarizeAssociation(row.ID, row.Sequence, p.Target.Project, p.Target.Job, m.Kind, m.Digest, sup, row.RecordedAt, &m, true))
		out = &a
		return nil
	})
	return out, err
}
func publicationInspect(r *Reader, dir *os.File, t ResultPublicationTarget, digest string) ResultPublicationStatus {
	out, p := publicationLoad(dir, t, digest)
	if out.State == "none" || out.State == "blocked" {
		return out
	}
	if a, err := publicationReceipt(r, p); err == nil {
		out.State = "recorded_observed"
		out.Association = a
		return out
	}
	if out.State == "prepared" || out.State == "armed" {
		err := r.store.readSchema5(publicationFence(r.store, p))
		if err != nil {
			out.State = "changed"
		}
	}
	return out
}
func publicationReadDir(r *Reader, t ResultPublicationTarget) (*os.File, error) {
	fd, err := syscall.Open(r.store.dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	root := os.NewFile(uintptr(fd), r.store.dir)
	defer root.Close()
	return composeOpenPrivateDirAt(root, t.journal())
}
func InspectResultPublication(dir string, t ResultPublicationTarget, digest string) (ResultPublicationStatus, error) {
	out := ResultPublicationStatus{State: "blocked", Target: t, ManifestDigest: digest}
	if !t.valid() || !isValidHexLen(digest, 64) {
		return out, errors.New("invalid publication target/digest")
	}
	r, err := OpenReader(dir)
	if err != nil {
		return out, err
	}
	defer r.Close()
	j, err := publicationReadDir(r, t)
	if errors.Is(err, syscall.ENOENT) {
		out.State = "none"
		return out, nil
	}
	if err != nil {
		return out, err
	}
	defer j.Close()
	return publicationInspect(r, j, t, digest), nil
}
func ListResultPublications(dir string, t ResultPublicationTarget) ([]ResultPublicationStatus, error) {
	if !t.valid() {
		return nil, errors.New("invalid publication target")
	}
	r, err := OpenReader(dir)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	if err = r.store.readSchema5(func(q sqlQueryer) error { _, err := publicationOwnership(r.store, q, t); return err }); err != nil {
		return nil, err
	}
	j, err := publicationReadDir(r, t)
	if errors.Is(err, syscall.ENOENT) {
		return []ResultPublicationStatus{}, nil
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
		return nil, errors.New("publication listing limit; inspect an exact known digest")
	}
	var digests []string
	for _, e := range entries {
		d := strings.TrimSuffix(e.Name(), ".intent.json")
		if d != e.Name() && isValidHexLen(d, 64) {
			digests = append(digests, d)
		}
	}
	sort.Strings(digests)
	out := []ResultPublicationStatus{}
	for _, d := range digests {
		out = append(out, publicationInspect(r, j, t, d))
	}
	return out, nil
}

func ReceiveResultPublication(dir string, t ResultPublicationTarget, launch string) (ResultPublicationStatus, error) {
	if !t.valid() || !isValidHexLen(launch, 64) {
		return ResultPublicationStatus{}, errors.New("invalid publication launch")
	}
	j, err := openLocalJournal(dir, t.journal())
	if err != nil {
		return ResultPublicationStatus{}, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return ResultPublicationStatus{}, err
	}
	var retainedDigest string
	err = j.withComposeConnection(launch, func(conn net.Conn) error {
		// Same decimal exact-byte frame used by native intake/resolution.
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
				return errors.New("invalid publication frame")
			}
			digits.WriteByte(one[0])
		}
		n, err := strconv.Atoi(digits.String())
		if err != nil || n < 1 || n > composeLimit || strconv.Itoa(n) != digits.String() {
			return errors.New("invalid publication size")
		}
		raw := make([]byte, n)
		if _, err = io.ReadFull(conn, raw); err != nil {
			return err
		}
		var p ResultPublicationPayload
		if _, err = decodeStrictJSONObject(raw, composeLimit, &p); err != nil || !p.valid() || p.Target != t {
			return errors.New("invalid publication payload")
		}
		digest := sha256Hex([]byte(p.Manifest))
		status, _ := publicationLoad(j.dir, t, digest)
		if status.State != "none" {
			return errors.New("publication already retained; inspect only")
		}
		r, err := OpenReader(dir)
		if err != nil {
			return err
		}
		_, err = validatePublication(r, p)
		err = errors.Join(err, r.Close())
		if err != nil {
			return err
		}
		rec := publicationRecord{1, sha256Hex(raw), raw}
		b, err := json.Marshal(rec)
		if err != nil || len(b) > publicationRecordLimit {
			return errors.New("publication journal exceeds bound")
		}
		if err = j.create(digest+".intent.json", rec); err != nil {
			return err
		}
		retainedDigest = digest
		_, _ = fmt.Fprintln(conn, "PREPARED "+digest)
		return nil
	})
	if err != nil {
		return ResultPublicationStatus{}, err
	}
	// The receive acknowledgement and stdout both omit the private bytes.
	return InspectResultPublication(dir, t, retainedDigest)
}
func ResultPublicationRendezvous(dir string, t ResultPublicationTarget, launch string) (ComposeRendezvous, error) {
	if !t.valid() || !isValidHexLen(launch, 64) {
		return ComposeRendezvous{}, errors.New("invalid publication rendezvous")
	}
	r, err := OpenReader(dir)
	if err != nil {
		return ComposeRendezvous{}, err
	}
	defer r.Close()
	j, err := publicationReadDir(r, t)
	if err != nil {
		return ComposeRendezvous{}, err
	}
	defer j.Close()
	b, err := publicationRead(j, "rendezvous.json")
	var entry ComposeRendezvous
	if err != nil || json.Unmarshal(b, &entry) != nil || entry.Launch != launch || entry.Port < 1 || entry.Port > 65535 || !isValidHexLen(entry.Capability, 64) {
		return entry, errors.New("publication rendezvous unavailable")
	}
	return entry, nil
}
func PreviewResultPublication(dir string, t ResultPublicationTarget, digest, payloadDigest, launch string) (ResultPublicationStatus, error) {
	if !t.valid() || !isValidHexLen(digest, 64) || !isValidHexLen(payloadDigest, 64) || !isValidHexLen(launch, 64) {
		return ResultPublicationStatus{}, errors.New("invalid publication preview")
	}
	j, err := openLocalJournal(dir, t.journal())
	if err != nil {
		return ResultPublicationStatus{}, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return ResultPublicationStatus{}, err
	}
	status, _ := publicationLoad(j.dir, t, digest)
	if status.State == "none" || status.State == "blocked" || status.PayloadDigest != payloadDigest {
		return status, errors.New("exact retained publication unavailable")
	}
	b, err := publicationRead(j.dir, digest+".intent.json")
	if err != nil {
		return status, err
	}
	var rec publicationRecord
	if err = json.Unmarshal(b, &rec); err != nil {
		return status, err
	}
	err = j.withComposeConnection(launch, func(conn net.Conn) error {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], uint32(len(rec.Raw)))
		if _, err := conn.Write(header[:]); err != nil {
			return err
		}
		_, err := conn.Write(rec.Raw)
		return err
	})
	if err != nil {
		return status, err
	}
	return InspectResultPublication(dir, t, digest)
}
func ArmResultPublication(dir string, t ResultPublicationTarget, digest, payloadDigest string) (ResultPublicationStatus, error) {
	return sendResultPublication(dir, t, digest, payloadDigest, false)
}
func AttemptResultPublication(dir string, t ResultPublicationTarget, digest, payloadDigest string) (ResultPublicationStatus, error) {
	return sendResultPublication(dir, t, digest, payloadDigest, true)
}
func sendResultPublication(dir string, t ResultPublicationTarget, digest, payloadDigest string, attempt bool) (ResultPublicationStatus, error) {
	if !t.valid() || !isValidHexLen(digest, 64) || !isValidHexLen(payloadDigest, 64) {
		return ResultPublicationStatus{}, errors.New("invalid publication claim")
	}
	j, err := openLocalJournal(dir, t.journal())
	if err != nil {
		return ResultPublicationStatus{}, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return ResultPublicationStatus{}, err
	}
	status, p := publicationLoad(j.dir, t, digest)
	expected, suffix := "prepared", ".armed.json"
	if attempt {
		expected, suffix = "armed", ".attempt.json"
	}
	if status.State != expected || status.PayloadDigest != payloadDigest {
		return status, errors.New("publication claim consumed or mismatched; check only")
	}
	r, err := OpenReader(dir)
	if err != nil {
		return status, err
	}
	_, err = validatePublication(r, p)
	err = errors.Join(err, r.Close())
	if err != nil {
		return InspectResultPublication(dir, t, digest)
	}
	if err = j.syncFile(digest + ".intent.json"); err != nil {
		return status, err
	}
	if attempt {
		if err = j.syncFile(digest + ".armed.json"); err != nil {
			return status, err
		}
	}
	if err = j.create(digest+suffix, publicationMarker{1, payloadDigest}); err != nil {
		return status, err
	}
	if attempt {
		// Durable attempt precedes opening the writer. Never retry an uncertain
		// commit, and never infer publication from a terminal job or client exit.
		w, err := OpenExistingResultPublisher(dir)
		if err == nil {
			_, _ = w.Publish(p)
			_ = w.Close()
		}
	}
	return InspectResultPublication(dir, t, digest)
}
