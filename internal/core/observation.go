package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ZanzyTHEbar/faults-go"
	"ocq/internal/core/dbquery"
)

const ObservationProtocol = "ocq-observe-v1"
const ObservationRequestBytes = 16 << 10
const ObservationResponseBytes = 1 << 20
const observationPageSize = 20

// Cursor is the displayed page START, never the next cursor returned by a read.
// Projects/lanes use offsets; all other pages use stable sequence cursors.
type ObservationSelector struct {
	Slot      string `json:"slot"`
	Kind      string `json:"kind"`
	ProjectID string `json:"project_id,omitempty"`
	JobID     string `json:"job_id,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	EventID   string `json:"event_id,omitempty"`
	Digest    string `json:"digest,omitempty"`
	Sequence  string `json:"sequence,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
}

type ObservationRequest struct {
	Protocol      string                 `json:"protocol"`
	QueueIdentity string                 `json:"queue_identity,omitempty"`
	StoreContext  string                 `json:"store_context,omitempty"`
	BridgeContext string                 `json:"bridge_context,omitempty"`
	Selectors     []ObservationSelector  `json:"selectors"`
	Log           *ObservationLogCursor  `json:"log,omitempty"`
	Work          *WorkObservationCursor `json:"work,omitempty"`
}

// Data is deliberately NOT serializable: callers must explicitly project each
// kind through a safe allowlist (some core views contain private execution data).
type ObservationSlot struct {
	Slot    string `json:"slot"`
	Kind    string `json:"kind"`
	Status  string `json:"status"`
	Cursor  string `json:"cursor,omitempty"`
	HasMore bool   `json:"has_more"`
	Data    any    `json:"-"`
}

type ObservationSnapshot struct {
	Protocol      string                `json:"protocol"`
	Status        string                `json:"status"`
	QueueIdentity string                `json:"queue_identity"`
	StoreContext  string                `json:"store_context"`
	BaseContext   string                `json:"-"`
	Schema        int                   `json:"schema"`
	Revision      string                `json:"revision"`
	ReadAt        string                `json:"read_at"`
	Consistency   string                `json:"consistency"`
	Slots         []ObservationSlot     `json:"slots"`
	Log           *ObservationLogOutput `json:"log,omitempty"`
	Work          *WorkList             `json:"work,omitempty"`
	Main          *MainList             `json:"main,omitempty"`
	Clients       []ClientStatus        `json:"clients,omitempty"`
}

func observationDecimal(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 || strconv.FormatInt(n, 10) != s {
		return 0, faults.New(faults.CodeValidationFailed, "expected canonical nonnegative int64 decimal string")
	}
	return n, nil
}

func DecodeObservationRequest(raw []byte) (ObservationRequest, error) {
	var req ObservationRequest
	if _, err := decodeStrictJSONObject(raw, ObservationRequestBytes, &req); err != nil {
		return req, err
	}
	return req, req.validate()
}

