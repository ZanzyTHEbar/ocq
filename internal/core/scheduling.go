package core

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

type SchedulingPolicy struct {
	Schema        int    `json:"schema"`
	Policy        string `json:"policy"`
	QueueIdentity string `json:"queue_identity"`
	Cutoff        string `json:"cutoff"`
	LastProject   string `json:"last_project"`
}

// ReadSchedulingPolicy is a non-initializing diagnostic, not a dispatch permit.
func ReadSchedulingPolicy(dir string) (SchedulingPolicy, error) {
	s, err := openReadOnlyStore(dir)
	if err != nil {
		return SchedulingPolicy{}, err
	}
	defer s.database.Close()
	v := SchedulingPolicy{Policy: "legacy_fifo", Cutoff: "0"}
	v.QueueIdentity, err = notificationQueueIdentity(s)
	if err != nil {
		return v, err
	}
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return v, err
	}
	defer tx.Rollback()
	if err = tx.QueryRow("PRAGMA user_version").Scan(&v.Schema); err != nil {
		return v, err
	}
	if !supportedSchedulingSchema(v.Schema) {
		return v, nil
	}
	var cutoff int64
	var identity string
	err = tx.QueryRow("SELECT cutoff,last_project,queue_identity FROM scheduling_policy").Scan(&cutoff, &v.LastProject, &identity)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	if identity != v.QueueIdentity {
		return v, fmt.Errorf("scheduling store identity changed; dispatch is not authorized")
	}
	v.Policy = "project_rr_v1"
	v.Cutoff = strconv.FormatInt(cutoff, 10)
	return v, nil
}

//go:embed sql/schema7.sql
var schedulingSchema string

func (s *store) validateSchedulingIdentity() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if !supportedSchedulingSchema(version) {
		return nil
	}
	var expected string
	err := s.db.QueryRow("SELECT queue_identity FROM scheduling_policy").Scan(&expected)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	actual, err := notificationQueueIdentity(s)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("scheduling store identity changed; mutation/dispatch refused")
	}
	return nil
}

func migrateSchedulingSchema(ctx context.Context, tx *sql.Tx) error {
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version != 6 {
		return fmt.Errorf("scheduling migration requires schema 6, got %d", version)
	}
	_, err := tx.ExecContext(ctx, schedulingSchema)
	return err
}

// SchedulingEnvelope is independent of the canonical command/agent receipt.
// Dependencies refer to a future or existing exact association, never an exit.
type SchedulingEnvelope struct {
	Version       int               `json:"version"`
	QueueIdentity string            `json:"queue_identity"`
	ProjectID     string            `json:"project_id"`
	Dependencies  []ExactDependency `json:"dependencies"`
}
type ExactDependency struct {
	ProjectID      string `json:"project_id"`
	ProducerJob    string `json:"producer_job"`
	AssociationID  string `json:"association_id"`
	ManifestDigest string `json:"manifest_digest"`
	Criterion      string `json:"criterion"`
	Verification   string `json:"verification"`
}

func parseScheduling(raw []byte, project, queue string) (SchedulingEnvelope, []byte, error) {
	v := SchedulingEnvelope{Version: 1, QueueIdentity: queue, ProjectID: project, Dependencies: []ExactDependency{}}
	if len(raw) > 0 {
		v = SchedulingEnvelope{}
		if _, err := decodeStrictJSONObject(raw, 16384, &v); err != nil {
			return v, nil, err
		}
	}
	if v.Version != 1 || v.QueueIdentity != queue || v.Dependencies == nil || (v.ProjectID != "unattributed" && !isValidProjectID(v.ProjectID)) || (project != "" && project != v.ProjectID) || len(v.Dependencies) > 32 {
		return v, nil, fmt.Errorf("invalid scheduling version/project/dependency bound")
	}
	seen := map[string]bool{}
	for _, d := range v.Dependencies {
		if d.ProjectID != v.ProjectID || !isValidJobID(d.ProducerJob) || !isValidAssociationID(d.AssociationID) ||
			!isValidHexLen(d.ManifestDigest, 64) || d.Criterion != "captured_snapshot" || d.Verification != "machine_verified_snapshot" || seen[d.AssociationID] {
			return v, nil, fmt.Errorf("invalid exact captured-snapshot dependency")
		}
		seen[d.AssociationID] = true
	}
	b, err := json.Marshal(v)
	return v, b, err
}

