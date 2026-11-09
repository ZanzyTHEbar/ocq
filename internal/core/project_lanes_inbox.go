package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ZanzyTHEbar/faults-go"

	"ocq/internal/core/dbquery"
)

type ProjectLane struct {
	LaneID               string  `json:"lane_id"`
	ProjectID            string  `json:"project_id"`
	OwnerJobID           string  `json:"owner_job_id"`
	Role                 string  `json:"role"`
	JobState             string  `json:"job_state"`
	BaseCommit           string  `json:"base_commit"`
	FsforkDriver         string  `json:"fsfork_driver"`
	FsforkPath           string  `json:"fsfork_path"`
	SealedManifestDigest *string `json:"sealed_manifest_digest,omitempty"`
	CreatedAt            string  `json:"created_at"`
	SealedAt             *string `json:"sealed_at,omitempty"`
}

type ProjectLanesOutput struct {
	ProjectID string        `json:"project_id"`
	ReadAt    string        `json:"read_at"`
	Freshness string        `json:"freshness"`
	Lanes     []ProjectLane `json:"lanes"`
}

type ProjectInboxMessage struct {
	ID             string  `json:"id"`
	Sequence       int64   `json:"sequence"`
	Key            string  `json:"key"`
	ProjectID      string  `json:"project_id"`
	Sender         string  `json:"sender"`
	Recipient      string  `json:"recipient"`
	LaneID         string  `json:"lane_id"`
	JobID          string  `json:"job_id"`
	Kind           string  `json:"kind"`
	Subject        string  `json:"subject"`
	Body           string  `json:"body"`
	ManifestDigest *string `json:"manifest_digest,omitempty"`
	AssocID        *string `json:"assoc_id,omitempty"`
	FsforkPath     string  `json:"fsfork_path"`
	ReplyToID      *string `json:"reply_to_id,omitempty"`
	CreatedAt      string  `json:"created_at"`
	ReadAt         *string `json:"read_at,omitempty"`
	ArchivedAt     *string `json:"archived_at,omitempty"`
}

type ProjectInboxOutput struct {
	ProjectID     string                `json:"project_id"`
	Recipient     string                `json:"recipient"`
	AfterSequence int64                 `json:"after_sequence"`
	NextSequence  int64                 `json:"next_sequence"`
	UnreadCount   int                   `json:"unread_count"`
	ReadAt        string                `json:"read_at"`
	Freshness     string                `json:"freshness"`
	Messages      []ProjectInboxMessage `json:"messages"`
}

