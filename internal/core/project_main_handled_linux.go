package core

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/ZanzyTHEbar/faults-go"
)

const mainHandledTool = "ocq_main_handled"
const MainHandledInputLimit = 4096

//go:embed sql/schema8.sql
var mainHandledSchema string

func migrateMainHandledSchema(ctx context.Context, tx *sql.Tx) error {
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version != 7 {
		return fmt.Errorf("Main handled migration requires schema 7, got %d", version)
	}
	_, err := tx.ExecContext(ctx, mainHandledSchema)
	return err
}

// UpgradeProjectMainHandled is explicitly operator-initiated; neither tool
// execution nor receipt recovery can initialize, migrate or activate scheduling.
func UpgradeProjectMainHandled(dir string) error {
	s, err := openExistingWritableStore(dir)
	if err != nil {
		return err
	}
	defer s.database.Close()
	f, err := privateFile(filepath.Join(s.dir, "schema.lock"), os.O_RDWR)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	lock, err := s.lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	var version int
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version < 6 {
		return errors.New("upgrade observation schema explicitly before Main handled")
	}
	// Do not commit either additive migration if the installed observation
	// schema is already damaged. Post-migration validation is too late.
	if err = validateObservationSchema(context.Background(), s.db); err != nil {
		return err
	}
	return migrateStore(s.db, 4)
}

type MainHandledPointer struct {
	RequestID string `json:"request_id"`
	InboxID   string `json:"inbox_id"`
}

// Actor fields are forwarded by the trusted in-process server plugin, not by
// the model. They are corroborated, not accepted as authority in their own right.
type mainHandledInvocation struct {
	Pointer   MainHandledPointer `json:"pointer"`
	SessionID string             `json:"session_id"`
	MessageID string             `json:"message_id"`
	CallID    string             `json:"call_id"`
	ServerPID int                `json:"server_pid"`
}

type MainHandledReceipt struct {
	ProjectID         string `json:"project_id"`
	RequestID         string `json:"request_id"`
	IntentDigest      string `json:"intent_sha256"`
	SubmissionKey     string `json:"submission_key"`
	EnvelopeDigest    string `json:"envelope_sha256"`
	AdmissionRevision int    `json:"admission_revision"`
	JobID             string `json:"job_id"`
	LaneID            string `json:"lane_id"`
	InboxID           string `json:"inbox_id"`
	InboxSequence     int64  `json:"inbox_sequence"`
	AssociationID     string `json:"association_id"`
	ResultDigest      string `json:"result_sha256"`
	MainRevision      int    `json:"main_revision"`
	MainSetupID       string `json:"main_setup_id"`
	MainSessionID     string `json:"main_session_id"`
	MainRoleDigest    string `json:"main_role_sha256"`
	NativeMessageID   string `json:"native_message_id"`
	NativeCallID      string `json:"native_call_id"`
	RecordedAt        string `json:"recorded_at"`
}

var mainHandledNativeID = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,256}$`)

func validMainHandledActor(session, message, call string) bool {
	return strings.HasPrefix(session, "ses_") && mainHandledNativeID.MatchString(session) &&
		strings.HasPrefix(message, "msg_") && mainHandledNativeID.MatchString(message) &&
		mainHandledNativeID.MatchString(call)
}

func mainHandledSchemaReady(q sqlQueryer) (bool, error) {
	var version int
	if err := q.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&version); err != nil {
		return false, err
	}
	return version == 8, nil
}

func validateMainHandledSchema(ctx context.Context, q sqlQueryer) error {
	var count int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE
 (type='table' AND name IN ('project_main_handled','project_task_handoffs')) OR
 (type='view' AND name='project_task_handoff_status') OR
 (type='trigger' AND name IN ('project_main_handled_no_update','project_main_handled_no_delete','project_main_handled_revision',
 'project_task_handoff_identity','project_task_handoff_publish','project_task_handoff_no_delete','project_task_handoff_revision'))`).Scan(&count)
	if err == nil && count != 10 {
		return errors.New("incomplete task handoff or Main handled schema")
	}
	return err
}

const mainHandledFields = `project_id,request_id,intent_digest,submission_key,envelope_digest,
 admission_revision,job_id,lane_id,inbox_id,inbox_sequence,association_id,result_digest,main_revision,
 main_setup_id,main_session_id,main_role_digest,native_message_id,native_call_id,recorded_at`

func (r *MainHandledReceipt) columns() []any {
	return []any{&r.ProjectID, &r.RequestID, &r.IntentDigest, &r.SubmissionKey, &r.EnvelopeDigest,
		&r.AdmissionRevision, &r.JobID, &r.LaneID, &r.InboxID, &r.InboxSequence, &r.AssociationID, &r.ResultDigest,
		&r.MainRevision, &r.MainSetupID, &r.MainSessionID, &r.MainRoleDigest, &r.NativeMessageID, &r.NativeCallID, &r.RecordedAt}
}

