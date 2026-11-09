-- name: InsertAgentOwnership :exec
INSERT INTO agent_ownership(job_id,session_id,prompt_id,token) VALUES(?,?,?,?);

-- name: InsertAgentOperation :exec
INSERT INTO agent_operations(job_id,kind,intent) VALUES(?,?,?);

-- name: SetAgentPhase :exec
UPDATE agent_ownership SET phase=? WHERE job_id=?;

-- name: SetAgentOperationResponse :exec
UPDATE agent_operations SET response_path=? WHERE job_id=? AND kind=?;

-- name: MarkAgentUnknown :exec
UPDATE jobs SET state='unknown',outcome=?,reason=? WHERE id=? AND state IN ('launching','running','unknown','finished');

-- name: AgentPromptIntent :one
SELECT intent FROM agent_operations WHERE job_id=? AND kind='prompt';

-- name: AgentOwnershipByJob :one
SELECT session_id,prompt_id,token,phase,worktree,project_id,server_outcome,terminal_evidence
FROM agent_ownership WHERE job_id=?;

-- name: CompleteAgentOwnership :exec
UPDATE agent_ownership SET phase='terminal',server_outcome=?,terminal_evidence=? WHERE job_id=?;

-- name: CompleteAgentJob :exec
UPDATE jobs SET state='finished',outcome=?,reason=? WHERE id=?;

-- name: SetAgentServerOutcome :exec
UPDATE agent_ownership SET server_outcome=? WHERE job_id=?;

-- name: SetAgentWorktree :exec
UPDATE agent_ownership SET worktree=? WHERE job_id=?;

-- name: SetLaneWorktree :exec
UPDATE project_lanes SET fsfork_path=? WHERE owner_job_id=? AND fsfork_path='';

-- name: SetAgentProject :exec
UPDATE agent_ownership SET project_id=? WHERE job_id=?;

-- name: MarkAgentRunning :exec
UPDATE jobs SET state='running',outcome='agent_pending',reason='prompt accepted; CLI exit is not agent completion' WHERE id=?;

-- name: MarkSessionInterference :exec
UPDATE agent_ownership SET phase='session_interference' WHERE job_id=?;

-- name: MarkInterferenceUnknown :exec
UPDATE jobs SET state='unknown',outcome='uncertain',reason=? WHERE id=?;

-- name: ObservableAgentJobs :many
SELECT j.id FROM jobs j JOIN agent_ownership a ON j.id=a.job_id
WHERE j.state IN ('running','unknown') AND a.phase IN ('prompt_intent','prompt_accepted','terminal');