func (req ObservationRequest) validate() error {
	if req.Protocol != ObservationProtocol || (len(req.Selectors) < 1 && req.Log == nil && req.Work == nil) || len(req.Selectors) > 8 {
		return faults.New(faults.CodeValidationFailed, "observation requires ocq-observe-v1 and 1..8 selectors")
	}
	if req.QueueIdentity != "" && !isValidHexLen(req.QueueIdentity, 64) {
		return faults.New(faults.CodeValidationFailed, "invalid observation queue identity")
	}
	if req.QueueIdentity != "" && !isValidHexLen(req.StoreContext, 64) {
		return faults.New(faults.CodeValidationFailed, "pinned snapshots require store_context")
	}
	for _, c := range []string{req.StoreContext, req.BridgeContext} {
		if c != "" && !isValidHexLen(c, 64) {
			return faults.New(faults.CodeValidationFailed, "invalid observation context")
		}
	}
	if req.Log != nil {
		if err := req.Log.validate(); err != nil {
			return err
		}
	}
	if req.Work != nil {
		if err := req.Work.validate(false); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, s := range req.Selectors {
		if s.Slot == "" || len(s.Slot) > 64 || seen[s.Slot] {
			return faults.New(faults.CodeValidationFailed, "invalid or duplicate observation slot")
		}
		seen[s.Slot] = true
		// Reject irrelevant fields as well as unknown kinds; selectors are exact.
		want := ObservationSelector{Slot: s.Slot, Kind: s.Kind}
		switch s.Kind {
		case "projects", "queue-jobs", "notifications":
			want.Cursor = s.Cursor
		case "board":
			want.ProjectID = s.ProjectID
		case "lanes", "inbox", "results":
			want.ProjectID, want.Cursor = s.ProjectID, s.Cursor
		case "job":
			want.ProjectID, want.JobID = s.ProjectID, s.JobID
		case "queue-job":
			want.JobID = s.JobID
		case "mail":
			want.ProjectID, want.MessageID, want.Sequence = s.ProjectID, s.MessageID, s.Sequence
		case "manifest":
			want.ProjectID, want.Digest = s.ProjectID, s.Digest
		case "notification":
			want.EventID = s.EventID
		default:
			return faults.New(faults.CodeValidationFailed, "unsupported observation kind")
		}
		if want != s {
			return faults.New(faults.CodeValidationFailed, "irrelevant observation selector fields")
		}
		if s.ProjectID != "" && !isValidProjectID(s.ProjectID) {
			return faults.New(faults.CodeValidationFailed, "invalid observation project")
		}
		if (s.Kind == "board" || s.Kind == "lanes" || s.Kind == "inbox" || s.Kind == "results" || s.Kind == "job" || s.Kind == "mail" || s.Kind == "manifest") && !isValidProjectID(s.ProjectID) {
			return faults.New(faults.CodeValidationFailed, "missing observation project")
		}
		if (s.Kind == "job" || s.Kind == "queue-job") && !isValidJobID(s.JobID) {
			return faults.New(faults.CodeValidationFailed, "invalid observation job")
		}
		if s.Kind == "mail" {
			n, e := observationDecimal(s.Sequence)
			if e != nil || n == 0 || !isValidInboxMessageID(s.MessageID) {
				return faults.New(faults.CodeValidationFailed, "invalid exact mail selector")
			}
		}
		if s.Kind == "manifest" && !isValidHexLen(s.Digest, 64) {
			return faults.New(faults.CodeValidationFailed, "invalid observation manifest")
		}
		if s.Kind == "notification" && (len(s.EventID) > 128 || s.EventID == "") {
			return faults.New(faults.CodeValidationFailed, "invalid observation event")
		}
		switch s.Kind {
		case "projects", "queue-jobs", "notifications", "lanes", "inbox", "results":
			if _, e := observationDecimal(s.Cursor); e != nil {
				return e
			}
		}
	}
	return nil
}

func validateObservationSchema(ctx context.Context, q sqlQueryer) error {
	queries := dbquery.New(q)
	if _, err := queries.ObservationRevision(ctx); err != nil {
		return err
	}
	count, err := queries.ObservationTriggerCount(ctx)
	if err != nil {
		return err
	}
	if count != 20 {
		return faults.New(faults.CodeInvariantFailed, "incomplete observation schema")
	}
	objects, err := queries.ObservationSchemaObjects(ctx)
	if err != nil {
		return err
	}
	// Adoption of an already-versioned schema must validate the actual retained
	// DDL, not recreate objects or manufacture a successful migration ledger.
	expected := map[string]string{}
	for _, part := range strings.Split(observationSchema, "CREATE ")[1:] {
		end := strings.Index(part, ";")
		if strings.HasPrefix(part, "TRIGGER ") {
			end = strings.Index(part, " END;")
			if end >= 0 {
				end += 4
			}
		}
		if end < 0 {
			return faults.New(faults.CodeInvariantFailed, "invalid embedded observation DDL")
		}
		ddl := "CREATE " + part[:end]
		fields := strings.Fields(ddl)
		expected[fields[2]] = strings.Join(fields, " ")
	}
	if len(objects) != len(expected) {
		return faults.New(faults.CodeInvariantFailed, "observation schema objects differ")
	}
	for _, object := range objects {
		if strings.Join(strings.Fields(object.Definition), " ") != expected[object.Name] {
			return faults.New(faults.CodeInvariantFailed, "observation schema definition differs")
		}
	}
	return nil
}

// ObserveSnapshot never initializes, locks, migrates, acknowledges or performs
// live execution observations. All selected database projections share one short
// deferred read transaction. No wrapper that owns a transaction is called here.
func ObserveSnapshot(ctx context.Context, dir string, req ObservationRequest) (ObservationSnapshot, error) {
	if err := req.validate(); err != nil {
		return ObservationSnapshot{}, err
	}
	return observeSnapshot(ctx, dir, req)
}

// ObserveHead reads only validated identity/schema/revision metadata, never
// request bodies or execution ownership. It is also initial discovery for a
// missing queue. It performs no initialization and no migration.
func ObserveHead(ctx context.Context, dir string) (ObservationSnapshot, error) {
	return observeSnapshot(ctx, dir, ObservationRequest{})
}

func observationContexts(dir string, info os.FileInfo, identity string, schema int) (string, string) {
	var device, inode uint64
	if info != nil {
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			device = uint64(st.Dev)
			inode = st.Ino
		}
	}
	b, _ := json.Marshal([]any{dir, device, inode, identity, os.Getuid()})
	base := sha256Hex(b)
	var directoryDevice, directoryInode uint64
	if current, err := os.Lstat(dir); err == nil {
		if st, ok := current.Sys().(*syscall.Stat_t); ok {
			directoryDevice, directoryInode = uint64(st.Dev), st.Ino
		}
	}
	b, _ = json.Marshal([]any{ObservationProtocol, base, schema, directoryDevice, directoryInode})
	return base, sha256Hex(b)
}