func isValidLaneID(id string) bool {
	if !strings.HasPrefix(id, "lane-") || len(id) <= len("lane-") || len(id) > 128 || !utf8.ValidString(id) {
		return false
	}
	for i := len("lane-"); i < len(id); i++ {
		c := id[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func isValidInboxMessageID(id string) bool {
	if !strings.HasPrefix(id, "msg-") || len(id) != len("msg-")+32 {
		return false
	}
	return isValidHexLen(id[len("msg-"):], 32)
}

func isValidInboxKey(key string) bool {
	return key != "" && len(key) <= 256 && utf8.ValidString(key) && !strings.ContainsRune(key, 0)
}

func isValidInboxKind(kind string) bool {
	switch kind {
	case "work.submitted", "review.submitted", "lane.blocked", "lane.assigned":
		return true
	}
	return false
}

func deterministicMessageID(key string) string {
	return "msg-" + sha256Hex([]byte(key))[:32]
}

func validateInboxSender(sender, laneID, jobID string) error {
	if strings.HasPrefix(sender, "lane:") {
		if sender != "lane:"+laneID {
			return faults.New(faults.CodeValidationFailed, fmt.Sprintf("sender %q must name the lane %s owning this message", sender, laneID))
		}
		return nil
	}
	if strings.HasPrefix(sender, "job:") {
		if sender != "job:"+jobID {
			return faults.New(faults.CodeValidationFailed, fmt.Sprintf("sender %q must name the job %s owning this message", sender, jobID))
		}
		return nil
	}
	if strings.HasPrefix(sender, "integrator:") {
		id := strings.TrimPrefix(sender, "integrator:")
		if id == "" || len(id) > 128 || !utf8.ValidString(id) || strings.ContainsAny(id, " \t\r\n\x00") {
			return faults.New(faults.CodeValidationFailed, fmt.Sprintf("invalid integrator sender %q", sender))
		}
		return nil
	}
	return faults.New(faults.CodeValidationFailed, fmt.Sprintf("sender %q must be lane:<lane_id>, job:<job_id>, or integrator:<id>", sender))
}

func validateInboxRecipient(recipient, projectID string) error {
	if recipient == "integrator:"+projectID {
		return nil
	}
	if strings.HasPrefix(recipient, "lane:") && isValidLaneID(strings.TrimPrefix(recipient, "lane:")) {
		return nil
	}
	return faults.New(faults.CodeValidationFailed, fmt.Sprintf("recipient %q must be integrator:%s or lane:<lane_id>", recipient, projectID))
}

func nullableSQLString(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	v := s.String
	return &v
}

func stringPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func inboxMessageFromRow(row dbquery.InboxPageRow) ProjectInboxMessage {
	return ProjectInboxMessage{
		ID: row.ID, Sequence: row.Sequence, Key: row.Key, ProjectID: row.ProjectID,
		Sender: row.Sender, Recipient: row.Recipient, LaneID: row.LaneID, JobID: row.JobID,
		Kind: row.Kind, Subject: row.Subject, Body: row.Body,
		ManifestDigest: nullableSQLString(row.ManifestDigest), AssocID: nullableSQLString(row.AssocID),
		FsforkPath: row.FsforkPath, ReplyToID: nullableSQLString(row.ReplyToID), CreatedAt: row.CreatedAt,
		ReadAt: nullableSQLString(row.ReadAt), ArchivedAt: nullableSQLString(row.ArchivedAt),
	}
}

func inboxMessageContentEqual(a, b ProjectInboxMessage) bool {
	return a.Key == b.Key && a.ProjectID == b.ProjectID && a.Sender == b.Sender && a.Recipient == b.Recipient &&
		a.LaneID == b.LaneID && a.JobID == b.JobID && a.Kind == b.Kind && a.Subject == b.Subject && a.Body == b.Body &&
		stringPtrEqual(a.ManifestDigest, b.ManifestDigest) && stringPtrEqual(a.AssocID, b.AssocID) &&
		a.FsforkPath == b.FsforkPath && stringPtrEqual(a.ReplyToID, b.ReplyToID)
}

func insertInboxMessageConn(ctx context.Context, c *sql.Conn, msg ProjectInboxMessage) (ProjectInboxMessage, error) {
	if !isValidInboxKey(msg.Key) || !isValidInboxKind(msg.Kind) || msg.Subject == "" || len(msg.Subject) > 512 || !utf8.ValidString(msg.Subject) || strings.ContainsRune(msg.Subject, 0) {
		return ProjectInboxMessage{}, faults.New(faults.CodeValidationFailed, "invalid inbox message key, kind, or subject")
	}
	if len(msg.Body) > 16<<10 || !utf8.ValidString(msg.Body) || strings.ContainsRune(msg.Body, 0) {
		return ProjectInboxMessage{}, faults.New(faults.CodeValidationFailed, "inbox message body must be valid UTF-8 <=16384 bytes without NUL")
	}
	if err := validateInboxSender(msg.Sender, msg.LaneID, msg.JobID); err != nil {
		return ProjectInboxMessage{}, err
	}
	if err := validateInboxRecipient(msg.Recipient, msg.ProjectID); err != nil {
		return ProjectInboxMessage{}, err
	}
	queries := dbquery.New(c)
	row, err := queries.InboxMessageByKey(ctx, msg.Key)
	if err == nil {
		existing := inboxMessageFromRow(dbquery.InboxPageRow(row))
		if !inboxMessageContentEqual(existing, msg) {
			return ProjectInboxMessage{}, faults.New(faults.CodeValidationFailed, fmt.Sprintf("inbox message key %q already belongs to different content", msg.Key))
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ProjectInboxMessage{}, classifyDiagnostic(faults.CodeDatabaseRead, err)
	}
	msg.ID = deterministicMessageID(msg.Key)
	msg.Sequence, err = queries.InsertInboxMessage(ctx, dbquery.InsertInboxMessageParams{ID: msg.ID, Key: msg.Key, ProjectID: msg.ProjectID, Sender: msg.Sender, Recipient: msg.Recipient, LaneID: msg.LaneID, JobID: msg.JobID, Kind: msg.Kind, Subject: msg.Subject, Body: msg.Body, ManifestDigest: sqlStringPtr(msg.ManifestDigest), AssocID: sqlStringPtr(msg.AssocID), FsforkPath: msg.FsforkPath, ReplyToID: sqlStringPtr(msg.ReplyToID), CreatedAt: msg.CreatedAt})
	if err != nil {
		return ProjectInboxMessage{}, classifyDiagnostic(faults.CodeDatabaseWrite, err)
	}
	return msg, nil
}

func ensureLaneConn(ctx context.Context, c *sql.Conn, projectID, jobID, ts string) (ProjectLane, error) {
	var lane ProjectLane
	queries := dbquery.New(c)
	row, err := queries.LaneForJob(ctx, dbquery.LaneForJobParams{ProjectID: projectID, OwnerJobID: jobID})
	if err == nil {
		lane = ProjectLane{LaneID: row.LaneID, ProjectID: row.ProjectID, OwnerJobID: row.OwnerJobID, Role: row.Role, BaseCommit: row.BaseCommit, FsforkDriver: row.FsforkDriver, FsforkPath: row.FsforkPath, SealedManifestDigest: nullableSQLString(row.SealedManifestDigest), CreatedAt: row.CreatedAt, SealedAt: nullableSQLString(row.SealedAt)}
		return lane, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return lane, classifyDiagnostic(faults.CodeDatabaseRead, err)
	}
	job, err := queries.ProjectJobRoleAndRequest(ctx, dbquery.ProjectJobRoleAndRequestParams{JobID: jobID, ProjectID: projectID})
	if err != nil {
		return lane, faults.Wrap(faults.CodeDatabaseRead, fmt.Sprintf("project lane unavailable for job %s", jobID), err)
	}
	_, agentReq, err := storedRequest(job.Canonical)
	if err != nil || agentReq == nil {
		return lane, faults.New(faults.CodeInvariantFailed, fmt.Sprintf("project lane unavailable for job %s: no canonical v2 agent request", jobID))
	}
	laneID := "lane-" + sha256Hex([]byte(jobID))[:16]
	err = queries.InsertLane(ctx, dbquery.InsertLaneParams{LaneID: laneID, ProjectID: projectID, OwnerJobID: jobID, Role: job.Role, BaseCommit: agentReq.Base, FsforkDriver: "git_worktree", FsforkPath: "", CreatedAt: ts})
	if err != nil {
		return lane, classifyDiagnostic(faults.CodeDatabaseWrite, err)
	}
	return ProjectLane{
		LaneID: laneID, ProjectID: projectID, OwnerJobID: jobID, Role: job.Role, BaseCommit: agentReq.Base,
		FsforkDriver: "git_worktree", CreatedAt: ts,
	}, nil
}

// sealLaneAndDeliverMail atomically seals the lane with the recorded manifest
// digest and deposits the corresponding immutable message into the project
// integrator inbox. It is called inside the same SQLite transaction that
// records the manifest and result association.
func sealLaneAndDeliverMail(ctx context.Context, c *sql.Conn, m parsedManifest, projectID, jobID, assocID, fsforkPath, ts string) error {
	lane, err := ensureLaneConn(ctx, c, projectID, jobID, ts)
	if err != nil {
		return err
	}
	effectivePath := lane.FsforkPath
	if effectivePath == "" {
		effectivePath = fsforkPath
	}
	var pathArg sql.NullString
	if effectivePath != "" {
		pathArg = sql.NullString{String: effectivePath, Valid: true}
	}
	queries := dbquery.New(c)
	incomingSeq, err := queries.AssociationSequence(ctx, dbquery.AssociationSequenceParams{ID: assocID, ProjectID: projectID, JobID: jobID})
	if err != nil {
		return faults.Wrap(faults.CodeDatabaseRead, "lane seal association unavailable", err)
	}
	currentSeq, err := queries.SealedAssociationSequence(ctx, dbquery.SealedAssociationSequenceParams{ProjectID: projectID, OwnerJobID: jobID})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return classifyDiagnostic(faults.CodeDatabaseRead, err)
	}
	shouldSeal := errors.Is(err, sql.ErrNoRows) || incomingSeq >= currentSeq
	if err := queries.SealLane(ctx, dbquery.SealLaneParams{ShouldSeal: int64(boolInt(shouldSeal)), ManifestDigest: sql.NullString{String: m.Digest, Valid: true}, SealedTime: sql.NullString{String: ts, Valid: true}, NewPath: pathArg, ProjectID: projectID, OwnerJobID: jobID}); err != nil {
		return classifyDiagnostic(faults.CodeDatabaseWrite, err)
	}
	var kind, subject, body string
	switch m.Kind {
	case "result":
		if m.Result == nil {
			return faults.New(faults.CodeInvariantFailed, "result manifest missing result payload")
		}
		kind = "work.submitted"
		subject = fmt.Sprintf("[%s] implementation snapshot %s", lane.LaneID, m.Digest[:12])
		body = fmt.Sprintf("Implementation snapshot %s captured from %s with %d changed file(s); verification=%s.",
			m.Digest[:12], m.Source, len(m.Result.ChangedFiles), effectiveVerification(m.Source))
	case "review_result":
		if m.ReviewResult == nil {
			return faults.New(faults.CodeInvariantFailed, "review result manifest missing review payload")
		}
		kind = "review.submitted"
		reviewed := m.ReviewResult.ReviewedResultDigest
		if len(reviewed) > 12 {
			reviewed = reviewed[:12]
		}
		subject = fmt.Sprintf("[%s] review verdict: %s (%s)", lane.LaneID, m.ReviewResult.Verdict, reviewed)
		body = fmt.Sprintf("Review verdict %s for result %s with %d finding(s).", m.ReviewResult.Verdict, reviewed, len(m.ReviewResult.Findings))
	default:
		return faults.New(faults.CodeInvariantFailed, fmt.Sprintf("unsupported lane seal manifest kind %q", m.Kind))
	}
	manifestDigest := m.Digest
	assoc := assocID
	_, err = insertInboxMessageConn(ctx, c, ProjectInboxMessage{
		Key:            "auto-mail:" + assocID,
		ProjectID:      projectID,
		Sender:         "lane:" + lane.LaneID,
		Recipient:      "integrator:" + projectID,
		LaneID:         lane.LaneID,
		JobID:          jobID,
		Kind:           kind,
		Subject:        subject,
		Body:           body,
		ManifestDigest: &manifestDigest,
		AssocID:        &assoc,
		FsforkPath:     effectivePath,
		CreatedAt:      ts,
	})
	return err
}

func loadProjectLanesTx(ctx context.Context, tx *sql.Tx, projectID string) ([]ProjectLane, error) {
	rows, err := dbquery.New(tx).ProjectLanes(ctx, projectID)
	if err != nil {
		return nil, err
	}
	lanes := []ProjectLane{}
	for _, row := range rows {
		lanes = append(lanes, ProjectLane{LaneID: row.LaneID, ProjectID: row.ProjectID, OwnerJobID: row.OwnerJobID, Role: row.Role, JobState: row.State, BaseCommit: row.BaseCommit, FsforkDriver: row.FsforkDriver, FsforkPath: row.FsforkPath, SealedManifestDigest: nullableSQLString(row.SealedManifestDigest), CreatedAt: row.CreatedAt, SealedAt: nullableSQLString(row.SealedAt)})
	}
	return lanes, nil
}

func integratorUnreadCountTx(ctx context.Context, tx *sql.Tx, projectID string) (int, error) {
	count, err := dbquery.New(tx).IntegratorUnreadCount(ctx, dbquery.IntegratorUnreadCountParams{ProjectID: projectID, Recipient: "integrator:" + projectID})
	return int(count), err
}

// Lanes returns stored lane ownership without inspecting live resources.
func (r *Reader) Lanes(projectID string) (_ ProjectLanesOutput, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeDatabaseRead, resultErr) }()
	var out ProjectLanesOutput
	if !isValidProjectID(projectID) {
		return out, faults.New(faults.CodeValidationFailed, "project lanes requires a valid PROJECT ID (proj-...)")
	}
	ctx := context.Background()
	tx, err := r.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	queries := dbquery.New(tx)
	if _, err := queries.ProjectByID(ctx, projectID); errors.Is(err, sql.ErrNoRows) {
		return out, faults.Wrap(faults.CodeNotFound, fmt.Sprintf("project %s does not exist", projectID), err)
	} else if err != nil {
		return out, err
	}
	lanes, err := loadProjectLanesTx(ctx, tx, projectID)
	if err != nil {
		return out, err
	}
	return ProjectLanesOutput{
		ProjectID: projectID,
		ReadAt:    now(),
		Freshness: "stored_db_snapshot",
		Lanes:     lanes,
	}, nil
}

// Inbox returns a non-consuming, recipient-scoped page and unread count.
func (r *Reader) Inbox(projectID, recipient string, unreadOnly bool, afterSeq int64, limit int) (_ ProjectInboxOutput, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeDatabaseRead, resultErr) }()
	var out ProjectInboxOutput
	if !isValidProjectID(projectID) {
		return out, faults.New(faults.CodeValidationFailed, "project inbox requires a valid PROJECT ID (proj-...)")
	}
	if afterSeq < 0 || limit < 1 || limit > 100 {
		return out, faults.New(faults.CodeValidationFailed, "project inbox requires --after-sequence >= 0 and --limit between 1 and 100")
	}
	if recipient == "" {
		recipient = "integrator:" + projectID
	}
	if recipient != "all" {
		if err := validateInboxRecipient(recipient, projectID); err != nil {
			return out, err
		}
	}
	ctx := context.Background()
	tx, err := r.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	queries := dbquery.New(tx)
	if _, err := queries.ProjectByID(ctx, projectID); errors.Is(err, sql.ErrNoRows) {
		return out, faults.Wrap(faults.CodeNotFound, fmt.Sprintf("project %s does not exist", projectID), err)
	} else if err != nil {
		return out, err
	}
	if strings.HasPrefix(recipient, "lane:") {
		if _, err := queries.LaneByID(ctx, dbquery.LaneByIDParams{ProjectID: projectID, LaneID: strings.TrimPrefix(recipient, "lane:")}); errors.Is(err, sql.ErrNoRows) {
			return out, faults.Wrap(faults.CodeNotFound, fmt.Sprintf("lane %s does not exist in project %s", strings.TrimPrefix(recipient, "lane:"), projectID), err)
		} else if err != nil {
			return out, err
		}
	}
	params := dbquery.InboxPageParams{ProjectID: projectID, AfterSequence: afterSeq, Recipient: recipient, PageLimit: int64(limit)}
	var rows []dbquery.InboxPageRow
	if unreadOnly {
		unreadRows, err := queries.UnreadInboxPage(ctx, dbquery.UnreadInboxPageParams(params))
		if err != nil {
			return out, err
		}
		for _, row := range unreadRows {
			rows = append(rows, dbquery.InboxPageRow(row))
		}
	} else {
		rows, err = queries.InboxPage(ctx, params)
		if err != nil {
			return out, err
		}
	}
	messages := []ProjectInboxMessage{}
	nextSequence := afterSeq
	for _, row := range rows {
		m := inboxMessageFromRow(row)
		messages = append(messages, m)
		nextSequence = m.Sequence
	}
	unread, err := queries.InboxUnreadCount(ctx, dbquery.InboxUnreadCountParams{ProjectID: projectID, Recipient: recipient})
	if err != nil {
		return out, err
	}
	return ProjectInboxOutput{
		ProjectID:     projectID,
		Recipient:     recipient,
		AfterSequence: afterSeq,
		NextSequence:  nextSequence,
		UnreadCount:   int(unread),
		ReadAt:        now(),
		Freshness:     "stored_db_snapshot",
		Messages:      messages,
	}, nil
}

