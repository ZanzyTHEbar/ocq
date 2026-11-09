CREATE TABLE agent_ownership (
 job_id TEXT PRIMARY KEY REFERENCES jobs(id), session_id TEXT NOT NULL UNIQUE,
 prompt_id TEXT NOT NULL UNIQUE, token TEXT NOT NULL, phase TEXT NOT NULL DEFAULT 'queued',
 worktree TEXT NOT NULL DEFAULT '', project_id TEXT NOT NULL DEFAULT '',
 server_outcome TEXT NOT NULL DEFAULT '', terminal_evidence TEXT NOT NULL DEFAULT ''
);
CREATE TABLE agent_operations (
 job_id TEXT NOT NULL REFERENCES agent_ownership(job_id), kind TEXT NOT NULL,
 intent BLOB NOT NULL, response_path TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(job_id,kind)
);
CREATE TRIGGER agent_intent_immutable BEFORE UPDATE OF intent ON agent_operations
 BEGIN SELECT RAISE(ABORT, 'external operation intents are immutable'); END;
PRAGMA user_version=2;
