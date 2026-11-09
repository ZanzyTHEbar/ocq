-- name: ReceiptByKey :one
SELECT job_id, canonical FROM receipts WHERE key = ?;

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