func observeSnapshot(ctx context.Context, dir string, req ObservationRequest) (result ObservationSnapshot, resultErr error) {
	out := ObservationSnapshot{Protocol: ObservationProtocol, Status: "unavailable", Slots: []ObservationSlot{}}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			resultErr = ctx.Err()
		}
	}()
	dir, err := resolveStateDir(dir)
	if err != nil {
		return out, err
	}
	out.BaseContext, out.StoreContext = observationContexts(dir, nil, "", 0)
	before, err := os.Lstat(filepath.Join(dir, "queue.db"))
	if err != nil {
		return out, nil
	}
	s, err := openObservationStoreContext(ctx, dir)
	if err != nil {
		return out, nil
	}
	defer s.database.Close()
	identity, err := notificationQueueIdentity(s)
	if err != nil {
		return out, nil
	}
	current, err := os.Lstat(filepath.Join(s.dir, "queue.db"))
	if err != nil || !os.SameFile(before, current) {
		out.Status = "reset"
		return out, nil
	}
	out.QueueIdentity = identity
	if req.QueueIdentity != "" && req.QueueIdentity != identity {
		out.Status = "reset"
		return out, nil
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, nil
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&out.Schema); err != nil {
		return out, nil
	}
	out.BaseContext, out.StoreContext = observationContexts(s.dir, current, identity, out.Schema)
	if req.StoreContext != "" && req.StoreContext != out.StoreContext {
		out.Status = "reset"
		return out, nil
	}
	if out.Schema != 6 {
		if out.Schema == 5 {
			out.Status = "upgrade_required"
		}
		return out, nil
	}
	if err := validateObservationSchema(ctx, tx); err != nil {
		return out, nil
	}
	// This first table read establishes the SQLite snapshot before ANY view.
	revision, err := dbquery.New(tx).ObservationRevision(ctx)
	if err != nil || revision < 0 {
		return out, nil
	}
	out.Revision = strconv.FormatInt(revision, 10)
	out.ReadAt = now()
	for _, selector := range req.Selectors {
		slot, err := observeSlot(ctx, s, tx, selector, out.ReadAt)
		if err != nil {
			return ObservationSnapshot{Protocol: ObservationProtocol, Status: "unavailable", Slots: []ObservationSlot{}}, nil
		}
		out.Slots = append(out.Slots, slot)
	}
	if err := tx.Commit(); err != nil {
		return ObservationSnapshot{Protocol: ObservationProtocol, Status: "unavailable"}, nil
	}
	// Filesystem output is explicitly separate from the completed SQLite read.
	if req.Log != nil {
		log := observeLog(ctx, s, *req.Log, true)
		out.Log = &log
	}
	if req.Work != nil {
		work, err := ListProjectWork(s.dir, req.Work.ProjectID)
		if err != nil {
			return out, err
		}
		out.Work = &work
		main, err := ListProjectMain(s.dir, req.Work.ProjectID)
		if err != nil {
			return out, err
		}
		out.Main = &main
		clients, err := ListProjectClients(s.dir, req.Work.ProjectID)
		if err != nil {
			return out, err
		}
		out.Clients = clients
	}
	after, err := notificationQueueIdentity(s)
	if err != nil || after != identity {
		return ObservationSnapshot{Protocol: ObservationProtocol, Status: "reset"}, nil
	}
	out.Status = "ready"
	out.Consistency = "single_sqlite_snapshot"
	return out, nil
}

