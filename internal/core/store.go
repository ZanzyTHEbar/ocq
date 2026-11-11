package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"

	dbbootstrap "github.com/ZanzyTHEbar/dbbootstrap"
	"github.com/ZanzyTHEbar/faults-go"
	"github.com/pressly/goose/v3"

	"ocq/internal/core/dbquery"
)

type store struct {
	db                 *sql.DB
	database           *dbbootstrap.DB
	dir                string
	existingSchemaOnly bool
}

type Job struct {
	ID             string          `json:"id"`
	Sequence       int64           `json:"sequence"`
	Key            string          `json:"key"`
	Digest         string          `json:"digest"`
	Request        json.RawMessage `json:"request"`
	State          string          `json:"state"`
	Reason         string          `json:"reason"`
	Process        Process         `json:"process"`
	Stdout         string          `json:"stdout"`
	Stderr         string          `json:"stderr"`
	ExitCode       *int            `json:"exit_code"`
	Outcome        string          `json:"outcome"`
	Validation     string          `json:"validation"`
	Evidence       string          `json:"resolution_evidence"`
	AcceptedAt     string          `json:"accepted_at"`
	OutputStatus   string          `json:"output_status"`
	Agent          *AgentOwnership `json:"agent,omitempty"`
	commandRequest *commandRequest
	agentRequest   *agentRequest
}

//go:embed sql/schema.sql
var schema string

// These are also sqlc's schema inputs, so migration and query generation use
// the same immutable historical SQL rather than a separately maintained copy.
//
//go:embed sql/schema2.sql
var agentSchema string

//go:embed sql/schema3.sql
var notificationSchema string

//go:embed sql/schema4.sql
var projectSchema string

//go:embed sql/schema5.sql
var projectLaneSchema string

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func resolveStateDir(dir string) (string, error) {
	if dir == "" {
		base := os.Getenv("XDG_STATE_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", classifyDiagnostic(faults.CodeConfigInvalid, err)
			}
			base = filepath.Join(home, ".local", "state")
		}
		if !filepath.IsAbs(base) {
			return "", faults.New(faults.CodeConfigInvalid, "XDG_STATE_HOME must be absolute")
		}
		dir = filepath.Join(base, "ocq")
	}
	path, err := filepath.Abs(dir)
	return path, classifyDiagnostic(faults.CodeConfigInvalid, err)
}