// ActivateScheduling requires both filesystem locks and commits the immutable
// cutoff independently of dispatch. It never launches or releases old holds.
func (q *Queue) ActivateScheduling() error {
	f, err := privateFile(filepath.Join(q.store.dir, "schema.lock"), os.O_CREATE|os.O_RDWR)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	lock, err := q.store.lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := migrateStore(q.store.db, 3); err != nil {
		return err
	}
	identity, err := notificationQueueIdentity(q.store)
	if err != nil {
		return err
	}
	return q.store.write(func(c *sql.Conn) error {
		var version int
		if err := c.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&version); err != nil {
			return err
		}
		if !supportedSchedulingSchema(version) {
			return fmt.Errorf("scheduling activation requires schema 7 or 8")
		}
		_, err := c.ExecContext(context.Background(), `INSERT INTO scheduling_policy(singleton,cutoff,activated_at,queue_identity)
		 SELECT 1,COALESCE(max(sequence),0),?,? FROM receipts WHERE true ON CONFLICT(singleton) DO NOTHING`, now(), identity)
		return err
	})
}

func insertScheduling(c *sql.Conn, id string, raw []byte, project string) error {
	ctx := context.Background()
	var version int
	if err := c.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if !supportedSchedulingSchema(version) {
		if len(raw) > 0 {
			return fmt.Errorf("scheduling metadata requires schema 7")
		}
		return nil
	}
	var cutoff int64
	var queue string
	err := c.QueryRowContext(ctx, "SELECT cutoff,queue_identity FROM scheduling_policy WHERE singleton=1").Scan(&cutoff, &queue)
	if errors.Is(err, sql.ErrNoRows) {
		if len(raw) > 0 {
			return fmt.Errorf("activate scheduling before supplying metadata")
		}
		return nil
	}
	if err != nil {
		return err
	}
	v, b, err := parseScheduling(raw, project, queue)
	if err != nil {
		return err
	}
	if v.ProjectID != "unattributed" {
		var id string
		if err = c.QueryRowContext(ctx, "SELECT id FROM projects WHERE id=?", v.ProjectID).Scan(&id); err != nil {
			return fmt.Errorf("scheduling requires an existing local project: %w", err)
		}
	}
	_, err = c.ExecContext(ctx, `INSERT INTO job_scheduling(job_id,sequence,project_id,canonical)
	 SELECT job_id,sequence,?,? FROM receipts WHERE job_id=?`, v.ProjectID, b, id)
	if err != nil {
		return err
	}
	for _, d := range v.Dependencies {
		var p string
		if err = c.QueryRowContext(ctx, "SELECT project_id FROM project_jobs WHERE job_id=?", d.ProducerJob).Scan(&p); err != nil {
			return err
		}
		if p != d.ProjectID {
			return fmt.Errorf("dependency producer belongs to another project")
		}
		_, err = c.ExecContext(ctx, `INSERT INTO job_dependencies VALUES(?,?,?,?,?,?,?)`, id, d.ProducerJob, d.ProjectID, d.AssociationID, d.ManifestDigest, d.Criterion, d.Verification)
		if err != nil {
			return err
		}
	}
	return nil
}

func compareScheduling(q sqlQueryer, id string, raw []byte, project string) error {
	var version int
	if err := q.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if !supportedSchedulingSchema(version) {
		if len(raw) > 0 {
			return fmt.Errorf("scheduling metadata requires schema 7")
		}
		return nil
	}
	var b []byte
	err := q.QueryRowContext(context.Background(), "SELECT canonical FROM job_scheduling WHERE job_id=?", id).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) && len(raw) == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	var queue string
	if err = q.QueryRowContext(context.Background(), "SELECT queue_identity FROM scheduling_policy").Scan(&queue); err != nil {
		return err
	}
	_, want, err := parseScheduling(raw, project, queue)
	if err != nil {
		return err
	}
	if !bytes.Equal(b, want) {
		return fmt.Errorf("idempotency key has different immutable scheduling metadata")
	}
	return nil
}