// MailRequest is an explicit lane assignment or blocked-work message.
type MailRequest struct {
	ProjectID, Key, From, To, LaneID, JobID, Kind, Subject, Body string
	Manifest, ReplyTo, FsforkPath                                string
}

// SendMail retains an immutable, idempotent message in the project inbox.
func (q *Queue) SendMail(in MailRequest) (_ ProjectInboxMessage, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeDatabaseWrite, resultErr) }()
	var committed ProjectInboxMessage
	projectID := in.ProjectID
	key, from, to, lane, job := in.Key, in.From, in.To, in.LaneID, in.JobID
	kind, subject, body := in.Kind, in.Subject, in.Body
	manifest, replyTo, fsforkPath := in.Manifest, in.ReplyTo, in.FsforkPath
	if !isValidProjectID(projectID) || !isValidLaneID(lane) || !isValidJobID(job) {
		return committed, faults.New(faults.CodeValidationFailed, "project mail-send requires --project PROJECT --lane LANE --job JOB --key KEY --from SENDER --to RECIPIENT --kind KIND --subject TEXT --body TEXT")
	}
	if !isValidInboxKey(key) || !isValidInboxKind(kind) || subject == "" || len(subject) > 512 || !utf8.ValidString(subject) {
		return committed, faults.New(faults.CodeValidationFailed, "project mail-send requires a valid --key, --kind, and --subject (<=512 UTF-8 bytes)")
	}
	if kind != "lane.blocked" && kind != "lane.assigned" {
		return committed, faults.New(faults.CodeValidationFailed, "project mail-send supports explicit lane.blocked or lane.assigned messages; work.submitted and review.submitted are delivered automatically with result associations")
	}
	switch kind {
	case "lane.blocked":
		if !strings.HasPrefix(from, "lane:") && !strings.HasPrefix(from, "job:") {
			return committed, faults.New(faults.CodeValidationFailed, "lane.blocked must be sent by lane:<lane_id> or job:<job_id>")
		}
		if to != "integrator:"+projectID {
			return committed, faults.New(faults.CodeValidationFailed, "lane.blocked must be addressed to integrator:<project_id>")
		}
	case "lane.assigned":
		if !strings.HasPrefix(from, "integrator:") {
			return committed, faults.New(faults.CodeValidationFailed, "lane.assigned must be sent by integrator:<id>")
		}
		if !strings.HasPrefix(to, "lane:") {
			return committed, faults.New(faults.CodeValidationFailed, "lane.assigned must be addressed to lane:<lane_id>")
		}
	}
	if manifest != "" && !isValidHexLen(manifest, 64) {
		return committed, faults.New(faults.CodeValidationFailed, "project mail-send --manifest must be a 64-hex SHA-256 digest")
	}
	if replyTo != "" && !isValidInboxMessageID(replyTo) {
		return committed, faults.New(faults.CodeValidationFailed, "project mail-send --reply-to must be a valid msg-... ID")
	}
	if err := validateInboxSender(from, lane, job); err != nil {
		return committed, err
	}
	if err := validateInboxRecipient(to, projectID); err != nil {
		return committed, err
	}

	ctx := context.Background()
	err := q.store.write(func(c *sql.Conn) error {
		queries := dbquery.New(c)
		owner, err := queries.LaneOwnerAndPath(ctx, dbquery.LaneOwnerAndPathParams{ProjectID: projectID, LaneID: lane})
		if errors.Is(err, sql.ErrNoRows) {
			return faults.Wrap(faults.CodeNotFound, fmt.Sprintf("lane %s does not exist in project %s", lane, projectID), err)
		}
		if err != nil {
			return classifyDiagnostic(faults.CodeDatabaseRead, err)
		}
		if owner.OwnerJobID != job {
			return faults.New(faults.CodeValidationFailed, fmt.Sprintf("lane %s is owned by job %s, not %s", lane, owner.OwnerJobID, job))
		}
		if strings.HasPrefix(to, "lane:") {
			if _, err := queries.LaneByID(ctx, dbquery.LaneByIDParams{ProjectID: projectID, LaneID: strings.TrimPrefix(to, "lane:")}); errors.Is(err, sql.ErrNoRows) {
				return faults.Wrap(faults.CodeNotFound, fmt.Sprintf("recipient lane %s does not exist in project %s", strings.TrimPrefix(to, "lane:"), projectID), err)
			} else if err != nil {
				return classifyDiagnostic(faults.CodeDatabaseRead, err)
			}
		}
		var manifestPtr, assocPtr, replyPtr *string
		if manifest != "" {
			if storedProject, err := queries.ManifestProject(ctx, manifest); errors.Is(err, sql.ErrNoRows) {
				return faults.Wrap(faults.CodeNotFound, fmt.Sprintf("manifest %s does not exist", manifest), err)
			} else if err != nil {
				return classifyDiagnostic(faults.CodeDatabaseRead, err)
			} else if storedProject != projectID {
				return faults.New(faults.CodeValidationFailed, fmt.Sprintf("manifest %s does not belong to project %s", manifest, projectID))
			}
			v := manifest
			manifestPtr = &v
		}
		if replyTo != "" {
			if replyProject, err := queries.InboxMessageProject(ctx, replyTo); errors.Is(err, sql.ErrNoRows) {
				return faults.Wrap(faults.CodeNotFound, fmt.Sprintf("reply-to message %s does not exist", replyTo), err)
			} else if err != nil {
				return classifyDiagnostic(faults.CodeDatabaseRead, err)
			} else if replyProject != projectID {
				return faults.New(faults.CodeValidationFailed, fmt.Sprintf("reply-to message %s does not belong to project %s", replyTo, projectID))
			}
			v := replyTo
			replyPtr = &v
		}
		if fsforkPath == "" {
			fsforkPath = owner.FsforkPath
		}
		msg, err := insertInboxMessageConn(ctx, c, ProjectInboxMessage{
			Key: key, ProjectID: projectID, Sender: from, Recipient: to, LaneID: lane, JobID: job,
			Kind: kind, Subject: subject, Body: body, ManifestDigest: manifestPtr, AssocID: assocPtr,
			FsforkPath: fsforkPath, ReplyToID: replyPtr, CreatedAt: now(),
		})
		if err != nil {
			return err
		}
		committed = msg
		return nil
	})
	if err != nil {
		return committed, err
	}
	return committed, nil
}