// Storage support depends on actual permissions, locking, WAL shared memory and
// sync operations below, not a filesystem-name allowlist. Successful operations
// cannot prove backing-store persistence or correct cross-host coherence; the
// operator must satisfy docs/storage.md's single-host and retention contract.
func openStore(dir string) (*store, error) {
	dir, err := resolveStateDir(dir)
	if err != nil {
		return nil, err
	}
	if err := privateDir(dir); err != nil {
		return nil, err
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	// SQLite's initial journal-mode transition can fail BUSY without waiting when
	// first-time opens race. Serialize only connection/schema initialization, not
	// submission; the receipt itself is protected by its SQLite transaction.
	initLock, err := privateFile(filepath.Join(dir, "schema.lock"), os.O_CREATE|os.O_RDWR)
	if err != nil {
		return nil, err
	}
	defer initLock.Close()
	if err := syscall.Flock(int(initLock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, faults.Wrap(faults.CodeStorageFailed, "acquire schema.lock: storage must support advisory flock locking", err)
	}
	path := filepath.Join(dir, "queue.db")
	file, err := privateFile(path, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return nil, err
	}
	err = file.Sync()
	file.Close()
	if err != nil {
		return nil, classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	if err := syncDir(dir); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	query := url.Values{}
	// Goose and database/sql begin write transactions through BeginTx. Keep the
	// lock mode on the DSN so it also applies to replacement connections.
	query.Set("_txlock", "immediate")
	// modernc applies these on EVERY connection, including a replacement connection.
	for _, pragma := range []string{"busy_timeout(5000)", "journal_mode(WAL)", "synchronous(FULL)", "foreign_keys(1)"} {
		query.Add("_pragma", pragma)
	}
	u.RawQuery = query.Encode()
	database, err := dbbootstrap.Open(context.Background(), dbbootstrap.Config{
		DSN: u.String(), MaxOpenConns: 1, MaxIdleConns: 1,
	}, dbbootstrap.SQLite())
	if err != nil {
		return nil, classifyDiagnostic(faults.CodeDatabaseWrite, err)
	}
	db := database.SQL()
	s := &store{db: db, database: database, dir: dir}
	var mode string
	var synchronous, foreignKeys, busy int
	err = db.QueryRow("PRAGMA journal_mode").Scan(&mode)
	if err == nil {
		err = db.QueryRow("PRAGMA synchronous").Scan(&synchronous)
	}
	if err == nil {
		err = db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys)
	}
	if err == nil {
		err = db.QueryRow("PRAGMA busy_timeout").Scan(&busy)
	}
	if err != nil || mode != "wal" || synchronous != 2 || foreignKeys != 1 || busy != 5000 {
		database.Close()
		message := fmt.Sprintf("SQLite safety pragmas unavailable: mode=%s sync=%d foreign_keys=%d busy=%d", mode, synchronous, foreignKeys, busy)
		if err != nil {
			return nil, faults.Wrap(faults.CodeConfigInvalid, message, err)
		}
		return nil, faults.New(faults.CodeConfigInvalid, message)
	}
	var migrationLock *os.File
	defer func() {
		if migrationLock != nil {
			migrationLock.Close()
		}
	}()
	var version int
	// Connection and schema-control PRAGMAs are runtime SQLite configuration,
	// not application queries; they intentionally stay outside sqlc.
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		database.Close()
		return nil, classifyDiagnostic(faults.CodeDatabaseRead, err)
	}
	if version < 0 || version > 5 {
		database.Close()
		return nil, faults.New(faults.CodeConfigInvalid, fmt.Sprintf("unsupported schema version %d", version))
	}
	if version < 5 {
		migrationLock, err = s.lock()
		if err != nil {
			database.Close()
			return nil, faults.Wrap(faults.CodeResourceLimit, "stop dispatcher before schema migration", err)
		}
	}
	if err := migrateStore(db); err != nil {
		database.Close()
		return nil, err
	}
	return s, nil
}

// migrateStore uses a single Goose migration for all historical steps: Goose
// commits the callback and its applied-version record in the same transaction.
// Goose may first commit an empty version table separately; this does not change
// user_version or queue data and a subsequent open retries the migration.
func migrateStore(db *sql.DB) error {
	migration := goose.NewGoMigration(1, &goose.GoFunc{RunTx: migrateSchema}, &goose.GoFunc{
		RunTx: func(context.Context, *sql.Tx) error {
			return faults.New(faults.CodeConfigInvalid, "schema downgrade is not supported")
		},
	})
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, nil,
		goose.WithDisableGlobalRegistry(true), goose.WithGoMigrations(migration))
	if err != nil {
		return classifyDiagnostic(faults.CodeConfigInvalid, err)
	}
	if _, err := provider.Up(context.Background()); err != nil {
		return classifyDiagnostic(faults.CodeDatabaseWrite, err)
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return classifyDiagnostic(faults.CodeDatabaseRead, err)
	}
	if version != 5 {
		return faults.New(faults.CodeInvariantFailed, fmt.Sprintf("inconsistent Goose migration and schema version %d", version))
	}
	return nil
}

func migrateSchema(ctx context.Context, tx *sql.Tx) error {
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return classifyDiagnostic(faults.CodeDatabaseRead, err)
	}
	if version < 0 || version > 5 {
		return faults.New(faults.CodeConfigInvalid, fmt.Sprintf("unsupported schema version %d", version))
	}
	// Execute the embedded historical DDL/backfill as Goose's one atomic Go
	// migration, not as application queries or separately committed steps.
	steps := []string{schema, agentSchema, notificationSchema, projectSchema, projectLaneSchema}
	for version < len(steps) {
		if _, err := tx.ExecContext(ctx, steps[version]); err != nil {
			return faults.Wrap(faults.CodeDatabaseWrite, fmt.Sprintf("schema %d to %d", version, version+1), err)
		}
		version++
	}
	return nil
}

