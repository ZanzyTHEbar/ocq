-- Admission retains the intended Main; publication fills the pointer to the
-- original inbox envelope once. No message content is copied into the outbox.
CREATE TABLE project_task_handoffs (
 project_id TEXT NOT NULL,
 request_id TEXT NOT NULL,
 intent_digest TEXT NOT NULL,
 job_id TEXT NOT NULL UNIQUE REFERENCES project_jobs(job_id),
 main_revision INTEGER NOT NULL,
 main_setup_id TEXT NOT NULL,
 main_session_id TEXT NOT NULL,
 inbox_id TEXT UNIQUE REFERENCES project_inbox_messages(id),
 PRIMARY KEY(project_id, request_id),
 FOREIGN KEY(project_id, main_revision) REFERENCES project_bindings(project_id, revision)
);
CREATE TRIGGER project_task_handoff_identity BEFORE UPDATE OF project_id,request_id,intent_digest,job_id,main_revision,main_setup_id,main_session_id ON project_task_handoffs
 BEGIN SELECT RAISE(ABORT, 'task handoff identity is immutable'); END;
CREATE TRIGGER project_task_handoff_publish BEFORE UPDATE OF inbox_id ON project_task_handoffs
 WHEN OLD.inbox_id IS NOT NULL OR NOT EXISTS (
  SELECT 1 FROM project_inbox_messages m
  JOIN project_lanes l ON l.project_id=m.project_id AND l.lane_id=m.lane_id AND l.owner_job_id=m.job_id
  JOIN project_result_associations a ON a.id=m.assoc_id AND a.project_id=m.project_id AND a.job_id=m.job_id
  WHERE m.id=NEW.inbox_id AND m.project_id=OLD.project_id AND m.job_id=OLD.job_id
    AND m.kind='work.submitted' AND m.key='auto-mail:'||a.id AND m.reply_to_id IS NULL
    AND m.sender='lane:'||l.lane_id AND m.recipient='integrator:'||m.project_id
    AND l.role='implementation' AND a.kind='result' AND a.manifest_digest=m.manifest_digest
    AND m.manifest_digest=l.sealed_manifest_digest
 )
 BEGIN SELECT RAISE(ABORT, 'expected one original sealed task result envelope'); END;
CREATE TRIGGER project_task_handoff_no_delete BEFORE DELETE ON project_task_handoffs
 BEGIN SELECT RAISE(ABORT, 'task handoffs are retained indefinitely'); END;
CREATE TRIGGER project_task_handoff_revision AFTER UPDATE OF inbox_id ON project_task_handoffs
 BEGIN UPDATE observation_revision SET revision=revision+1 WHERE singleton=1; END;

CREATE TABLE project_main_handled (
 project_id TEXT NOT NULL REFERENCES projects(id),
 request_id TEXT NOT NULL,
 intent_digest TEXT NOT NULL,
 submission_key TEXT NOT NULL REFERENCES project_submissions(key),
 envelope_digest TEXT NOT NULL,
 admission_revision INTEGER NOT NULL,
 job_id TEXT NOT NULL REFERENCES project_jobs(job_id),
 lane_id TEXT NOT NULL,
 inbox_id TEXT NOT NULL UNIQUE REFERENCES project_inbox_messages(id),
 inbox_sequence INTEGER NOT NULL REFERENCES project_inbox_messages(sequence),
 association_id TEXT NOT NULL REFERENCES project_result_associations(id),
 result_digest TEXT NOT NULL REFERENCES project_manifests(digest),
 main_revision INTEGER NOT NULL,
 main_setup_id TEXT NOT NULL,
 main_session_id TEXT NOT NULL,
 main_role_digest TEXT NOT NULL,
 native_message_id TEXT NOT NULL,
 native_call_id TEXT NOT NULL,
 recorded_at TEXT NOT NULL,
 PRIMARY KEY(project_id, request_id),
 UNIQUE(main_session_id, native_message_id, native_call_id),
 FOREIGN KEY(project_id, request_id) REFERENCES project_task_handoffs(project_id, request_id),
 FOREIGN KEY(project_id, lane_id) REFERENCES project_lanes(project_id, lane_id),
 FOREIGN KEY(project_id, admission_revision) REFERENCES project_bindings(project_id, revision),
 FOREIGN KEY(project_id, main_revision) REFERENCES project_bindings(project_id, revision)
);
CREATE TRIGGER project_main_handled_no_update BEFORE UPDATE ON project_main_handled
 BEGIN SELECT RAISE(ABORT, 'Main handled receipts are immutable'); END;
CREATE TRIGGER project_main_handled_no_delete BEFORE DELETE ON project_main_handled
 BEGIN SELECT RAISE(ABORT, 'Main handled receipts are retained indefinitely'); END;
CREATE TRIGGER project_main_handled_revision AFTER INSERT ON project_main_handled
 BEGIN UPDATE observation_revision SET revision=revision+1 WHERE singleton=1; END;

-- Durable pending/dead-letter projection over references, not another queue.
-- A completed receipt remains handled even if Main or the seal changes later.
CREATE VIEW project_task_handoff_status AS
 SELECT h.*, m.sequence AS inbox_sequence, m.assoc_id, m.manifest_digest, m.lane_id,
 CASE
  WHEN r.request_id IS NOT NULL THEN 'handled'
  WHEN NOT EXISTS (SELECT 1 FROM project_bindings b
                   WHERE b.project_id=h.project_id AND b.revision>h.main_revision)
       AND (h.inbox_id IS NULL OR l.sealed_manifest_digest IS m.manifest_digest)
    THEN CASE WHEN h.inbox_id IS NULL THEN 'awaiting_result' ELSE 'pending_main' END
  ELSE 'dead_letter'
 END AS state,
 CASE
  WHEN r.request_id IS NOT NULL THEN ''
  WHEN EXISTS (SELECT 1 FROM project_bindings b
               WHERE b.project_id=h.project_id AND b.revision>h.main_revision) THEN 'main_binding_replaced'
  WHEN h.inbox_id IS NOT NULL AND l.sealed_manifest_digest IS NOT m.manifest_digest THEN 'result_superseded'
  ELSE ''
 END AS reason
 FROM project_task_handoffs h
 LEFT JOIN project_inbox_messages m ON m.id=h.inbox_id
 LEFT JOIN project_lanes l ON l.project_id=h.project_id AND l.owner_job_id=h.job_id
 LEFT JOIN project_main_handled r ON r.project_id=h.project_id AND r.request_id=h.request_id;
PRAGMA user_version=8;
