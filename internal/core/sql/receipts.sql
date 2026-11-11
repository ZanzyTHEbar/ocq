-- name: ReceiptByKey :one
SELECT job_id, canonical FROM receipts WHERE key = ?;

-- name: PreparedAgentReceiptByKey :one
SELECT r.job_id, r.canonical, r.digest, r.sequence,
 j.id IS NOT NULL AS job_present, a.job_id IS NOT NULL AS agent_present
 FROM receipts r LEFT JOIN jobs j ON j.id=r.job_id
 LEFT JOIN agent_ownership a ON a.job_id=r.job_id WHERE r.key=?;

-- name: InsertReceipt :exec
INSERT INTO receipts(key, job_id, canonical, digest, accepted_at) VALUES (?, ?, ?, ?, ?);

-- name: InsertQueuedJob :exec
INSERT INTO jobs(id, state) VALUES (?, 'queued');

-- name: QueuedHead :one
SELECT j.id FROM jobs j JOIN receipts r ON j.id = r.job_id
WHERE j.state = 'queued' ORDER BY r.sequence LIMIT 1;

-- name: CancelQueuedJob :execrows
UPDATE jobs SET state = 'cancelled', outcome = 'cancelled_before_launch',
 reason = 'operator cancelled queued job' WHERE id = ? AND state = 'queued';

-- name: ResolveUnknownJob :exec
UPDATE jobs SET state = 'resolved', outcome = 'operator_reconciled', evidence = ?,
 reason = 'ownership released by operator; not validated' WHERE id = ? AND state = 'unknown';

-- name: JobByID :one
SELECT j.id,r.sequence,r.key,r.digest,r.canonical,j.state,j.reason,
 j.pid,j.start_ticks,j.boot,j.stdout,j.stderr,j.exit_code,j.outcome,j.validation,j.evidence,r.accepted_at
 FROM jobs j JOIN receipts r ON j.id=r.job_id WHERE j.id=?;

-- name: ListJobs :many
SELECT j.id,r.sequence,r.key,r.digest,r.canonical,j.state,j.reason,
 j.pid,j.start_ticks,j.boot,j.stdout,j.stderr,j.exit_code,j.outcome,j.validation,j.evidence,r.accepted_at
 FROM jobs j JOIN receipts r ON j.id=r.job_id ORDER BY r.sequence;

-- name: QueuedHeadJob :one
SELECT j.id,r.sequence,r.key,r.digest,r.canonical,j.state,j.reason,
 j.pid,j.start_ticks,j.boot,j.stdout,j.stderr,j.exit_code,j.outcome,j.validation,j.evidence,r.accepted_at
 FROM jobs j JOIN receipts r ON j.id=r.job_id WHERE j.state='queued' ORDER BY r.sequence LIMIT 1;

-- name: QueueJobsPage :many
SELECT j.id, r.sequence, CASE WHEN a.job_id IS NULL THEN 'command' ELSE 'agent' END AS kind,
 j.state, j.reason, j.outcome, j.exit_code, j.validation, r.accepted_at,
 coalesce(a.session_id,'') AS session_id, coalesce(a.worktree,'') AS worktree,
 j.stdout <> '' AS has_stdout, j.stderr <> '' AS has_stderr, coalesce(n.id,'') AS completion_event_id
 FROM jobs j JOIN receipts r ON j.id=r.job_id LEFT JOIN agent_ownership a ON a.job_id=j.id
 LEFT JOIN completion_notifications n ON n.job_id=j.id
 WHERE r.sequence > ? ORDER BY r.sequence LIMIT ?;

-- name: QueueJobByID :one
SELECT j.id, r.sequence, CASE WHEN a.job_id IS NULL THEN 'command' ELSE 'agent' END AS kind,
 j.state, j.reason, j.outcome, j.exit_code, j.validation, r.accepted_at,
 coalesce(a.session_id,'') AS session_id, coalesce(a.worktree,'') AS worktree,
 j.stdout <> '' AS has_stdout, j.stderr <> '' AS has_stderr, coalesce(n.id,'') AS completion_event_id
 FROM jobs j JOIN receipts r ON j.id=r.job_id LEFT JOIN agent_ownership a ON a.job_id=j.id
 LEFT JOIN completion_notifications n ON n.job_id=j.id
 WHERE j.id = ?;

-- name: RetainedJobLog :one
SELECT stdout, stderr FROM jobs WHERE id = ?;