func mainHandledReceiptFrom(q sqlQueryer, project, request string) (r MainHandledReceipt, err error) {
	ready, err := mainHandledSchemaReady(q)
	if err != nil {
		return r, err
	}
	if !ready {
		return r, sql.ErrNoRows
	}
	err = q.QueryRowContext(context.Background(), `SELECT `+mainHandledFields+
		` FROM project_main_handled WHERE project_id=? AND request_id=?`, project, request).Scan(r.columns()...)
	return r, err
}

func (r MainHandledReceipt) matches(in mainHandledInvocation) bool {
	return r.RequestID == in.Pointer.RequestID && r.InboxID == in.Pointer.InboxID &&
		r.MainSessionID == in.SessionID && r.NativeMessageID == in.MessageID && r.NativeCallID == in.CallID
}

// ReadProjectMainHandled is read-back only, including after replacement of
// Main or loss of the server response. Absence never authorizes a resend.
func ReadProjectMainHandled(dir, project string, pointer MainHandledPointer, session, message, call string) (out MainHandledReceipt, err error) {
	if !isValidProjectID(project) || !workID(pointer.RequestID) || !isValidInboxMessageID(pointer.InboxID) ||
		!validMainHandledActor(session, message, call) {
		return out, errors.New("invalid Main handled receipt identity")
	}
	r, err := OpenReader(dir)
	if err != nil {
		return out, err
	}
	defer r.Close()
	err = r.store.readSchema5(func(q sqlQueryer) error {
		got, err := mainHandledReceiptFrom(q, project, pointer.RequestID)
		if errors.Is(err, sql.ErrNoRows) ||
			err == nil && !got.matches(mainHandledInvocation{Pointer: pointer, SessionID: session, MessageID: message, CallID: call}) {
			return faults.New(faults.CodeNotFound, "exact Main handled receipt not observed; do not resend an uncertain invocation")
		}
		out = got
		return err
	})
	return out, err
}

// Corroboration uses the managed native service that is running the plugin,
// exact session ownership and the supported exact assistant-message GET. A
// running state is required; a historical tool part alone grants no new write.
func verifyMainHandledCall(ctx context.Context, main mainRecord, in mainHandledInvocation) error {
	service, err := discoverNativeServiceContext(ctx, main.Repo)
	if err != nil || service.PID != in.ServerPID {
		return errors.New("Main handled requires the exact locally managed OpenCode service")
	}
	session, err := nativeSessionContext(ctx, main.Repo, service, main.SessionID)
	if err != nil {
		return err
	}
	if err = nativeIdentity(session, main.SessionID, main.Repo, main.ProjectID, nativeHash(main.Metadata)); err != nil {
		return err
	}
	main.Server = service
	if agreed, _, err := observeMainRole(main); err != nil || !agreed {
		return errors.New("exact selected Main role unavailable or mismatched")
	}
	path := "/api/session/" + url.PathEscape(in.SessionID) + "/message/" + url.PathEscape(in.MessageID)
	b, err := nativeCallContext(ctx, main.Repo, service, "get", path, nil)
	if err != nil {
		return err
	}
	var m message
	if err = decodeData(b, &m); err != nil || m.ID != in.MessageID || m.Type != "assistant" {
		return errors.New("exact native assistant message unavailable or mismatched")
	}
	matches := 0
	for _, part := range m.Content {
		if part.Type != "tool" || part.ID != in.CallID {
			continue
		}
		matches++
		var pointer MainHandledPointer
		if _, err := decodeStrictJSONObject(part.State.Input, MainHandledInputLimit, &pointer); err != nil ||
			part.Name != mainHandledTool || part.State.Status != "running" ||
			part.Executed == nil || *part.Executed || pointer != in.Pointer {
			return errors.New("exact running Main handled tool call or pointer mismatched")
		}
	}
	if matches != 1 {
		return errors.New("unique exact native Main handled invocation not observed")
	}
	return nil
}