// Until large legacy detail views gain independently paged dimensions, refuse
// them BEFORE loading any rows. This is explicit limit state, not truncation
// presented as a complete board. Existing unpaged CLI contracts are unchanged.
func observationDetailBound(ctx context.Context, q sqlQueryer, project string) (bool, error) {
	oversized, err := dbquery.New(q).ObservationDetailOversized(ctx, project)
	return oversized == 0, err
}

func observeSlot(ctx context.Context, s *store, q sqlQueryer, sel ObservationSelector, readAt string) (ObservationSlot, error) {
	out := ObservationSlot{Slot: sel.Slot, Kind: sel.Kind, Status: "ready"}
	after, _ := observationDecimal(sel.Cursor)
	queries := dbquery.New(q)
	if sel.ProjectID != "" {
		_, err := queries.ProjectByID(ctx, sel.ProjectID)
		if errors.Is(err, sql.ErrNoRows) {
			out.Status = "not_found"
			return out, nil
		}
		if err != nil {
			return out, err
		}
	}
	if sel.Kind == "board" || sel.Kind == "job" || sel.Kind == "results" {
		ok, err := observationDetailBound(ctx, q, sel.ProjectID)
		if err != nil {
			return out, err
		}
		if !ok {
			out.Status = "limit_exceeded"
			return out, nil
		}
	}
	var err error
	switch sel.Kind {
	case "projects":
		out.Data, out.Cursor, out.HasMore, err = observationProjects(ctx, q, after)
	case "queue-jobs":
		rows, e := queries.QueueJobsPage(ctx, dbquery.QueueJobsPageParams{Sequence: after, Limit: 21})
		err = e
		items := []QueueJobSummary{}
		out.HasMore = len(rows) > 20
		if out.HasMore {
			rows = rows[:20]
		}
		out.Cursor = sel.Cursor
		for _, r := range rows {
			item := queueSummary(dbquery.QueueJobByIDRow(r))
			items = append(items, item)
			out.Cursor = item.Sequence
		}
		out.Data = QueueJobsOutput{ReadAt: readAt, Items: items, Cursor: out.Cursor, HasMore: out.HasMore}
	case "queue-job":
		var row dbquery.QueueJobByIDRow
		row, err = queries.QueueJobByID(ctx, sel.JobID)
		out.Data = queueSummary(row)
	case "notifications":
		// Pending sequence paging is non-consuming; exact lookup also retains
		// acknowledged events.
		out.Data, out.Cursor, out.HasMore, err = observationNotifications(ctx, q, after)
	case "notification":
		var row dbquery.NotificationByIDRow
		row, err = queries.NotificationByID(ctx, sel.EventID)
		out.Data = queueNotification(notificationFromRow(row))
	case "board":
		var show ProjectShowOutput
		show, err = s.projectShowQuery(ctx, q, sel.ProjectID)
		if err != nil {
			break
		}
		show.ReadAt = readAt
		var rows []dbquery.BoardUnreadMessagesRow
		rows, err = queries.BoardUnreadMessages(ctx, dbquery.BoardUnreadMessagesParams{ProjectID: sel.ProjectID, Recipient: "integrator:" + sel.ProjectID})
		messages := []ProjectInboxMessage{}
		for _, r := range rows {
			messages = append(messages, inboxMessageFromRow(dbquery.InboxPageRow(r)))
		}
		out.HasMore = show.IntegratorUnreadMailCount > len(messages)
		out.Data = Board{Project: show, MailReadAt: readAt, Messages: messages}
	case "job":
		var data ProjectJobDetailOutput
		data, err = s.projectJobQuery(ctx, q, sel.ProjectID, sel.JobID)
		data.ReadAt = readAt
		out.Data = data
	case "results":
		var data ProjectResultsOutput
		data, err = s.projectResultsQuery(ctx, q, sel.ProjectID, after, 21, false)
		data.ReadAt = readAt
		out.HasMore = len(data.Items) > 20
		if out.HasMore {
			data.Items = data.Items[:20]
			data.NextSequence = data.Items[19].Event.Sequence
		}
		out.Cursor = strconv.FormatInt(data.NextSequence, 10)
		out.Data = data
	case "inbox":
		var data ProjectInboxOutput
		data, err = inboxQuery(ctx, q, sel.ProjectID, "all", false, after, 21, false)
		data.ReadAt = readAt
		out.HasMore = len(data.Messages) > 20
		if out.HasMore {
			data.Messages = data.Messages[:20]
			data.NextSequence = data.Messages[19].Sequence
		}
		out.Cursor = strconv.FormatInt(data.NextSequence, 10)
		out.Data = data
	case "mail":
		var row dbquery.InboxMessageByIDRow
		row, err = queries.InboxMessageByID(ctx, dbquery.InboxMessageByIDParams{ProjectID: sel.ProjectID, ID: sel.MessageID})
		if err == nil && strconv.FormatInt(row.Sequence, 10) != sel.Sequence {
			err = sql.ErrNoRows
		}
		out.Data = inboxMessageFromRow(dbquery.InboxPageRow(row))
	case "lanes":
		out.Data, out.Cursor, out.HasMore, err = observationLanes(ctx, q, sel.ProjectID, after)
	case "manifest":
		var size int64
		size, err = queries.ObservationManifestSize(ctx, dbquery.ObservationManifestSizeParams{ProjectID: sel.ProjectID, Digest: sel.Digest})
		if err == nil && size > 65536 {
			out.Status = "limit_exceeded"
			return out, nil
		}
		if err == nil {
			var raw []byte
			raw, err = projectManifestQuery(ctx, q, sel.ProjectID, sel.Digest)
			out.Data = string(raw)
		}
	}
	if errors.Is(err, sql.ErrNoRows) || faults.IsCode(err, faults.CodeNotFound) {
		out.Status = "not_found"
		out.Data = nil
		err = nil
	}
	return out, err
}

