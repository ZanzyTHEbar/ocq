-- sqlc schema input ONLY. Goose owns this table at runtime; this file is never
-- embedded or executed by OCQ. It describes the dependency's migration ledger.
CREATE TABLE goose_db_version (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 version_id INTEGER NOT NULL,
 is_applied BOOLEAN NOT NULL,
 tstamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
-- SQLite's built-in schema catalog, likewise compiler input only.
CREATE TABLE sqlite_master (
 type TEXT NOT NULL, name TEXT NOT NULL, tbl_name TEXT NOT NULL,
 rootpage INTEGER, sql TEXT
);