// RecordProjectMainHandled consumes a bounded engine envelope from a fixed
// opt-in helper command. No native mutation, mail ack or event consumption.
func RecordProjectMainHandled(ctx context.Context, dir, project string, raw []byte) (out MainHandledReceipt, err error) {
	var in mainHandledInvocation
	if _, err = decodeStrictJSONObject(raw, MainHandledInputLimit, &in); err != nil ||
		!filepath.IsAbs(dir) || !isValidProjectID(project) || !workID(in.Pointer.RequestID) ||
		!isValidInboxMessageID(in.Pointer.InboxID) || !validMainHandledActor(in.SessionID, in.MessageID, in.CallID) || in.ServerPID < 1 {
		return out, errors.New("invalid bounded Main handled invocation")
	}
	r, err := OpenReader(dir)
	if err != nil {
		return out, err
	}
	defer r.Close()
	if ready, err := mainHandledSchemaReady(r.store.db); err != nil || !ready {
		return out, errors.New("Main handled requires explicitly installed schema 8")
	}
	if err = validateMainHandledSchema(ctx, r.store.db); err != nil {
		return out, err
	}
	replay := func(q sqlQueryer) (bool, error) {
		got, err := mainHandledReceiptFrom(q, project, in.Pointer.RequestID)
		if err != nil {
			return false, err
		}
		if !got.matches(in) {
			return false, errors.New("Work already has a different Main handled receipt; no replacement")
		}
		out = got
		return true, nil
	}
	if found, err := replay(r.store.db); found || !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}

	j, err := openLocalJournal(dir, workJournal(project))
	if err != nil {
		return out, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return out, err
	}
	if found, err := replay(r.store.db); found || !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	main, _, selected, err := selectedMain(r, j.dir, project)
	if err != nil || main.SessionID != in.SessionID {
		return out, errors.New("invocation is not from the exact selected Main")
	}
	rec, payload, work, err := workLoad(r, j.dir, project, in.Pointer.RequestID)
	if err != nil {
		return out, err
	}
	if _, err = loadStandaloneTaskGoal(j.dir, rec, work); err != nil {
		return out, err
	}
	if rec.Binding != selected {
		return out, errors.New("current Main differs from Work admission binding; no stale Work retargeting")
	}
	parsed, err := standaloneTaskSubmission(rec, work, payload)
	if err != nil {
		return out, err
	}
	checkHandoff := func(q sqlQueryer) (taskHandoffRow, error) {
		h, err := taskHandoffFrom(ctx, q, project, in.Pointer.RequestID)
		if errors.Is(err, sql.ErrNoRows) || err == nil && (h.State != "pending_main" ||
			h.InboxID != in.Pointer.InboxID || h.intent != rec.Digest || h.lane != parsed.LaneID ||
			h.MainRevision != selected.Revision || h.MainSetupID != main.SetupID || h.MainSessionID != in.SessionID) {
			return h, errors.New("exact pending handoff for this selected Main unavailable; no replacement or retargeting")
		}
		return h, err
	}
	if _, err = checkHandoff(r.store.db); err != nil {
		return out, err
	}
	if err = verifyMainHandledCall(ctx, main, in); err != nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	s, err := openExistingWritableStore(dir)
	if err != nil {
		return out, err
	}
	defer s.database.Close()
	device, inode, err := j.queueIdentity()
	if err != nil || s.dir != rec.Queue || device != rec.Device || inode != rec.Inode {
		return out, errors.New("queue identity changed before Main handled write")
	}
	err = s.write(func(c *sql.Conn) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if found, err := replay(c); found || !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		current, err := s.currentProjectBinding(ctx, c, project)
		// Binding/manifest rows are immutable. Equality includes the handoff
		// digest selecting SetupID, revision, session and the exact checkout.
		// The project lock above keeps role selection and retirement fixed.
		if err != nil || current != selected {
			return errors.New("Main selection changed before acknowledgement; no write")
		}
		h, err := checkHandoff(c)
		if err != nil {
			return err
		}
		submission, err := s.submissionReceiptFrom(c, parsed)
		if err != nil {
			return err
		}
		job, err := s.getFrom(c, submission.JobID)
		if err != nil {
			return err
		}
		if h.job != job.ID || !taskHandoffTerminal(job, in.SessionID) {
			return errors.New("exact retained terminal proof for distinct task session unavailable")
		}
		next := MainHandledReceipt{ProjectID: project, RequestID: in.Pointer.RequestID,
			IntentDigest: rec.Digest, SubmissionKey: submission.Key, EnvelopeDigest: submission.EnvelopeDigest,
			AdmissionRevision: submission.BindingRevision, JobID: submission.JobID, LaneID: parsed.LaneID, InboxID: in.Pointer.InboxID,
			InboxSequence: h.sequence, AssociationID: h.AssociationID, ResultDigest: h.ResultDigest,
			MainRevision: current.Revision, MainSetupID: main.SetupID, MainSessionID: in.SessionID,
			MainRoleDigest: main.Role.SHA256, NativeMessageID: in.MessageID, NativeCallID: in.CallID, RecordedAt: now()}
		if _, err = c.ExecContext(ctx, `INSERT INTO project_main_handled (`+mainHandledFields+
			`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, next.columns()...); err != nil {
			return err
		}
		out = next
		return nil
	})
	if err != nil {
		return MainHandledReceipt{}, err
	}
	return out, nil
}
