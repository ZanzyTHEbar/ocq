-- Durable invalidation hints, not an event log. Every increment shares the
-- observable write transaction; rollback rolls back its revision as well.
CREATE TABLE observation_revision (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision>=0)
);
INSERT INTO observation_revision(singleton,revision) VALUES(1,0);
CREATE TRIGGER observation_revision_retained BEFORE DELETE ON observation_revision
 BEGIN SELECT RAISE(ABORT, 'observation revision is retained'); END;
CREATE TRIGGER observe_receipts_insert AFTER INSERT ON receipts BEGIN
 UPDATE observation_revision SET revision=revision+1 WHERE singleton=1; END;
CREATE TRIGGER observe_projects_insert AFTER INSERT ON projects BEGIN
 UPDATE observation_revision SET revision=revision+1 WHERE singleton=1; END;
CREATE TRIGGER observe_project_manifests_insert AFTER INSERT ON project_manifests BEGIN
 UPDATE observation_revision SET revision=revision+1 WHERE singleton=1; END;
CREATE TRIGGER observe_project_bindings_insert AFTER INSERT ON project_bindings BEGIN
 UPDATE observation_revision SET revision=revision+1 WHERE singleton=1; END;
CREATE TRIGGER observe_project_registrations_insert AFTER INSERT ON project_registrations BEGIN
 UPDATE observation_revision SET revision=revision+1 WHERE singleton=1; END;
CREATE TRIGGER observe_project_submissions_insert AFTER INSERT ON project_submissions BEGIN
 UPDATE observation_revision SET revision=revision+1 WHERE singleton=1; END;
CREATE TRIGGER observe_project_jobs_insert AFTER INSERT ON project_jobs BEGIN
 UPDATE observation_revision SET revision=revision+1 WHERE singleton=1; END;
CREATE TRIGGER observe_project_result_associations_insert AFTER INSERT ON project_result_associations BEGIN
 UPDATE observation_revision SET revision=revision+1 WHERE singleton=1; END;
CREATE TRIGGER observe_jobs_insert AFTER INSERT ON jobs BEGIN
 UPDATE observation_revision SET revision=revision+1 WHERE singleton=1; END;
CREATE TRIGGER observe_jobs_delete AFTER DELETE ON jobs BEGIN
 UPDATE observation_revision SET revision=revision+1 WHERE singleton=1; END;
CREATE TRIGGER observe_jobs_update AFTER UPDATE ON jobs
 WHEN OLD.id IS NOT NEW.id OR OLD.state IS NOT NEW.state OR OLD.reason IS NOT NEW.reason
 OR OLD.pid IS NOT NEW.pid OR OLD.start_ticks IS NOT NEW.start_ticks OR OLD.boot IS NOT NEW.boot
 OR OLD.stdout IS NOT NEW.stdout OR OLD.stderr IS NOT NEW.stderr OR OLD.exit_code IS NOT NEW.exit_code
 OR OLD.outcome IS NOT NEW.outcome OR OLD.validation IS NOT NEW.validation OR OLD.evidence IS NOT NEW.evidence
 BEGIN UPDATE observation_revision SET revision=revision+1 WHERE singleton=1; END;
CREATE TRIGGER observe_agent_ownership_insert AFTER INSERT ON agent_ownership BEGIN
 UPDATE observation_revision SET revision=revision+1 WHERE singleton=1; END;
CREATE TRIGGER observe_agent_ownership_delete AFTER DELETE ON agent_ownership BEGIN
 UPDATE observation_revision SET revision=revision+1 WHERE singleton=1; END;
CREATE TRIGGER observe_agent_ownership_update AFTER UPDATE ON agent_ownership
 WHEN OLD.job_id IS NOT NEW.job_id OR OLD.session_id IS NOT NEW.session_id OR OLD.prompt_id IS NOT NEW.prompt_id
 OR OLD.token IS NOT NEW.token OR OLD.phase IS NOT NEW.phase OR OLD.worktree IS NOT NEW.worktree
 OR OLD.project_id IS NOT NEW.project_id OR OLD.server_outcome IS NOT NEW.server_outcome
 OR OLD.terminal_evidence IS NOT NEW.terminal_evidence
 BEGIN UPDATE observation_revision SET revision=revision+1 WHERE singleton=1; END;
CREATE TRIGGER observe_project_lanes_insert AFTER INSERT ON project_lanes BEGIN
 UPDATE observation_revision SET revision=revision+1 WHERE singleton=1; END;
CREATE TRIGGER observe_project_lanes_update AFTER UPDATE ON project_lanes
 WHEN OLD.fsfork_path IS NOT NEW.fsfork_path OR OLD.sealed_manifest_digest IS NOT NEW.sealed_manifest_digest
 OR OLD.sealed_at IS NOT NEW.sealed_at
 BEGIN UPDATE observation_revision SET revision=revision+1 WHERE singleton=1; END;
CREATE TRIGGER observe_project_inbox_messages_insert AFTER INSERT ON project_inbox_messages BEGIN
 UPDATE observation_revision SET revision=revision+1 WHERE singleton=1; END;
CREATE TRIGGER observe_project_inbox_messages_update AFTER UPDATE ON project_inbox_messages
 WHEN OLD.read_at IS NOT NEW.read_at OR OLD.archived_at IS NOT NEW.archived_at
 BEGIN UPDATE observation_revision SET revision=revision+1 WHERE singleton=1; END;
CREATE TRIGGER observe_completion_notifications_insert AFTER INSERT ON completion_notifications BEGIN
 UPDATE observation_revision SET revision=revision+1 WHERE singleton=1; END;
CREATE TRIGGER observe_completion_notifications_update AFTER UPDATE ON completion_notifications
 WHEN OLD.acknowledged_at IS NOT NEW.acknowledged_at
 BEGIN UPDATE observation_revision SET revision=revision+1 WHERE singleton=1; END;
PRAGMA user_version=6;
