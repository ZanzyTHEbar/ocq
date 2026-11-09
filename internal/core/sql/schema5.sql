CREATE TABLE project_lanes (
 lane_id TEXT NOT NULL,
 project_id TEXT NOT NULL REFERENCES projects(id),
 owner_job_id TEXT NOT NULL UNIQUE REFERENCES project_jobs(job_id),
 role TEXT NOT NULL CHECK(role IN ('implementation','review')),
 base_commit TEXT NOT NULL,
 fsfork_driver TEXT NOT NULL CHECK(fsfork_driver IN ('git_worktree','cow_fsfork')),
 fsfork_path TEXT NOT NULL DEFAULT '',
 sealed_manifest_digest TEXT REFERENCES project_manifests(digest),
 created_at TEXT NOT NULL,
 sealed_at TEXT,
 PRIMARY KEY(project_id, lane_id)
);
CREATE TRIGGER project_lanes_no_update BEFORE UPDATE ON project_lanes
 WHEN OLD.lane_id IS NOT NEW.lane_id OR OLD.project_id IS NOT NEW.project_id OR OLD.owner_job_id IS NOT NEW.owner_job_id
   OR OLD.role IS NOT NEW.role OR OLD.base_commit IS NOT NEW.base_commit OR OLD.fsfork_driver IS NOT NEW.fsfork_driver
   OR OLD.created_at IS NOT NEW.created_at
 BEGIN SELECT RAISE(ABORT, 'project lane ownership is immutable'); END;
CREATE TRIGGER project_lanes_fsfork_once BEFORE UPDATE OF fsfork_path ON project_lanes
 WHEN OLD.fsfork_path <> '' AND NEW.fsfork_path IS NOT OLD.fsfork_path
 BEGIN SELECT RAISE(ABORT, 'project lane fsfork path is set-once'); END;
CREATE TRIGGER project_lanes_seal_pair BEFORE UPDATE OF sealed_manifest_digest, sealed_at ON project_lanes
 WHEN (NEW.sealed_manifest_digest IS NULL) <> (NEW.sealed_at IS NULL)
 BEGIN SELECT RAISE(ABORT, 'project lane seal digest and timestamp must be set together'); END;
CREATE TRIGGER project_lanes_seal_validate BEFORE UPDATE OF sealed_manifest_digest, sealed_at ON project_lanes
 WHEN NEW.sealed_manifest_digest IS NOT NULL AND NOT EXISTS (
   SELECT 1 FROM project_result_associations pra
   WHERE pra.project_id=NEW.project_id AND pra.job_id=NEW.owner_job_id AND pra.manifest_digest=NEW.sealed_manifest_digest
 )
 BEGIN SELECT RAISE(ABORT, 'project lane seal must reference a recorded association for its owner job'); END;
CREATE TRIGGER project_lanes_seal_no_clear BEFORE UPDATE OF sealed_manifest_digest, sealed_at ON project_lanes
 WHEN OLD.sealed_manifest_digest IS NOT NULL AND NEW.sealed_manifest_digest IS NULL
 BEGIN SELECT RAISE(ABORT, 'project lane seal cannot be cleared'); END;
CREATE TRIGGER project_lanes_no_delete BEFORE DELETE ON project_lanes
 BEGIN SELECT RAISE(ABORT, 'project lanes are retained indefinitely'); END;
CREATE TABLE project_inbox_messages (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT NOT NULL UNIQUE,
 key TEXT NOT NULL UNIQUE,
 project_id TEXT NOT NULL REFERENCES projects(id),
 sender TEXT NOT NULL,
 recipient TEXT NOT NULL,
 lane_id TEXT NOT NULL,
 job_id TEXT NOT NULL REFERENCES project_jobs(job_id),
 kind TEXT NOT NULL CHECK(kind IN ('work.submitted','review.submitted','lane.blocked','lane.assigned')),
 subject TEXT NOT NULL,
 body TEXT NOT NULL,
 manifest_digest TEXT REFERENCES project_manifests(digest),
 assoc_id TEXT REFERENCES project_result_associations(id),
 fsfork_path TEXT NOT NULL DEFAULT '',
 reply_to_id TEXT REFERENCES project_inbox_messages(id),
 created_at TEXT NOT NULL,
 read_at TEXT,
 archived_at TEXT,
 FOREIGN KEY(project_id, lane_id) REFERENCES project_lanes(project_id, lane_id)
);
CREATE TRIGGER project_inbox_messages_no_content_update BEFORE UPDATE ON project_inbox_messages
 WHEN OLD.sequence IS NOT NEW.sequence OR OLD.id IS NOT NEW.id OR OLD.key IS NOT NEW.key
   OR OLD.project_id IS NOT NEW.project_id OR OLD.sender IS NOT NEW.sender OR OLD.recipient IS NOT NEW.recipient
   OR OLD.lane_id IS NOT NEW.lane_id OR OLD.job_id IS NOT NEW.job_id OR OLD.kind IS NOT NEW.kind
   OR OLD.subject IS NOT NEW.subject OR OLD.body IS NOT NEW.body
   OR OLD.manifest_digest IS NOT NEW.manifest_digest OR OLD.assoc_id IS NOT NEW.assoc_id
   OR OLD.fsfork_path IS NOT NEW.fsfork_path OR OLD.reply_to_id IS NOT NEW.reply_to_id
   OR OLD.created_at IS NOT NEW.created_at
 BEGIN SELECT RAISE(ABORT, 'project inbox message content is immutable'); END;
CREATE TRIGGER project_inbox_messages_read_once BEFORE UPDATE OF read_at ON project_inbox_messages
 WHEN OLD.read_at IS NOT NULL AND NEW.read_at IS NOT OLD.read_at
 BEGIN SELECT RAISE(ABORT, 'project inbox read_at is set once'); END;
CREATE TRIGGER project_inbox_messages_archive_once BEFORE UPDATE OF archived_at ON project_inbox_messages
 WHEN OLD.archived_at IS NOT NULL AND NEW.archived_at IS NOT OLD.archived_at
 BEGIN SELECT RAISE(ABORT, 'project inbox archived_at is set once'); END;
CREATE TRIGGER project_inbox_messages_archive_requires_read BEFORE UPDATE OF archived_at ON project_inbox_messages
 WHEN NEW.archived_at IS NOT NULL AND NEW.read_at IS NULL
 BEGIN SELECT RAISE(ABORT, 'project inbox message must be read before archiving'); END;
CREATE TRIGGER project_inbox_messages_no_delete BEFORE DELETE ON project_inbox_messages
 BEGIN SELECT RAISE(ABORT, 'project inbox messages are retained indefinitely'); END;
PRAGMA user_version=5;