// Non-initializing read-only store open for projection reads. Fails closed if
// the directory or database file does not exist or is not at schema version 5.
func openReadOnlyStore(dir string) (*store, error) {
	dir, err := resolveStateDir(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() || !info.IsDir() || info.Mode() != os.ModeDir|0700 {
		return nil, faults.New(faults.CodeConfigInvalid, fmt.Sprintf("state/artifact directory must be owned, nonsymlink, mode 0700: %s", dir))
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	path := filepath.Join(dir, "queue.db")
	file, err := privateFile(path, os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	file.Close()
	u := url.URL{Scheme: "file", Path: path}
	query := url.Values{}
	query.Set("mode", "ro")
	for _, pragma := range []string{"busy_timeout(5000)", "foreign_keys(1)", "query_only(1)"} {
		query.Add("_pragma", pragma)
	}
	u.RawQuery = query.Encode()
	database, err := dbbootstrap.Open(context.Background(), dbbootstrap.Config{
		DSN: u.String(), MaxOpenConns: 1, MaxIdleConns: 1,
	}, dbbootstrap.SQLite())
	if err != nil {
		return nil, classifyDiagnostic(faults.CodeDatabaseRead, err)
	}
	db := database.SQL()
	var mode string
	var foreignKeys, queryOnly, busy, version int
	err = db.QueryRow("PRAGMA journal_mode").Scan(&mode)
	if err == nil {
		err = db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys)
	}
	if err == nil {
		err = db.QueryRow("PRAGMA query_only").Scan(&queryOnly)
	}
	if err == nil {
		err = db.QueryRow("PRAGMA busy_timeout").Scan(&busy)
	}
	if err == nil {
		err = db.QueryRow("PRAGMA user_version").Scan(&version)
	}
	if err != nil || mode != "wal" || foreignKeys != 1 || queryOnly != 1 || busy != 5000 {
		database.Close()
		message := fmt.Sprintf("read-only SQLite safety pragmas unavailable: mode=%s foreign_keys=%d query_only=%d busy=%d", mode, foreignKeys, queryOnly, busy)
		if err != nil {
			return nil, faults.Wrap(faults.CodeConfigInvalid, message, err)
		}
		return nil, faults.New(faults.CodeConfigInvalid, message)
	}
	if version != 5 {
		database.Close()
		return nil, faults.New(faults.CodeConfigInvalid, fmt.Sprintf("read-only store requires schema version 5, got %d", version))
	}
	return &store{db: db, database: database, dir: dir}, nil
}

// openExistingWritableStore opens only an existing schema-5 database. Unlike
// openStore it creates neither state nor locks, changes no journal mode, and
// never invokes Goose. The existing initialization lock excludes migrations
// while the connection and its safety settings are inspected.
func openExistingWritableStore(dir string) (*store, error) {
	dir, err := resolveStateDir(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() || !info.IsDir() || info.Mode() != os.ModeDir|0700 {
		return nil, faults.New(faults.CodeConfigInvalid, fmt.Sprintf("state/artifact directory must be owned, nonsymlink, mode 0700: %s", dir))
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	initLock, err := privateFile(filepath.Join(dir, "schema.lock"), os.O_RDWR)
	if err != nil {
		return nil, err
	}
	defer initLock.Close()
	if err := syscall.Flock(int(initLock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, faults.Wrap(faults.CodeStorageFailed, "acquire schema.lock: storage must support advisory flock locking", err)
	}
	path := filepath.Join(dir, "queue.db")
	file, err := privateFile(path, os.O_RDWR)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	original, err := file.Stat()
	if err != nil {
		return nil, classifyDiagnostic(faults.CodeStorageFailed, err)
	}
	u := url.URL{Scheme: "file", Path: path}
	query := url.Values{}
	query.Set("mode", "rw")
	query.Set("_txlock", "immediate")
	for _, pragma := range []string{"busy_timeout(5000)", "synchronous(FULL)", "foreign_keys(1)"} {
		query.Add("_pragma", pragma)
	}
	u.RawQuery = query.Encode()
	database, err := dbbootstrap.Open(context.Background(), dbbootstrap.Config{
		DSN: u.String(), MaxOpenConns: 1, MaxIdleConns: 1,
	}, dbbootstrap.SQLite())
	if err != nil {
		return nil, classifyDiagnostic(faults.CodeDatabaseWrite, err)
	}
	db := database.SQL()
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(original, current) {
		database.Close()
		return nil, faults.New(faults.CodeStorageFailed, "queue database changed during open")
	}
	var mode string
	var synchronous, foreignKeys, busy, version int
	err = db.QueryRow("PRAGMA journal_mode").Scan(&mode)
	if err == nil {
		err = db.QueryRow("PRAGMA synchronous").Scan(&synchronous)
	}
	if err == nil {
		err = db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys)
	}
	if err == nil {
		err = db.QueryRow("PRAGMA busy_timeout").Scan(&busy)
	}
	if err == nil {
		err = db.QueryRow("PRAGMA user_version").Scan(&version)
	}
	if err != nil || mode != "wal" || synchronous != 2 || foreignKeys != 1 || busy != 5000 {
		database.Close()
		message := fmt.Sprintf("existing writable SQLite safety pragmas unavailable: mode=%s sync=%d foreign_keys=%d busy=%d", mode, synchronous, foreignKeys, busy)
		if err != nil {
			return nil, faults.Wrap(faults.CodeConfigInvalid, message, err)
		}
		return nil, faults.New(faults.CodeConfigInvalid, message)
	}
	if version != 5 {
		database.Close()
		return nil, faults.New(faults.CodeConfigInvalid, fmt.Sprintf("existing writable store requires schema version 5, got %d", version))
	}
	return &store{db: db, database: database, dir: dir}, nil
}

// Short IMMEDIATE transactions serialize receipt creation and state transitions.
// Transaction-control statements stay raw so all generated queries use this
// exact connection. Never perform spawn, wait or filesystem setup inside it.
func (s *store) write(fn func(*sql.Conn) error) error {
	c, err := s.db.Conn(context.Background())
	if err != nil {
		return classifyDiagnostic(faults.CodeDatabaseWrite, err)
	}
	defer c.Close()
	if _, err := c.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		return classifyDiagnostic(faults.CodeDatabaseWrite, err)
	}
	defer c.ExecContext(context.Background(), "ROLLBACK") // harmless after COMMIT
	if s.existingSchemaOnly {
		if err := requireProjectSchema5(c); err != nil {
			return err
		}
	}
	if err := fn(c); err != nil {
		return err
	}
	_, err = c.ExecContext(context.Background(), "COMMIT")
	return classifyDiagnostic(faults.CodeDatabaseWrite, err)
}

func (s *store) submit(key string, request commandRequest) (Job, error) {
	data, _, err := encodeCommandRequest(request)
	if err != nil {
		return Job{}, classifyDiagnostic(faults.CodeInvariantFailed, err)
	}
	return s.acceptCanonical(key, data, false)
}

func (s *store) acceptCanonical(key string, data []byte, agent bool) (Job, error) {
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	id := ""
	err := s.write(func(c *sql.Conn) error {
		queries := dbquery.New(c)
		previous, err := queries.ReceiptByKey(context.Background(), key)
		if err == nil {
			id = previous.JobID
			if !bytes.Equal(data, previous.Canonical) {
				return faults.New(faults.CodeValidationFailed, "idempotency key already belongs to a different canonical request")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return classifyDiagnostic(faults.CodeDatabaseRead, err)
		}
		id, err = insertCanonicalReceipt(c, key, data, digest, agent)
		return err
	})
	if err != nil {
		return Job{}, classifyDiagnostic(faults.CodeDatabaseWrite, err)
	}
	return s.get(id)
}

// Database-only admission work shared by the existing CLI/command path and
// prepared agent admission. No validation callbacks, subprocesses or filesystem
// setup may run while the write transaction is held.
func insertCanonicalReceipt(c *sql.Conn, key string, data []byte, digest string, agent bool) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", classifyDiagnostic(faults.CodeRuntimeFailed, err)
	}
	id := "job-" + hex.EncodeToString(random[:])
	queries := dbquery.New(c)
	if err := queries.InsertReceipt(context.Background(), dbquery.InsertReceiptParams{Key: key, JobID: id, Canonical: data, Digest: digest, AcceptedAt: now()}); err != nil {
		return "", err
	}
	if err := queries.InsertQueuedJob(context.Background(), id); err != nil {
		return "", err
	}
	if agent {
		if err := insertAgentOwnership(c, id); err != nil {
			return "", err
		}
	}
	return id, nil
}

func jobFromRow(row dbquery.JobByIDRow) (Job, error) {
	if row.StartTicks < 0 {
		return Job{}, faults.New(faults.CodeInvariantFailed, "negative process start ticks")
	}
	job := Job{ID: row.ID, Sequence: row.Sequence, Key: row.Key, Digest: row.Digest,
		State: row.State, Reason: row.Reason, Process: Process{PID: int(row.Pid), Start: uint64(row.StartTicks), Boot: row.Boot},
		Stdout: row.Stdout, Stderr: row.Stderr, Outcome: row.Outcome, Validation: row.Validation,
		Evidence: row.Evidence, AcceptedAt: row.AcceptedAt}
	job.Request = append(json.RawMessage(nil), row.Canonical...)
	var err error
	job.commandRequest, job.agentRequest, err = storedRequest(row.Canonical)
	if err != nil {
		return job, err
	}
	if row.ExitCode.Valid {
		code := int(row.ExitCode.Int64)
		job.ExitCode = &code
	}
	job.OutputStatus = "not_created"
	if job.Stdout != "" && job.Stderr != "" {
		job.OutputStatus = "available"
		for _, path := range []string{job.Stdout, job.Stderr} {
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				job.OutputStatus = "unavailable: " + path
				break
			}
		}
	}
	return job, nil
}

func (s *store) get(id string) (_ Job, resultErr error) {
	return s.getFrom(s.db, id)
}

func (s *store) getFrom(q sqlQueryer, id string) (_ Job, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeDatabaseRead, resultErr) }()
	row, err := dbquery.New(q).JobByID(context.Background(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Job{}, classifyDiagnostic(faults.CodeNotFound, err)
		}
		return Job{}, classifyDiagnostic(faults.CodeDatabaseRead, err)
	}
	job, err := jobFromRow(row)
	if err == nil && job.State == "queued" {
		var head string
		head, err = dbquery.New(q).QueuedHead(context.Background())
		if err == nil && head != id {
			job.Reason = "FIFO behind " + head
		} else if errors.Is(err, sql.ErrNoRows) {
			err = nil // concurrent cancellation/admission; snapshot remains honest
		} else if err != nil {
			err = classifyDiagnostic(faults.CodeDatabaseRead, err)
		}
	}
	if err == nil && job.agentRequest != nil {
		job.Agent, err = agentOwnershipFrom(q, id)
	}
	return job, err
}

