CREATE TABLE IF NOT EXISTS receipts (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 key TEXT NOT NULL UNIQUE, job_id TEXT NOT NULL UNIQUE,
 canonical BLOB NOT NULL, digest TEXT NOT NULL, accepted_at TEXT NOT NULL
);
CREATE TRIGGER IF NOT EXISTS receipts_no_update BEFORE UPDATE ON receipts
 BEGIN SELECT RAISE(ABORT, 'receipts are immutable'); END;
CREATE TRIGGER IF NOT EXISTS receipts_no_delete BEFORE DELETE ON receipts
 BEGIN SELECT RAISE(ABORT, 'receipts are retained indefinitely'); END;
CREATE TABLE IF NOT EXISTS jobs (
 id TEXT PRIMARY KEY REFERENCES receipts(job_id),
 state TEXT NOT NULL CHECK(state IN ('queued','launching','running','unknown','finished','cancelled','resolved')),
 reason TEXT NOT NULL DEFAULT '', pid INTEGER NOT NULL DEFAULT 0,
 start_ticks INTEGER NOT NULL DEFAULT 0, boot TEXT NOT NULL DEFAULT '',
 stdout TEXT NOT NULL DEFAULT '', stderr TEXT NOT NULL DEFAULT '',
 exit_code INTEGER, outcome TEXT NOT NULL DEFAULT 'pending',
 validation TEXT NOT NULL DEFAULT 'not_run', evidence TEXT NOT NULL DEFAULT ''
);
PRAGMA user_version=1;
