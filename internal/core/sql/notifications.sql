-- name: PendingNotifications :many
SELECT id,sequence,job_id,state,outcome,exit_code,validation,origin,recorded_at,acknowledged_at
 FROM completion_notifications WHERE acknowledged_at IS NULL ORDER BY sequence LIMIT ?;

-- name: NotificationByID :one
SELECT id,sequence,job_id,state,outcome,exit_code,validation,origin,recorded_at,acknowledged_at
 FROM completion_notifications WHERE id=?;

-- name: NotificationByJob :one
SELECT id,sequence,job_id,state,outcome,exit_code,validation,origin,recorded_at,acknowledged_at
 FROM completion_notifications WHERE job_id=?;

-- name: AcknowledgeNotification :execrows
UPDATE completion_notifications
 SET acknowledged_at=coalesce(acknowledged_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id=?;