func (s *store) list() (_ []Job, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeDatabaseRead, resultErr) }()
	rows, err := dbquery.New(s.db).ListJobs(context.Background())
	if err != nil {
		return nil, classifyDiagnostic(faults.CodeDatabaseRead, err)
	}
	jobs := []Job{}
	for _, row := range rows {
		job, err := jobFromRow(dbquery.JobByIDRow(row))
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	for i := range jobs {
		if jobs[i].agentRequest != nil {
			jobs[i].Agent, err = s.agentOwnership(jobs[i].ID)
			if err != nil {
				return nil, err
			}
		}
	}
	return jobs, nil
}

func (s *store) cancel(id string) error {
	return s.cancelChecked(id, nil)
}

// The bridge checks the retained receipt in the same IMMEDIATE transaction as
// the CLI's queued-only transition and terminal-notification trigger.
func (s *store) cancelChecked(id string, check func(Job) error) error {
	return s.write(func(c *sql.Conn) error {
		if check != nil {
			job, err := s.getFrom(c, id)
			if err != nil {
				return err
			}
			if err := check(job); err != nil {
				return err
			}
		}
		n, err := dbquery.New(c).CancelQueuedJob(context.Background(), id)
		if err != nil {
			return classifyDiagnostic(faults.CodeDatabaseWrite, err)
		}
		if n != 1 {
			return faults.New(faults.CodeValidationFailed, "only an existing queued job can be cancelled; receipt is retained")
		}
		return nil
	})
}

