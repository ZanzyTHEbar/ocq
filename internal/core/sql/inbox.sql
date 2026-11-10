-- name: InboxPage :many
SELECT m.id,m.sequence,m.key,m.project_id,m.sender,m.recipient,m.lane_id,m.job_id,m.kind,m.subject,m.body,m.manifest_digest,m.assoc_id,m.fsfork_path,m.reply_to_id,m.created_at,m.read_at,m.archived_at
FROM project_inbox_messages m
WHERE m.project_id=sqlc.arg(project_id) AND m.sequence>sqlc.arg(after_sequence)
 AND (m.recipient=sqlc.arg(recipient) OR sqlc.arg(recipient)='all')
ORDER BY m.sequence ASC LIMIT sqlc.arg(page_limit);

-- name: InboxRecentAnchor :one
SELECT CAST(COALESCE((SELECT sequence FROM project_inbox_messages
 WHERE project_id=sqlc.arg(project_id) AND (recipient=sqlc.arg(recipient) OR sqlc.arg(recipient)='all')
 ORDER BY sequence DESC LIMIT 1 OFFSET sqlc.arg(page_limit)), 0) AS INTEGER);

-- name: UnreadInboxPage :many
SELECT m.id,m.sequence,m.key,m.project_id,m.sender,m.recipient,m.lane_id,m.job_id,m.kind,m.subject,m.body,m.manifest_digest,m.assoc_id,m.fsfork_path,m.reply_to_id,m.created_at,m.read_at,m.archived_at
FROM project_inbox_messages m
WHERE m.project_id=sqlc.arg(project_id) AND m.sequence>sqlc.arg(after_sequence)
 AND (m.recipient=sqlc.arg(recipient) OR sqlc.arg(recipient)='all') AND m.read_at IS NULL
ORDER BY m.sequence ASC LIMIT sqlc.arg(page_limit);

-- name: InboxUnreadCount :one
SELECT COUNT(*) FROM project_inbox_messages
WHERE project_id=sqlc.arg(project_id) AND read_at IS NULL
 AND (recipient=sqlc.arg(recipient) OR sqlc.arg(recipient)='all');

-- name: BoardUnreadMessages :many
SELECT m.id,m.sequence,m.key,m.project_id,m.sender,m.recipient,m.lane_id,m.job_id,m.kind,m.subject,m.body,m.manifest_digest,m.assoc_id,m.fsfork_path,m.reply_to_id,m.created_at,m.read_at,m.archived_at
FROM project_inbox_messages m WHERE m.project_id=? AND m.recipient=? AND m.read_at IS NULL ORDER BY m.sequence ASC LIMIT 20;

-- name: InboxMessageByKey :one
SELECT m.id,m.sequence,m.key,m.project_id,m.sender,m.recipient,m.lane_id,m.job_id,m.kind,m.subject,m.body,m.manifest_digest,m.assoc_id,m.fsfork_path,m.reply_to_id,m.created_at,m.read_at,m.archived_at
 FROM project_inbox_messages m WHERE m.key=?;

-- name: InboxMessageByID :one
SELECT m.id,m.sequence,m.key,m.project_id,m.sender,m.recipient,m.lane_id,m.job_id,m.kind,m.subject,m.body,m.manifest_digest,m.assoc_id,m.fsfork_path,m.reply_to_id,m.created_at,m.read_at,m.archived_at
 FROM project_inbox_messages m WHERE m.project_id=? AND m.id=?;

-- name: InsertInboxMessage :execlastid
INSERT INTO project_inbox_messages(id,key,project_id,sender,recipient,lane_id,job_id,kind,subject,body,manifest_digest,assoc_id,fsfork_path,reply_to_id,created_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?);

-- name: LaneForJob :one
SELECT lane_id,project_id,owner_job_id,role,base_commit,fsfork_driver,fsfork_path,sealed_manifest_digest,created_at,sealed_at
 FROM project_lanes WHERE project_id=? AND owner_job_id=?;

-- name: ProjectJobRoleAndRequest :one
SELECT pj.role,r.canonical FROM project_jobs pj JOIN receipts r ON pj.job_id=r.job_id WHERE pj.job_id=? AND pj.project_id=?;

-- name: AssociationSequence :one
SELECT sequence FROM project_result_associations WHERE id=? AND project_id=? AND job_id=?;

-- name: SealedAssociationSequence :one
SELECT pra.sequence FROM project_lanes l JOIN project_result_associations pra
 ON pra.project_id=l.project_id AND pra.job_id=l.owner_job_id AND pra.manifest_digest=l.sealed_manifest_digest
 WHERE l.project_id=? AND l.owner_job_id=?;

-- name: SealLane :exec
UPDATE project_lanes SET sealed_manifest_digest=CASE WHEN CAST(sqlc.arg(should_seal) AS INTEGER) THEN sqlc.arg(manifest_digest) ELSE sealed_manifest_digest END,
 sealed_at=CASE WHEN CAST(sqlc.arg(should_seal) AS INTEGER) THEN sqlc.arg(sealed_time) ELSE sealed_at END,
 fsfork_path=CASE WHEN fsfork_path='' AND CAST(sqlc.narg(new_path) AS TEXT) IS NOT NULL THEN CAST(sqlc.narg(new_path) AS TEXT) ELSE fsfork_path END
 WHERE project_id=sqlc.arg(project_id) AND owner_job_id=sqlc.arg(owner_job_id);

-- name: ProjectLanes :many
SELECT l.lane_id,l.project_id,l.owner_job_id,l.role,j.state,l.base_commit,l.fsfork_driver,l.fsfork_path,l.sealed_manifest_digest,l.created_at,l.sealed_at
 FROM project_lanes l JOIN jobs j ON l.owner_job_id=j.id WHERE l.project_id=? ORDER BY l.created_at ASC,l.lane_id ASC;

-- name: IntegratorUnreadCount :one
SELECT COUNT(*) FROM project_inbox_messages WHERE project_id=? AND recipient=? AND read_at IS NULL;

-- name: MarkInboxMessage :execrows
UPDATE project_inbox_messages SET read_at=COALESCE(read_at,sqlc.arg(read_time)),
 archived_at=CASE WHEN CAST(sqlc.arg(archive) AS INTEGER) THEN COALESCE(archived_at,sqlc.arg(archive_time)) ELSE archived_at END
 WHERE project_id=sqlc.arg(project_id) AND id=sqlc.arg(id);
