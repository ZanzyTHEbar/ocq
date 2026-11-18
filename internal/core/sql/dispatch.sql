-- name: RecoverInterruptedJobs :exec
UPDATE jobs SET state='unknown',outcome='uncertain',
 reason='dispatcher restarted; ownership/capacity retained pending operator reconciliation'
 WHERE state IN ('launching','running');

-- name: SetQueuedReason :exec
UPDATE jobs SET reason=? WHERE id=? AND state='queued' AND reason<>?;

-- name: InflightRequests :many
SELECT r.canonical FROM jobs j JOIN receipts r ON j.id=r.job_id
 WHERE j.state IN ('launching','running','unknown');

-- name: SetJobReason :exec
UPDATE jobs SET reason=? WHERE id=?;