func readJobScheduling(q sqlQueryer, id string) (*SchedulingEnvelope, error) {
	var version int
	if err := q.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	if !supportedSchedulingSchema(version) {
		return nil, nil
	}
	var raw []byte
	err := q.QueryRowContext(context.Background(), "SELECT canonical FROM job_scheduling WHERE job_id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var v SchedulingEnvelope
	if err = json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// The ordinary Main/Work project intake shares the scheduler, not an optional
// bridge-only path. Reviews retain their exact captured target as a prerequisite.
// Legacy receipts recover before adding any new scheduling requirements.
func projectScheduling(q sqlQueryer, p parsedProjectSubmission, raw []byte) ([]byte, error) {
	if len(raw) > 0 {
		var queue string
		if err := q.QueryRowContext(context.Background(), "SELECT queue_identity FROM scheduling_policy").Scan(&queue); err != nil {
			return nil, err
		}
		v, _, err := parseScheduling(raw, p.Envelope.ProjectID, queue)
		if err != nil {
			return nil, err
		}
		if p.Envelope.Role == "review" {
			found := false
			for _, d := range v.Dependencies {
				if p.Envelope.TargetJobID != nil && p.Envelope.TargetResultDigest != nil && d.ProducerJob == *p.Envelope.TargetJobID && d.ManifestDigest == *p.Envelope.TargetResultDigest {
					found = true
				}
			}
			if !found {
				return nil, fmt.Errorf("scheduled review must retain its exact captured target dependency")
			}
		}
		return raw, nil
	}
	ctx := context.Background()
	var version int
	if err := q.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	if !supportedSchedulingSchema(version) {
		return nil, nil
	}
	var id string
	err := q.QueryRowContext(ctx, "SELECT job_id FROM receipts WHERE key=?", p.Envelope.Key).Scan(&id)
	if err == nil {
		var retained []byte
		e := q.QueryRowContext(ctx, "SELECT canonical FROM job_scheduling WHERE job_id=?", id).Scan(&retained)
		if errors.Is(e, sql.ErrNoRows) {
			return nil, nil
		}
		return retained, e
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if p.Envelope.Role != "review" {
		return nil, nil
	}
	var active int
	if err = q.QueryRowContext(ctx, "SELECT count(*) FROM scheduling_policy").Scan(&active); err != nil {
		return nil, err
	}
	if active == 0 {
		return nil, nil
	}
	var assoc string
	err = q.QueryRowContext(ctx, `SELECT a.id FROM project_result_associations a JOIN project_manifests m ON m.digest=a.manifest_digest
	 WHERE a.project_id=? AND a.job_id=? AND a.manifest_digest=? AND a.kind='result'
	 AND json_extract(m.canonical,'$.source')='machine_capture'
	 ORDER BY a.sequence LIMIT 1`, p.Envelope.ProjectID, p.Envelope.TargetJobID, p.Envelope.TargetResultDigest).Scan(&assoc)
	if err != nil {
		return nil, fmt.Errorf("scheduled review requires exact published machine capture: %w", err)
	}
	v := SchedulingEnvelope{Version: 1, ProjectID: p.Envelope.ProjectID, Dependencies: []ExactDependency{{
		ProjectID: p.Envelope.ProjectID, ProducerJob: *p.Envelope.TargetJobID, AssociationID: assoc,
		ManifestDigest: *p.Envelope.TargetResultDigest, Criterion: "captured_snapshot", Verification: "machine_verified_snapshot",
	}}}
	if err = q.QueryRowContext(ctx, "SELECT queue_identity FROM scheduling_policy").Scan(&v.QueueIdentity); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// Select one FIFO runnable head per project BEFORE applying resource fit.
// The immutable sequence ring and cursor are machine-local. Legacy queued work
// forms a drain barrier; legacy unknown work is accounted only as capacity.
func (s *store) schedulingCandidate(q sqlQueryer, available int64, slots int, floor int64, commandsOnly bool) (string, error) {
	ctx := context.Background()
	var version int
	if err := q.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return "", err
	}
	if !supportedSchedulingSchema(version) {
		var id string
		err := q.QueryRowContext(ctx, `SELECT j.id FROM receipts r JOIN jobs j ON j.id=r.job_id WHERE j.state='queued' ORDER BY r.sequence LIMIT 1`).Scan(&id)
		return id, err
	}
	var cutoff int64
	var last string
	err := q.QueryRowContext(ctx, "SELECT cutoff,last_project FROM scheduling_policy WHERE singleton=1").Scan(&cutoff, &last)
	if errors.Is(err, sql.ErrNoRows) {
		var id string
		err = q.QueryRowContext(ctx, `SELECT j.id FROM receipts r JOIN jobs j ON j.id=r.job_id WHERE j.state='queued' ORDER BY r.sequence LIMIT 1`).Scan(&id)
		return id, err
	}
	if err != nil {
		return "", err
	}
	var legacy string
	err = q.QueryRowContext(ctx, `SELECT j.id FROM receipts r JOIN jobs j ON j.id=r.job_id WHERE j.state='queued' AND r.sequence<=? ORDER BY r.sequence LIMIT 1`, cutoff).Scan(&legacy)
	if err == nil {
		return legacy, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	rows, err := q.QueryContext(ctx, `SELECT r.canonical FROM jobs j JOIN receipts r ON r.job_id=j.id WHERE j.state IN ('launching','running','unknown')`)
	if err != nil {
		return "", err
	}
	count := 0
	demand := int64(0)
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			break
		}
		cmd, agent, e := storedRequest(b)
		if e != nil {
			err = e
			break
		}
		n := int64(0)
		if agent != nil {
			n = agent.MemoryBytes
		} else {
			n = cmd.MemoryBytes
		}
		count++
		if n > (1<<63-1)-demand {
			demand = 1<<63 - 1
		} else {
			demand += n
		}
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return "", err
	}
	if count >= slots || available < floor || available-floor < demand {
		return "", sql.ErrNoRows
	}
	rows, err = q.QueryContext(ctx, `WITH ready AS (
	 SELECT m.job_id,m.project_id,m.sequence FROM jobs j INDEXED BY jobs_state_id JOIN job_scheduling m ON m.job_id=j.id
	 WHERE j.state='queued' AND NOT EXISTS (
	  SELECT 1 FROM job_dependencies d WHERE d.job_id=m.job_id AND NOT EXISTS (
	   SELECT 1 FROM project_result_associations a JOIN project_manifests p ON p.digest=a.manifest_digest
	   WHERE a.id=d.association_id AND a.project_id=d.project_id AND a.job_id=d.producer_job
	    AND a.manifest_digest=d.manifest_digest AND a.kind='result'
	    AND json_extract(p.canonical,'$.source')='machine_capture'
	    AND json_extract(p.canonical,'$.claim_label')='machine_verified_snapshot'
	    AND json_extract(p.canonical,'$.claimed_validation.status')='not_run')))
	 , heads AS (SELECT project_id,min(sequence) AS sequence FROM ready GROUP BY project_id)
	 SELECT r.job_id,x.canonical FROM ready r JOIN heads h ON h.project_id=r.project_id AND h.sequence=r.sequence
	 JOIN receipts x ON x.job_id=r.job_id
	 ORDER BY (r.project_id>?) DESC,r.project_id`, last)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var b []byte
		if err = rows.Scan(&id, &b); err != nil {
			return "", err
		}
		cmd, agent, e := storedRequest(b)
		if e != nil {
			return "", e
		}
		need := int64(0)
		if agent != nil {
			if commandsOnly {
				continue
			}
			need = agent.MemoryBytes
		} else {
			need = cmd.MemoryBytes
		}
		if need <= available-floor-demand {
			return id, nil
		}
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	return "", sql.ErrNoRows
}
