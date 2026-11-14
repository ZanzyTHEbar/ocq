package core

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"unicode/utf8"

	"github.com/ZanzyTHEbar/faults-go"

	"ocq/internal/core/dbquery"
)

// Queue owns one connection to the durable command and agent queue.
type Queue struct{ store *store }

// OpenQueue initializes or opens the private local store, including migrations.
func OpenQueue(stateDir string) (_ *Queue, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeRuntimeFailed, resultErr) }()
	s, err := openStore(stateDir)
	if err != nil {
		return nil, err
	}
	return &Queue{store: s}, nil
}

func (q *Queue) Close() error {
	return classifyDiagnostic(faults.CodeDatabaseWrite, q.store.database.Close())
}

// SubmitCommand durably records a version-1 receipt before returning.
func (q *Queue) SubmitCommand(key, cwd string, argv []string, memoryBytes int64) (_ Job, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeRuntimeFailed, resultErr) }()
	if key == "" || len(key) > 1024 || !utf8.ValidString(key) || len(argv) == 0 || memoryBytes < 0 {
		return Job{}, faults.New(faults.CodeValidationFailed, "submit requires --key KEY [--cwd DIR] [--memory-bytes N>=0] -- COMMAND [ARG...]")
	}
	request, err := newCommandRequest(argv, cwd, memoryBytes)
	if err != nil {
		return Job{}, err
	}
	return q.store.submit(key, request)
}

func (q *Queue) Job(id string) (Job, error) {
	job, err := q.store.get(id)
	return job, classifyDiagnostic(faults.CodeDatabaseRead, err)
}
func (q *Queue) Jobs() ([]Job, error) {
	jobs, err := q.store.list()
	return jobs, classifyDiagnostic(faults.CodeDatabaseRead, err)
}

// Cancel changes only queued work and returns its stored state.
func (q *Queue) Cancel(id string) (_ Job, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeDatabaseWrite, resultErr) }()
	if err := q.store.cancel(id); err != nil {
		return Job{}, err
	}
	return q.store.get(id)
}

func (q *Queue) Notifications(limit int) ([]Notification, error) {
	return q.store.notifications(limit)
}

func (q *Queue) Acknowledge(id string) (Notification, error) {
	if id == "" {
		return Notification{}, faults.New(faults.CodeValidationFailed, "ack requires one exact notification event ID")
	}
	return q.store.acknowledge(id)
}

// Reconcile makes API-only observations under the dispatcher lock; it never
// resumes, prompts, approves, or releases uncertain ownership by process status.
func (q *Queue) Reconcile(id string) (_ Job, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeRuntimeFailed, resultErr) }()
	return q.store.reconcileChecked(id, nil, nil)
}

// The optional bridge hooks fence an exact stored observation and retain its
// operation receipt while the same dispatcher lock still excludes observers.
// Neither hook is an alternative completion policy or an execution control.
func (s *store) reconcileChecked(id string, check func(Job) error, observed func(error) error) (Job, error) {
	lock, err := s.lock()
	if err != nil {
		return Job{}, err
	}
	defer lock.Close()
	job, err := s.get(id)
	if err != nil {
		return Job{}, err
	}
	if job.agentRequest == nil {
		return Job{}, faults.New(faults.CodeValidationFailed, "reconcile is API-only and restricted to agent jobs")
	}
	if check != nil {
		if err := check(job); err != nil {
			return Job{}, err
		}
	}
	observeErr := s.observeAgent(job)
	if observed != nil {
		if err := observed(observeErr); err != nil {
			return Job{}, errors.Join(observeErr, err)
		}
	}
	if observeErr != nil {
		return Job{}, observeErr
	}
	return s.get(job.ID)
}

// OpenLog opens the exact private retained output for a job.
func (q *Queue) OpenLog(id string, stderr bool) (_ io.ReadCloser, resultErr error) {
	return q.store.openLog(id, stderr)
}

func (s *store) openLog(id string, stderr bool) (_ *os.File, resultErr error) {
	return openQueueLog(context.Background(), s.db, id, stderr)
}

func openQueueLog(ctx context.Context, q sqlQueryer, id string, stderr bool) (_ *os.File, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeStorageFailed, resultErr) }()
	job, err := dbquery.New(q).RetainedJobLog(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, faults.New(faults.CodeNotFound, "exact queue job not found")
	}
	if err != nil {
		return nil, err
	}
	path := job.Stdout
	if stderr {
		path = job.Stderr
	}
	if path == "" {
		return nil, faults.New(faults.CodeNotFound, "output not created: job has not reached launch intent")
	}
	file, err := privateFile(path, os.O_RDONLY)
	if err != nil {
		return nil, faults.Wrap(faults.CodeStorageFailed, "output unavailable (not success)", err)
	}
	return file, nil
}

// Dispatch keeps the singleton dispatcher lock until cancellation and drains
// owned foreground commands before returning. onReady is called after recovery.
func (q *Queue) Dispatch(ctx context.Context, slots int, floor int64, commandsOnly bool, onReady func()) error {
	if slots < 1 || floor < 0 {
		return faults.New(faults.CodeValidationFailed, "dispatch requires slots>=1 and memory-floor-bytes>=0")
	}
	if ctx == nil {
		return faults.New(faults.CodeValidationFailed, "dispatch requires a context")
	}
	return classifyDiagnostic(faults.CodeRuntimeFailed, q.store.dispatch(ctx, slots, floor, commandsOnly, onReady))
}

// Resolve records a human's evidence for an eligible unknown command job.
func (q *Queue) Resolve(id, evidence string) (_ Job, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeRuntimeFailed, resultErr) }()
	if id == "" || evidence == "" {
		return Job{}, faults.New(faults.CodeValidationFailed, "resolve --evidence TEXT JOB_ID (dispatcher must be stopped)")
	}
	return q.store.resolve(id, evidence)
}
