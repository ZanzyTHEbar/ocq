CREATE TABLE completion_notifications (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT NOT NULL UNIQUE,
 job_id TEXT NOT NULL UNIQUE REFERENCES jobs(id),
 state TEXT NOT NULL CHECK(state IN ('finished','cancelled','resolved')),
 outcome TEXT NOT NULL, exit_code INTEGER, validation TEXT NOT NULL,
 origin TEXT NOT NULL CHECK(origin IN ('transition','migration_snapshot')),
 recorded_at TEXT NOT NULL, acknowledged_at TEXT
);
CREATE INDEX notifications_pending ON completion_notifications(sequence)
 WHERE acknowledged_at IS NULL;
INSERT INTO completion_notifications(id,job_id,state,outcome,exit_code,validation,origin,recorded_at)
 SELECT 'completion-' || j.id,j.id,j.state,j.outcome,j.exit_code,j.validation,
 'migration_snapshot',strftime('%Y-%m-%dT%H:%M:%fZ','now')
 FROM jobs j JOIN receipts r ON r.job_id=j.id
 WHERE j.state IN ('finished','cancelled','resolved') ORDER BY r.sequence;
CREATE TRIGGER job_completion_notification AFTER UPDATE OF state ON jobs
 WHEN NEW.state IN ('finished','cancelled','resolved') AND OLD.state<>NEW.state
 BEGIN
 INSERT INTO completion_notifications(id,job_id,state,outcome,exit_code,validation,origin,recorded_at)
 VALUES('completion-' || NEW.id,NEW.id,NEW.state,NEW.outcome,NEW.exit_code,NEW.validation,
 'transition',strftime('%Y-%m-%dT%H:%M:%fZ','now'))
 ON CONFLICT(job_id) DO NOTHING;
 END;
CREATE TRIGGER notification_content_immutable BEFORE UPDATE OF sequence,id,job_id,state,outcome,exit_code,validation,origin,recorded_at
 ON completion_notifications
 BEGIN SELECT RAISE(ABORT, 'completion notifications are immutable'); END;
CREATE TRIGGER notification_ack_once BEFORE UPDATE OF acknowledged_at ON completion_notifications
 WHEN OLD.acknowledged_at IS NOT NULL AND NEW.acknowledged_at IS NOT OLD.acknowledged_at
 BEGIN SELECT RAISE(ABORT, 'notification acknowledgement cannot be changed'); END;
CREATE TRIGGER notification_retained BEFORE DELETE ON completion_notifications
 BEGIN SELECT RAISE(ABORT, 'completion notifications are retained'); END;
PRAGMA user_version=3;
