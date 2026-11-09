-- name: ProjectLabel :one
SELECT label FROM projects WHERE id=?;

-- name: InsertProject :exec
INSERT INTO projects(id,label,created_at) VALUES(?,?,?);

-- name: CurrentBindingRevision :one
SELECT COALESCE(MAX(revision), 0) FROM project_bindings WHERE project_id=?;

-- name: BindingSession :one
SELECT elephant_session_id FROM project_bindings WHERE project_id=? AND revision=?;

-- name: LaneOwner :one
SELECT owner_job_id FROM project_lanes WHERE project_id=? AND lane_id=?;

-- name: LaneOwnerAndPath :one
SELECT owner_job_id,fsfork_path FROM project_lanes WHERE project_id=? AND lane_id=?;

-- name: ProjectJobRole :one
SELECT role FROM project_jobs WHERE job_id=?;

-- name: AgentLocation :one
SELECT session_id,worktree FROM agent_ownership WHERE job_id=?;

-- name: ProjectByID :one
SELECT id FROM projects WHERE id=?;

-- name: ProjectDetails :one
SELECT label,created_at FROM projects WHERE id=?;

-- name: ProjectBindingCount :one
SELECT COUNT(*) FROM project_bindings WHERE project_id=?;

-- name: LaneByID :one
SELECT lane_id FROM project_lanes WHERE project_id=? AND lane_id=?;

-- name: ManifestProject :one
SELECT project_id FROM project_manifests WHERE digest=?;

-- name: InboxMessageProject :one
SELECT project_id FROM project_inbox_messages WHERE id=?;

-- name: AssociationIdentity :one
SELECT project_id,job_id,kind FROM project_result_associations WHERE id=?;

-- name: ProjectManifest :one
SELECT project_id,version,kind,canonical FROM project_manifests WHERE digest=?;

-- name: InsertManifest :exec
INSERT INTO project_manifests(digest,project_id,version,kind,canonical,recorded_at) VALUES(?,?,?,?,?,?);

-- name: ProjectResultEvents :many
SELECT n.id,n.sequence,n.job_id,n.state,n.outcome,n.exit_code,n.validation,n.origin,n.recorded_at,n.acknowledged_at,
 pj.project_id,pj.role,pj.binding_revision
 FROM completion_notifications n JOIN project_jobs pj ON n.job_id=pj.job_id
 WHERE pj.project_id=? AND n.sequence>? ORDER BY n.sequence ASC LIMIT ?;

-- name: ProjectJobByID :one
SELECT job_id,project_id,binding_revision,role,brief_digest,target_job_id,target_result_digest,recorded_at
 FROM project_jobs WHERE project_id=? AND job_id=?;

-- name: ProjectJobs :many
SELECT pj.job_id,pj.project_id,pj.binding_revision,pj.role,pj.brief_digest,pj.target_job_id,pj.target_result_digest,pj.recorded_at
 FROM project_jobs pj JOIN receipts r ON pj.job_id=r.job_id WHERE pj.project_id=? ORDER BY r.sequence ASC;

-- name: InsertBinding :exec
INSERT INTO project_bindings(project_id,revision,repo_root,git_common_dir,commit_anchor,elephant_session_id,opencode_project_id,opencode_directory,handoff_manifest_digest,auth_ref,verified_at,recorded_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?);

-- name: InsertRegistration :exec
INSERT INTO project_registrations(key,digest,canonical,project_id,revision,recorded_at) VALUES(?,?,?,?,?,?);

-- name: RegistrationByKey :one
SELECT digest,canonical,project_id,revision,recorded_at FROM project_registrations WHERE key=?;

-- name: RegistrationBinding :one
SELECT p.label,p.created_at,b.repo_root,b.git_common_dir,b.commit_anchor,
 b.elephant_session_id,b.opencode_project_id,b.opencode_directory,b.handoff_manifest_digest,
 b.auth_ref,b.verified_at,b.recorded_at FROM projects p JOIN project_bindings b ON p.id=b.project_id
 WHERE b.project_id=? AND b.revision=?;

-- name: InsertSubmission :exec
INSERT INTO project_submissions(key,envelope_digest,envelope_canonical,job_id,recorded_at) VALUES(?,?,?,?,?);

-- name: InsertProjectJob :exec
INSERT INTO project_jobs(job_id,project_id,binding_revision,role,brief_digest,target_job_id,target_result_digest,recorded_at) VALUES(?,?,?,?,?,?,?,?);

-- name: InsertLane :exec
INSERT INTO project_lanes(lane_id,project_id,owner_job_id,role,base_commit,fsfork_driver,fsfork_path,created_at) VALUES(?,?,?,?,?,?,?,?);

-- name: ReceiptDetailsByKey :one
SELECT job_id,canonical,digest FROM receipts WHERE key=?;

