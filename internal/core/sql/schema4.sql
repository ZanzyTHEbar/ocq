CREATE TABLE projects (
 id TEXT PRIMARY KEY,
 label TEXT NOT NULL,
 created_at TEXT NOT NULL
);
CREATE TRIGGER projects_no_update BEFORE UPDATE ON projects
 BEGIN SELECT RAISE(ABORT, 'projects are immutable'); END;
CREATE TRIGGER projects_no_delete BEFORE DELETE ON projects
 BEGIN SELECT RAISE(ABORT, 'projects are retained indefinitely'); END;
CREATE TABLE project_manifests (
 digest TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id),
 version INTEGER NOT NULL CHECK(version = 1),
 kind TEXT NOT NULL CHECK(kind IN ('handoff','brief','result','review_brief','review_result')),
 canonical BLOB NOT NULL,
 recorded_at TEXT NOT NULL
);
CREATE TRIGGER project_manifests_no_update BEFORE UPDATE ON project_manifests
 BEGIN SELECT RAISE(ABORT, 'project manifests are immutable'); END;
CREATE TRIGGER project_manifests_no_delete BEFORE DELETE ON project_manifests
 BEGIN SELECT RAISE(ABORT, 'project manifests are retained indefinitely'); END;
CREATE TABLE project_bindings (
 project_id TEXT NOT NULL REFERENCES projects(id),
 revision INTEGER NOT NULL CHECK(revision >= 1),
 repo_root TEXT NOT NULL,
 git_common_dir TEXT NOT NULL,
 commit_anchor TEXT NOT NULL,
 elephant_session_id TEXT NOT NULL,
 opencode_project_id TEXT NOT NULL,
 opencode_directory TEXT NOT NULL,
 handoff_manifest_digest TEXT NOT NULL REFERENCES project_manifests(digest),
 auth_ref TEXT NOT NULL,
 verified_at TEXT NOT NULL,
 recorded_at TEXT NOT NULL,
 PRIMARY KEY(project_id, revision)
);
CREATE TRIGGER project_bindings_no_update BEFORE UPDATE ON project_bindings
 BEGIN SELECT RAISE(ABORT, 'project bindings are immutable'); END;
CREATE TRIGGER project_bindings_no_delete BEFORE DELETE ON project_bindings
 BEGIN SELECT RAISE(ABORT, 'project bindings are retained indefinitely'); END;
CREATE TABLE project_registrations (
 key TEXT PRIMARY KEY,
 digest TEXT NOT NULL,
 canonical BLOB NOT NULL,
 project_id TEXT NOT NULL REFERENCES projects(id),
 revision INTEGER NOT NULL,
 recorded_at TEXT NOT NULL,
 FOREIGN KEY(project_id, revision) REFERENCES project_bindings(project_id, revision)
);
CREATE TRIGGER project_registrations_no_update BEFORE UPDATE ON project_registrations
 BEGIN SELECT RAISE(ABORT, 'project registrations are immutable'); END;
CREATE TRIGGER project_registrations_no_delete BEFORE DELETE ON project_registrations
 BEGIN SELECT RAISE(ABORT, 'project registrations are retained indefinitely'); END;
CREATE TABLE project_submissions (
 key TEXT PRIMARY KEY REFERENCES receipts(key),
 envelope_digest TEXT NOT NULL,
 envelope_canonical BLOB NOT NULL,
 job_id TEXT NOT NULL UNIQUE REFERENCES jobs(id),
 recorded_at TEXT NOT NULL
);
CREATE TRIGGER project_submissions_no_update BEFORE UPDATE ON project_submissions
 BEGIN SELECT RAISE(ABORT, 'project submissions are immutable'); END;
CREATE TRIGGER project_submissions_no_delete BEFORE DELETE ON project_submissions
 BEGIN SELECT RAISE(ABORT, 'project submissions are retained indefinitely'); END;
CREATE TABLE project_jobs (
 job_id TEXT PRIMARY KEY REFERENCES jobs(id),
 project_id TEXT NOT NULL REFERENCES projects(id),
 binding_revision INTEGER NOT NULL,
 role TEXT NOT NULL CHECK(role IN ('implementation','review')),
 brief_digest TEXT NOT NULL REFERENCES project_manifests(digest),
 target_job_id TEXT REFERENCES jobs(id),
 target_result_digest TEXT REFERENCES project_manifests(digest),
 recorded_at TEXT NOT NULL,
 FOREIGN KEY(project_id, binding_revision) REFERENCES project_bindings(project_id, revision)
);
CREATE TRIGGER project_jobs_no_update BEFORE UPDATE ON project_jobs
 BEGIN SELECT RAISE(ABORT, 'project jobs are immutable'); END;
CREATE TRIGGER project_jobs_no_delete BEFORE DELETE ON project_jobs
 BEGIN SELECT RAISE(ABORT, 'project jobs are retained indefinitely'); END;
CREATE TABLE project_result_associations (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT NOT NULL UNIQUE,
 project_id TEXT NOT NULL REFERENCES projects(id),
 job_id TEXT NOT NULL REFERENCES project_jobs(job_id),
 kind TEXT NOT NULL CHECK(kind IN ('result','review_result')),
 manifest_digest TEXT NOT NULL REFERENCES project_manifests(digest),
 supersedes_id TEXT REFERENCES project_result_associations(id),
 recorded_at TEXT NOT NULL
);
CREATE TRIGGER project_result_associations_no_update BEFORE UPDATE ON project_result_associations
 BEGIN SELECT RAISE(ABORT, 'project result associations are immutable'); END;
CREATE TRIGGER project_result_associations_no_delete BEFORE DELETE ON project_result_associations
 BEGIN SELECT RAISE(ABORT, 'project result associations are retained indefinitely'); END;
PRAGMA user_version=4;
