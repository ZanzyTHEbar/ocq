-- name: ObservationRevision :one
SELECT revision FROM observation_revision
 WHERE singleton=1 AND typeof(revision)='integer' AND revision>=0;

-- name: ObservationTriggerCount :one
SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name GLOB 'observe_*';

-- name: ObservationSchemaObjects :many
SELECT name,CAST(COALESCE(sql,'') AS TEXT) AS definition FROM sqlite_master
 WHERE (type='table' AND name='observation_revision')
 OR (type='trigger' AND (name GLOB 'observe_*' OR name='observation_revision_retained'))
 ORDER BY name;

-- name: ObservationLedgerExists :one
SELECT count(*) FROM sqlite_master WHERE type='table' AND name='goose_db_version';

-- name: ObservationLedgerVersion :one
SELECT CAST(COALESCE(max(version_id),0) AS INTEGER) FROM goose_db_version WHERE is_applied=1;

-- name: ObservationDetailOversized :one
SELECT CAST(
 (SELECT count(*) FROM project_jobs pj WHERE pj.project_id=sqlc.arg(scope_project))>20 OR
 (SELECT count(*) FROM project_lanes l WHERE l.project_id=sqlc.arg(scope_project))>20 OR
 (SELECT count(*) FROM project_result_associations a WHERE a.project_id=sqlc.arg(scope_project))>20 OR
 EXISTS(SELECT 1 FROM project_manifests m WHERE m.project_id=sqlc.arg(scope_project) AND length(m.canonical)>65536) OR
 EXISTS(SELECT 1 FROM receipts r JOIN project_jobs p ON p.job_id=r.job_id
  WHERE p.project_id=sqlc.arg(scope_project) AND length(r.canonical)>65536)
 AS INTEGER) AS oversized;

-- name: ObservationManifestSize :one
SELECT CAST(length(canonical) AS INTEGER) AS size FROM project_manifests
 WHERE project_id=sqlc.arg(project_id) AND digest=sqlc.arg(digest);

-- name: ObservationProjectsPage :many
SELECT p.id,p.label,CAST(COALESCE(b.revision,0) AS INTEGER) AS binding_revision,
 CAST(COALESCE(b.repo_root,'') AS TEXT) AS repo_root,
 (SELECT count(*) FROM project_inbox_messages m WHERE m.project_id=p.id
  AND m.recipient='integrator:'||p.id AND m.read_at IS NULL) AS unread_count
 FROM projects p LEFT JOIN project_bindings b ON b.project_id=p.id
 AND b.revision=(SELECT max(revision) FROM project_bindings WHERE project_id=p.id)
 ORDER BY p.created_at,p.id LIMIT 21 OFFSET sqlc.arg(page_offset);

-- name: ObservationLanesPage :many
SELECT l.lane_id,l.project_id,l.owner_job_id,l.role,j.state,l.base_commit,
 l.fsfork_driver,l.fsfork_path,l.sealed_manifest_digest,l.created_at,l.sealed_at
 FROM project_lanes l JOIN jobs j ON j.id=l.owner_job_id WHERE l.project_id=sqlc.arg(project_id)
 ORDER BY l.created_at,l.lane_id LIMIT 21 OFFSET sqlc.arg(page_offset);

-- name: ObservationNotificationsPage :many
SELECT id,sequence,job_id,state,outcome,exit_code,validation,origin,recorded_at,acknowledged_at
 FROM completion_notifications WHERE sequence>sqlc.arg(after_sequence) AND acknowledged_at IS NULL
 ORDER BY sequence LIMIT 21;