-- name: SubmissionByKey :one
SELECT ps.envelope_digest,ps.envelope_canonical,ps.job_id,ps.recorded_at,
 pj.project_id,pj.binding_revision,pj.role,pj.brief_digest,pj.target_job_id,pj.target_result_digest
 FROM project_submissions ps JOIN project_jobs pj ON ps.job_id=pj.job_id WHERE ps.key=?;

-- name: LatestBinding :one
SELECT project_id,revision,repo_root,git_common_dir,commit_anchor,elephant_session_id,opencode_project_id,opencode_directory,handoff_manifest_digest,auth_ref,verified_at,recorded_at
 FROM project_bindings WHERE project_id=? ORDER BY revision DESC LIMIT 1;

-- name: ProjectJobReceipt :one
SELECT pj.project_id,pj.role,r.canonical,r.digest FROM project_jobs pj JOIN receipts r ON pj.job_id=r.job_id WHERE pj.job_id=?;

-- name: ResultAssociationID :one
SELECT id FROM project_result_associations WHERE project_id=? AND job_id=? AND kind='result' AND manifest_digest=? LIMIT 1;

-- name: ManifestBytesByKind :one
SELECT canonical FROM project_manifests WHERE digest=? AND project_id=? AND kind=?;

-- name: ProjectJobPolicy :one
SELECT project_id,binding_revision,role,brief_digest,target_job_id,target_result_digest FROM project_jobs WHERE job_id=?;

-- name: ProjectSummaries :many
SELECT p.id,p.label,COALESCE(b.revision,0),COALESCE(b.repo_root,''),
 (SELECT COUNT(*) FROM project_inbox_messages m WHERE m.project_id=p.id AND m.recipient='integrator:'||p.id AND m.read_at IS NULL)
 FROM projects p LEFT JOIN project_bindings b ON b.project_id=p.id
 AND b.revision=(SELECT MAX(revision) FROM project_bindings WHERE project_id=p.id)
 ORDER BY p.created_at,p.id;

-- name: LaneDetailForJob :one
SELECT lane_id,fsfork_driver,fsfork_path,sealed_manifest_digest FROM project_lanes WHERE project_id=? AND owner_job_id=?;

-- name: ProjectJobReceiptTargets :one
SELECT pj.project_id,pj.role,pj.target_job_id,pj.target_result_digest,r.canonical,r.digest
 FROM project_jobs pj JOIN receipts r ON pj.job_id=r.job_id WHERE pj.job_id=?;

-- name: ExistingAssociation :one
SELECT id,sequence,supersedes_id,recorded_at FROM project_result_associations WHERE project_id=? AND job_id=? AND kind=? AND manifest_digest=?;

-- name: InsertAssociation :execlastid
INSERT INTO project_result_associations(id,project_id,job_id,kind,manifest_digest,supersedes_id,recorded_at) VALUES(?,?,?,?,?,?,?);

-- name: JobAssociations :many
SELECT id,sequence,project_id,job_id,kind,manifest_digest,supersedes_id,recorded_at FROM project_result_associations
 WHERE project_id=? AND job_id=? ORDER BY sequence ASC;

-- name: ReviewAssociations :many
SELECT pra.id,pra.sequence,pra.job_id,pra.manifest_digest FROM project_result_associations pra
 JOIN project_jobs rj ON pra.job_id=rj.job_id
 WHERE pra.project_id=? AND pra.kind='review_result' AND rj.role='review' AND rj.target_job_id=? ORDER BY pra.sequence ASC;

-- name: PendingResultJobs :many
SELECT pj.job_id FROM project_jobs pj JOIN jobs j ON pj.job_id=j.id JOIN receipts r ON pj.job_id=r.job_id
 WHERE pj.project_id=? AND j.state IN ('finished','cancelled','resolved')
 AND NOT EXISTS (SELECT 1 FROM project_result_associations pra WHERE pra.project_id=pj.project_id AND pra.job_id=pj.job_id)
 ORDER BY r.sequence ASC;

-- name: CaptureJobMetadata :one
SELECT pj.project_id,pj.role,r.digest,ao.worktree FROM project_jobs pj
 JOIN receipts r ON pj.job_id=r.job_id JOIN agent_ownership ao ON pj.job_id=ao.job_id WHERE pj.job_id=?;

-- name: CapturedAssociations :many
SELECT pra.id,pra.sequence,pra.manifest_digest,pra.supersedes_id,pra.recorded_at,pm.canonical
 FROM project_result_associations pra JOIN project_manifests pm ON pra.manifest_digest=pm.digest
 WHERE pra.project_id=? AND pra.job_id=? AND pra.kind='result' ORDER BY pra.sequence DESC;

-- name: TargetReviews :many
SELECT pra.id,pra.job_id,pra.manifest_digest,pm.canonical FROM project_result_associations pra
 JOIN project_jobs rj ON pra.job_id=rj.job_id JOIN project_manifests pm ON pra.manifest_digest=pm.digest
 WHERE pra.project_id=? AND pra.kind='review_result' AND rj.role='review' AND rj.target_job_id=? ORDER BY pra.sequence DESC;
