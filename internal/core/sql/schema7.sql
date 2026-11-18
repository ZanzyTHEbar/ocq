-- Policy activation is explicit and separate from structural migration.
CREATE TABLE scheduling_policy (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 cutoff INTEGER NOT NULL CHECK(cutoff>=0),
 activated_at TEXT NOT NULL,
 queue_identity TEXT NOT NULL,
 last_project TEXT NOT NULL DEFAULT ''
);
CREATE TRIGGER scheduling_policy_fixed BEFORE UPDATE OF cutoff,activated_at,queue_identity ON scheduling_policy
 BEGIN SELECT RAISE(ABORT,'scheduling activation is immutable'); END;
CREATE TRIGGER scheduling_policy_retained BEFORE DELETE ON scheduling_policy
 BEGIN SELECT RAISE(ABORT,'scheduling policy is retained'); END;
CREATE TABLE job_scheduling (
 job_id TEXT PRIMARY KEY REFERENCES jobs(id),
 sequence INTEGER NOT NULL UNIQUE,
 project_id TEXT NOT NULL,
 canonical BLOB NOT NULL
);
CREATE INDEX scheduling_project_sequence ON job_scheduling(project_id,sequence);
CREATE INDEX jobs_state_id ON jobs(state,id);
CREATE TABLE job_dependencies (
 job_id TEXT NOT NULL REFERENCES job_scheduling(job_id),
 producer_job TEXT NOT NULL REFERENCES project_jobs(job_id),
 project_id TEXT NOT NULL REFERENCES projects(id),
 association_id TEXT NOT NULL,
 manifest_digest TEXT NOT NULL,
 criterion TEXT NOT NULL CHECK(criterion='captured_snapshot'),
 verification TEXT NOT NULL CHECK(verification='machine_verified_snapshot'),
 PRIMARY KEY(job_id,association_id)
);
CREATE TRIGGER job_scheduling_fixed BEFORE UPDATE ON job_scheduling
 BEGIN SELECT RAISE(ABORT,'scheduling metadata is immutable'); END;
CREATE TRIGGER job_scheduling_retained BEFORE DELETE ON job_scheduling
 BEGIN SELECT RAISE(ABORT,'scheduling metadata is retained'); END;
CREATE TRIGGER job_dependencies_fixed BEFORE UPDATE ON job_dependencies
 BEGIN SELECT RAISE(ABORT,'dependencies are immutable'); END;
CREATE TRIGGER job_dependencies_retained BEFORE DELETE ON job_dependencies
 BEGIN SELECT RAISE(ABORT,'dependencies are retained'); END;
CREATE INDEX projects_observation_key ON projects(created_at,id);
CREATE INDEX lanes_observation_key ON project_lanes(project_id,created_at,lane_id);
PRAGMA user_version=7;