// AcknowledgeMail marks a retained inbox message read and optionally archived.
func (q *Queue) AcknowledgeMail(projectID, messageID string, archive bool) (_ ProjectInboxMessage, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeDatabaseWrite, resultErr) }()
	var committed ProjectInboxMessage
	if !isValidProjectID(projectID) || !isValidInboxMessageID(messageID) {
		return committed, faults.New(faults.CodeValidationFailed, "project mail-ack requires a valid PROJECT (proj-...) and MSG_ID (msg-...)")
	}
	ctx := context.Background()
	err := q.store.write(func(c *sql.Conn) error {
		ts := now()
		queries := dbquery.New(c)
		n, err := queries.MarkInboxMessage(ctx, dbquery.MarkInboxMessageParams{ReadTime: sql.NullString{String: ts, Valid: true}, Archive: int64(boolInt(archive)), ArchiveTime: sql.NullString{String: ts, Valid: true}, ProjectID: projectID, ID: messageID})
		if err != nil {
			return err
		}
		if n == 0 {
			return faults.New(faults.CodeNotFound, fmt.Sprintf("message %s does not exist in project %s", messageID, projectID))
		}
		row, err := queries.InboxMessageByID(ctx, dbquery.InboxMessageByIDParams{ProjectID: projectID, ID: messageID})
		if err != nil {
			return classifyDiagnostic(faults.CodeDatabaseRead, err)
		}
		committed = inboxMessageFromRow(dbquery.InboxPageRow(row))
		return nil
	})
	if err != nil {
		return committed, err
	}
	return committed, nil
}