func observationProjects(ctx context.Context, q sqlQueryer, offset int64) (any, string, bool, error) {
	rows, err := dbquery.New(q).ObservationProjectsPage(ctx, offset)
	if err != nil {
		return nil, "", false, err
	}
	items := []ProjectSummary{}
	for _, r := range rows {
		items = append(items, ProjectSummary{ProjectID: r.ID, Label: r.Label, BindingRevision: int(r.BindingRevision), RepoRoot: r.RepoRoot, IntegratorUnreadMailCount: int(r.UnreadCount)})
	}
	more := len(items) > 20
	if more {
		items = items[:20]
	}
	// A huge empty offset stays unchanged; do not overflow int64.
	next := offset
	if len(items) > 0 && offset <= int64(^uint64(0)>>1)-int64(len(items)) {
		next += int64(len(items))
	}
	return items, strconv.FormatInt(next, 10), more, nil
}

func observationLanes(ctx context.Context, q sqlQueryer, project string, offset int64) (any, string, bool, error) {
	rows, err := dbquery.New(q).ObservationLanesPage(ctx, dbquery.ObservationLanesPageParams{ProjectID: project, PageOffset: offset})
	if err != nil {
		return nil, "", false, err
	}
	items := []ProjectLane{}
	for _, r := range rows {
		items = append(items, ProjectLane{LaneID: r.LaneID, ProjectID: r.ProjectID, OwnerJobID: r.OwnerJobID, Role: r.Role, JobState: r.State, BaseCommit: r.BaseCommit, FsforkDriver: r.FsforkDriver, FsforkPath: r.FsforkPath, SealedManifestDigest: nullableSQLString(r.SealedManifestDigest), CreatedAt: r.CreatedAt, SealedAt: nullableSQLString(r.SealedAt)})
	}
	more := len(items) > 20
	if more {
		items = items[:20]
	}
	next := offset
	if len(items) > 0 && offset <= int64(^uint64(0)>>1)-int64(len(items)) {
		next += int64(len(items))
	}
	return items, strconv.FormatInt(next, 10), more, nil
}

func observationNotifications(ctx context.Context, q sqlQueryer, after int64) (any, string, bool, error) {
	rows, err := dbquery.New(q).ObservationNotificationsPage(ctx, after)
	if err != nil {
		return nil, "", false, err
	}
	items := []QueueNotification{}
	next := after
	for _, r := range rows {
		items = append(items, queueNotification(notificationFromRow(dbquery.NotificationByIDRow(r))))
	}
	more := len(items) > 20
	if more {
		items = items[:20]
	}
	if len(items) > 0 {
		next, _ = strconv.ParseInt(items[len(items)-1].Sequence, 10, 64)
	}
	return items, strconv.FormatInt(next, 10), more, nil
}