func (s *store) resolve(id, evidence string) (Job, error) {
	return s.resolveChecked(id, evidence, nil)
}

// Bridge scope checks share the CLI's dispatcher exclusion and real process
// checks; they run again on the exact write connection before the transition.
func (s *store) resolveChecked(id, evidence string, check func(Job) error) (Job, error) {
	var empty Job
	lock, err := s.lock()
	if err != nil {
		return empty, &commandResolutionRefusal{"dispatcher_lock_unavailable", err}
	}
	defer lock.Close()
	job, err := s.get(id)
	if err != nil {
		return empty, err
	}
	if job.State != "unknown" {
		return empty, faults.New(faults.CodeValidationFailed, "only an unknown job may be resolved; no rerun is performed")
	}
	if job.agentRequest != nil {
		return empty, faults.New(faults.CodeValidationFailed, "resolve cannot prove an agent stopped from process/boot identity; use API-only reconcile")
	}
	if err := safeToResolve(job.Process); err != nil {
		return empty, &commandResolutionRefusal{"process_exit_not_established", err}
	}
	if err := s.write(func(c *sql.Conn) error {
		current, err := s.getFrom(c, id)
		if err != nil {
			return err
		}
		if current.State != "unknown" || current.Process != job.Process {
			return faults.New(faults.CodeValidationFailed, "unknown command ownership changed; resolution refused")
		}
		if check != nil {
			if err := check(current); err != nil {
				return err
			}
		}
		return classifyDiagnostic(faults.CodeDatabaseWrite, dbquery.New(c).ResolveUnknownJob(context.Background(), dbquery.ResolveUnknownJobParams{Evidence: evidence, ID: id}))
	}); err != nil {
		return empty, err
	}
	return s.get(id)
}