// MailAcknowledger is a narrowly scoped writer for an existing schema-5 queue.
// It cannot initialize a missing queue or run migrations.
type MailAcknowledger struct{ store *store }

// OpenExistingMailAcknowledger opens an existing private writable queue without
// creating state or migrating it. The schema lock and database must exist.
func OpenExistingMailAcknowledger(stateDir string) (_ *MailAcknowledger, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeRuntimeFailed, resultErr) }()
	s, err := openExistingWritableStore(stateDir)
	if err != nil {
		return nil, err
	}
	return &MailAcknowledger{store: s}, nil
}

func (a *MailAcknowledger) Close() error {
	return classifyDiagnostic(faults.CodeDatabaseWrite, a.store.database.Close())
}

// Acknowledge verifies the exact retained project/message/sequence and, for
// archive, requires an already-read message in the same write transaction.
// Repeating an acknowledgement is idempotent and does not rewrite timestamps.
func (a *MailAcknowledger) Acknowledge(projectID, messageID string, sequence int64, archive bool) (_ ProjectInboxMessage, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeDatabaseWrite, resultErr) }()
	var committed ProjectInboxMessage
	if !isValidProjectID(projectID) || !isValidInboxMessageID(messageID) || sequence <= 0 {
		return committed, faults.New(faults.CodeValidationFailed, "mail acknowledgement requires valid project, message ID and positive exact sequence")
	}
	ctx := context.Background()
	err := a.store.write(func(c *sql.Conn) error {
		// A replacement connection must not permit an older or future schema.
		var version int
		if err := c.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
			return classifyDiagnostic(faults.CodeDatabaseRead, err)
		}
		if version != 5 {
			return faults.New(faults.CodeConfigInvalid, fmt.Sprintf("existing writable store requires schema version 5, got %d", version))
		}
		queries := dbquery.New(c)
		row, err := queries.InboxMessageByID(ctx, dbquery.InboxMessageByIDParams{ProjectID: projectID, ID: messageID})
		if errors.Is(err, sql.ErrNoRows) || (err == nil && row.Sequence != sequence) {
			return faults.New(faults.CodeNotFound, "exact inbox message not found")
		}
		if err != nil {
			return classifyDiagnostic(faults.CodeDatabaseRead, err)
		}
		if archive && !row.ReadAt.Valid {
			return faults.New(faults.CodeValidationFailed, "mark the message read before archiving")
		}
		ts := now()
		count, err := queries.MarkInboxMessage(ctx, dbquery.MarkInboxMessageParams{ReadTime: sql.NullString{String: ts, Valid: true}, Archive: int64(boolInt(archive)), ArchiveTime: sql.NullString{String: ts, Valid: true}, ProjectID: projectID, ID: messageID})
		if err != nil {
			return err
		}
		if count != 1 {
			return faults.New(faults.CodeInvariantFailed, "exact inbox message disappeared")
		}
		row, err = queries.InboxMessageByID(ctx, dbquery.InboxMessageByIDParams{ProjectID: projectID, ID: messageID})
		if err != nil {
			return classifyDiagnostic(faults.CodeDatabaseRead, err)
		}
		committed = inboxMessageFromRow(dbquery.InboxPageRow(row))
		return nil
	})
	return committed, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
