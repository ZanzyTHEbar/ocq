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

-- name: RecordLaunchIntent :exec
UPDATE jobs SET state='launching',reason='launch intent committed; spawn may be uncertain',
 boot=?,stdout=?,stderr=? WHERE id=?;

-- name: MarkLaunchFailure :exec
UPDATE jobs SET state='finished',outcome='launch_failed',reason=? WHERE id=?;

-- name: RecordProcessIdentity :exec
UPDATE jobs SET state=?,reason=?,pid=?,start_ticks=?,boot=? WHERE id=?;

-- name: FinishRunningJob :exec
UPDATE jobs SET state=?,outcome=?,reason=?,exit_code=? WHERE id=? AND state='running';
