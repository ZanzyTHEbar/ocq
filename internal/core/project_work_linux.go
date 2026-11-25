package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/ZanzyTHEbar/faults-go"

	"ocq/internal/core/dbquery"
)

const workIntentLimit = 128
const workFileLimit = 2048

type workRecord struct {
	Protocol   string         `json:"protocol"`
	Input      string         `json:"input_json"`
	Digest     string         `json:"input_sha256"`
	Binding    ProjectBinding `json:"binding"`
	Queue      string         `json:"queue_path"`
	Device     string         `json:"queue_device"`
	Inode      string         `json:"queue_inode"`
	Created    string         `json:"created_at"`
	BaseCommit string         `json:"base_commit,omitempty"`
}

type workPayload struct {
	Payload string `json:"payload"`
	Digest  string `json:"payload_sha256"`
}

type workClaim struct {
	Intent  string            `json:"intent_sha256"`
	Payload string            `json:"payload_sha256"`
	Preview string            `json:"preview_sha256"`
	Context WorkActionContext `json:"context"`
}

func workID(id string) bool {
	if len(id) < 1 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// Reserved structural publication context for the local Work command core, NOT
// a discovered/live Herdr space or a UI navigation target. Keep the same valid
// target shape and ownership/publication fences as other publication callers.
const workPublicationSpace = "spocqworkpublication"

func workJournal(project string) string { return "project-work-" + sha256Hex([]byte(project)) }

func workSubmissionKey(project, request string) string {
	return "work-" + sha256Hex([]byte(project+"\n"+request))
}

func decodeWork(raw []byte) (WorkPrepareRequest, error) {
	var in WorkPrepareRequest
	if _, err := decodeStrictJSONObject(raw, WorkRequestBytes, &in); err != nil {
		return in, err
	}
	if in.Protocol != WorkProtocol || !isValidProjectID(in.ProjectID) || !workID(in.RequestID) || !utf8.Valid(raw) {
		return in, errors.New("invalid ocq-work-v1 request identity")
	}
	rev, err := observationDecimal(in.BindingRevision)
	if err != nil || rev < 1 {
		return in, errors.New("explicit binding revision required")
	}
	want := WorkPrepareRequest{Protocol: in.Protocol, ProjectID: in.ProjectID, RequestID: in.RequestID, Kind: in.Kind, BindingRevision: in.BindingRevision}
	switch in.Kind {
	case "implementation":
		want.Profile, want.Brief, want.BaseRef = in.Profile, in.Brief, in.BaseRef
		if len(in.BaseRef) > 1024 || strings.ContainsAny(in.BaseRef, "\r\n\x00") {
			return in, errors.New("worker base ref must be a bounded single Git ref or commit")
		}
		if strings.TrimSpace(in.Brief) == "" || len(in.Brief) > 16<<10 {
			return in, errors.New("plain brief must be nonempty and <=16 KiB")
		}
	case "capture":
		want.ParentRequestID = in.ParentRequestID
	case "review":
		want.ParentRequestID, want.Profile, want.Criteria, want.Rules = in.ParentRequestID, in.Profile, in.Criteria, in.Rules
	case "review_result":
		want.ParentRequestID, want.ReviewResult = in.ParentRequestID, in.ReviewResult
	default:
		return in, errors.New("unsupported work operation")
	}
	if want != in || in.Kind != "implementation" && (!workID(in.ParentRequestID) || in.ParentRequestID == in.RequestID) {
		return in, errors.New("irrelevant work fields or invalid parent pointer")
	}
	if in.Kind == "implementation" || in.Kind == "review" {
		if _, err := workProfile(in.Profile); err != nil {
			return in, err
		}
	}
	return in, nil
}

func workProfile(raw string) (ProjectJobDraftProfile, error) {
	var p WorkProfile
	if _, err := decodeStrictJSONObject([]byte(raw), 16<<10, &p); err != nil {
		return ProjectJobDraftProfile{}, err
	}
	n, err := observationDecimal(p.MemoryBytes)
	if err != nil || p.Agent == "" || p.ProviderID == "" || p.ModelID == "" {
		return ProjectJobDraftProfile{}, errors.New("explicit agent/model/memory/ordered permissions required")
	}
	if !strings.HasPrefix(strings.TrimSpace(string(p.Permissions)), "[") {
		return ProjectJobDraftProfile{}, errors.New("profile permissions must be an explicit ordered array")
	}
	if _, err = parseJobDraftPermissions(p.Permissions); err != nil {
		return ProjectJobDraftProfile{}, err
	}
	return ProjectJobDraftProfile{Agent: p.Agent, ProviderID: p.ProviderID, ModelID: p.ModelID, Variant: p.Variant, MemoryBytes: &n, Permissions: p.Permissions}, nil
}

func workReadDir(r *Reader, project string) (*os.File, error) {
	fd, err := syscall.Open(r.store.dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	root := os.NewFile(uintptr(fd), r.store.dir)
	defer root.Close()
	return composeOpenPrivateDirAt(root, workJournal(project))
}

func workRead(dir *os.File, name string) ([]byte, error) {
	// Reject a nonregular journal entry without blocking on a FIFO open.
	f, err := composePrivateAt(dir, name, syscall.O_RDONLY|syscall.O_NONBLOCK)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, WorkResponseBytes+1))
	if len(b) > WorkResponseBytes {
		return nil, errors.New("work record exceeds bound")
	}
	return b, err
}

func workLoad(r *Reader, dir *os.File, project, id string) (workRecord, workPayload, WorkPrepareRequest, error) {
	var rec workRecord
	var payload workPayload
	b, err := workRead(dir, id+".started.json")
	if err != nil {
		return rec, payload, WorkPrepareRequest{}, err
	}
	if _, err = decodeStrictJSONObject(b, WorkResponseBytes, &rec); err != nil {
		return rec, payload, WorkPrepareRequest{}, err
	}
	in, err := decodeWork([]byte(rec.Input))
	if err != nil || in.ProjectID != project || in.RequestID != id || rec.Protocol != WorkProtocol || rec.Digest != sha256Hex([]byte(rec.Input)) || rec.Binding.ProjectID != project || strconv.Itoa(rec.Binding.Revision) != in.BindingRevision || rec.Queue != r.store.dir {
		return rec, payload, in, errors.New("work intent identity/content mismatch")
	}
	if in.BaseRef != "" && !isValidHexLen(rec.BaseCommit, 40) || in.BaseRef == "" && rec.BaseCommit != "" {
		return rec, payload, in, errors.New("frozen worker base unavailable")
	}
	info, err := os.Lstat(filepath.Join(r.store.dir, "queue.db"))
	if err != nil {
		return rec, payload, in, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || strconv.FormatUint(uint64(st.Dev), 10) != rec.Device || strconv.FormatUint(st.Ino, 10) != rec.Inode {
		return rec, payload, in, errors.New("work queue device/inode changed")
	}
	b, err = workRead(dir, id+".intent.json")
	if errors.Is(err, syscall.ENOENT) {
		return rec, payload, in, nil
	}
	if err != nil {
		return rec, payload, in, err
	}
	if _, err = decodeStrictJSONObject(b, WorkResponseBytes, &payload); err != nil || payload.Payload == "" || payload.Digest != sha256Hex([]byte(payload.Payload)) {
		return rec, payload, in, errors.New("work prepared payload unavailable")
	}
	return rec, payload, in, nil
}

func workTerminal(j Job) bool { return workTerminalEvidence(j, false) }

func taskHandoffTerminal(j Job, mainSession string) bool {
	return j.Agent != nil && j.Agent.SessionID != "" && j.Agent.SessionID != mainSession &&
		workTerminalEvidence(j, true)
}

func workTerminalEvidence(j Job, exactSession bool) bool {
	if j.State != "finished" || j.Agent == nil || j.Agent.Phase != "terminal" || j.Agent.TerminalEvidence == "" || j.Outcome != "agent_"+j.Agent.ServerOutcome {
		return false
	}
	if j.Agent.ServerOutcome != "succeeded" && j.Agent.ServerOutcome != "failed" && j.Agent.ServerOutcome != "interrupted" {
		return false
	}
	f, err := os.Open(j.Agent.TerminalEvidence)
	if err != nil {
		return false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 8<<20+1))
	if err != nil || len(b) > 8<<20 {
		return false
	}
	var proof struct {
		Policy  string      `json:"completion_policy"`
		Prompt  string      `json:"prompt_id"`
		Digest  string      `json:"request_digest"`
		Stable  bool        `json:"stable_transcript"`
		Active  *bool       `json:"active"`
		Session sessionInfo `json:"session"`
	}
	return json.Unmarshal(b, &proof) == nil && proof.Policy == agentCompletionPolicy && proof.Prompt == j.Agent.PromptID &&
		proof.Digest == j.Digest && proof.Stable && proof.Active != nil && !*proof.Active &&
		(!exactSession || j.agentRequest != nil && validateSession(j, proof.Session) == nil)
}

func workJob(r *Reader, id string) (*WorkJob, error) {
	j, err := r.store.get(id)
	if err != nil {
		return nil, err
	}
	return workJobView(j), nil
}

func workJobView(j Job) *WorkJob {
	out := &WorkJob{JobID: j.ID, Sequence: strconv.FormatInt(j.Sequence, 10), State: j.State, Outcome: j.Outcome, Reason: j.Reason, TerminalProven: workTerminal(j)}
	if j.Agent != nil {
		out.SessionID, out.Worktree = j.Agent.SessionID, j.Agent.Worktree
	}
	return out
}

func loadStandaloneTaskGoal(dir *os.File, rec workRecord, in WorkPrepareRequest) (TaskGoal, error) {
	var saved TaskGoal
	raw, err := workRead(dir, in.RequestID+".task.json")
	if err != nil {
		return saved, err
	}
	if _, err = decodeStrictJSONObject(raw, 4096, &saved); err != nil || in.Kind != "implementation" ||
		in.BaseRef != rec.Binding.CommitAnchor || saved != standaloneTaskGoal(rec, in) {
		return saved, errors.New("standalone task identity mismatch")
	}
	return saved, nil
}

func standaloneTaskSubmission(rec workRecord, in WorkPrepareRequest, p workPayload) (parsedProjectSubmission, error) {
	parsed, err := parseSubmissionEnvelope([]byte(p.Payload))
	if err != nil {
		return parsed, err
	}
	e := parsed.Envelope
	if e.ProjectID != in.ProjectID || e.Key != workSubmissionKey(in.ProjectID, in.RequestID) ||
		e.BindingRevision != rec.Binding.Revision || e.Role != "implementation" ||
		parsed.Request.Repo != rec.Binding.RepoRoot || parsed.Request.Base != rec.BaseCommit ||
		parsed.Request.Prompt != in.Brief {
		return parsed, errors.New("standalone task submission identity mismatch")
	}
	return parsed, nil
}

func standaloneTaskAdmission(r *Reader, rec workRecord, in WorkPrepareRequest, p workPayload, out *WorkStatus) error {
	parsed, err := standaloneTaskSubmission(rec, in, p)
	if err != nil {
		return err
	}
	var receipt ProjectSubmissionReceipt
	var job Job
	var laneID string
	err = r.store.readSchema5(func(tx sqlQueryer) error {
		var err error
		receipt, err = r.store.submissionReceiptFrom(tx, parsed)
		if faults.IsCode(err, faults.CodeNotFound) {
			return nil // No exact admission observed in this snapshot.
		}
		if err != nil {
			return err
		}
		job, err = r.store.getFrom(tx, receipt.JobID)
		if err != nil {
			return fmt.Errorf("standalone task job: %w", err)
		}
		lane, err := dbquery.New(tx).LaneForJob(context.Background(), dbquery.LaneForJobParams{ProjectID: in.ProjectID, OwnerJobID: receipt.JobID})
		if err != nil {
			return fmt.Errorf("standalone task owned lane: %w", err)
		}
		if lane.LaneID != parsed.LaneID || lane.Role != parsed.Envelope.Role || lane.BaseCommit != rec.BaseCommit ||
			lane.FsforkDriver != parsed.FsforkDriver || job.Agent == nil {
			return errors.New("standalone task job/lane ownership mismatch")
		}
		laneID = lane.LaneID
		ready, err := mainHandledSchemaReady(tx)
		if err != nil || !ready {
			return err
		}
		handoff, err := taskHandoffFrom(context.Background(), tx, in.ProjectID, in.RequestID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil // Legacy work is not retroactively enrolled.
		}
		if err != nil {
			return err
		}
		if handoff.job != job.ID || handoff.intent != rec.Digest || handoff.MainRevision != rec.Binding.Revision ||
			handoff.MainSessionID != rec.Binding.ElephantSessionID || (handoff.InboxID != "" && handoff.lane != laneID) {
			return errors.New("standalone task does not match stored handoff")
		}
		out.TaskLifecycle.Handoff = &handoff.TaskHandoff
		handled, err := mainHandledReceiptFrom(tx, in.ProjectID, in.RequestID)
		if err == nil {
			if handled.IntentDigest != rec.Digest || handled.JobID != job.ID || handled.LaneID != laneID ||
				handled.SubmissionKey != receipt.Key || handled.EnvelopeDigest != receipt.EnvelopeDigest ||
				handled.AdmissionRevision != receipt.BindingRevision || handled.InboxID != handoff.InboxID ||
				handled.AssociationID != handoff.AssociationID || handled.ResultDigest != handoff.ResultDigest {
				return errors.New("standalone task does not match stored Main handled receipt")
			}
			out.TaskLifecycle.MainHandled = "recorded"
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return nil
	})
	if err != nil || receipt.JobID == "" {
		return err
	}
	finished := job.State == "finished" || job.State == "cancelled" || job.State == "resolved"
	out.TaskLifecycle.LaneID, out.TaskLifecycle.WorkFinished = laneID, &finished
	out.State, out.Receipt, out.Job = "recorded_observed", &receipt, workJobView(job)
	return nil
}

func workInspect(r *Reader, dir *os.File, project, id string) (out WorkStatus, resultErr error) {
	defer func() {
		if resultErr == nil {
			resultErr = workOutputBound(out)
		}
	}()
	out = WorkStatus{Protocol: WorkProtocol, ProjectID: project, RequestID: id, State: "blocked"}
	rec, p, in, err := workLoad(r, dir, project, id)
	if err != nil {
		return out, err
	}
	out.Kind, out.IntentSHA256, out.CreatedAt, out.BindingRevision, out.ParentRequestID = in.Kind, rec.Digest, rec.Created, in.BindingRevision, in.ParentRequestID
	if saved, e := loadStandaloneTaskGoal(dir, rec, in); e == nil {
		out.TaskGoal = &saved
		out.TaskLifecycle = &TaskLifecycle{MainHandled: "unverified", SessionArchived: "unverified"}
	} else if !errors.Is(e, syscall.ENOENT) {
		return out, e
	}
	out.State = "preparing"
	if p.Payload == "" {
		return out, nil
	}
	out.State = "prepared"
	var claim workClaim
	for _, step := range []struct{ suffix, state string }{{".armed.json", "armed"}, {".attempt.json", "uncertain"}} {
		b, e := workRead(dir, id+step.suffix)
		if errors.Is(e, syscall.ENOENT) {
			continue
		}
		var c workClaim
		if e != nil || json.Unmarshal(b, &c) != nil || c.Intent != rec.Digest || c.Payload != p.Digest || !isValidHexLen(c.Preview, 64) || step.state == "uncertain" && (out.State != "armed" || c != claim) {
			out.State = "blocked"
			return out, nil
		}
		claim = c
		out.State = step.state
	}
	if out.TaskGoal != nil {
		if err = standaloneTaskAdmission(r, rec, in, p, &out); err != nil {
			return out, err
		}
		if h := out.TaskLifecycle.Handoff; h != nil && h.InboxID != "" {
			a, state := taskWakeJournal(dir, rec, *h)
			out.TaskLifecycle.NativeWake = state
			if state != "" && state != "unresolved" {
				out.TaskLifecycle.NativeWakeID = a.MessageID
			}
		}
		if out.Job != nil && out.Job.SessionID != "" {
			out.Job.SessionState = sessionDeletionState(dir, project, out.Job.SessionID, id)
			view := archiveDisplay(project, id, out.Job.SessionID)
			a, err := taskArchiveAttempt(r, dir, view)
			if err == nil && taskArchiveResult(dir, a, &view) == nil {
				out.TaskLifecycle.SessionArchived = view.State
			}
		}
	} else if in.Kind == "implementation" || in.Kind == "review" {
		receipt, e := r.SubmissionReceipt([]byte(p.Payload))
		if e == nil {
			out.State = "recorded_observed"
			out.Receipt = &receipt
			out.Job, err = workJob(r, receipt.JobID)
			if err == nil && out.Job.SessionID != "" {
				out.Job.SessionState = sessionDeletionState(dir, project, out.Job.SessionID, id)
			}
			return out, err
		}
		if !faults.IsCode(e, faults.CodeNotFound) {
			return out, e
		}
	} else {
		var payload ResultPublicationPayload
		if _, e := decodeStrictJSONObject([]byte(p.Payload), projectPayloadBytes, &payload); e != nil {
			return out, e
		}
		a, e := publicationReceipt(r, payload)
		if e == nil {
			m, parseErr := parseManifestBytes([]byte(payload.Manifest), "", project)
			if parseErr != nil {
				return out, parseErr
			}
			verdict := ""
			if m.ReviewResult != nil {
				verdict = m.ReviewResult.Verdict
			}
			out.State = "recorded_observed"
			out.Result = &WorkAssociation{a.ID, a.Sequence, a.Job, a.ManifestDigest, a.Source, a.Verification, verdict}
		}
		if e != nil && !errors.Is(e, sql.ErrNoRows) && !faults.IsCode(e, faults.CodeNotFound) {
			return out, e
		}
		out.Job, err = workJob(r, payload.Target.Job)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func InspectProjectWork(dir, project, id string) (WorkStatus, error) {
	if !isValidProjectID(project) || !workID(id) {
		return WorkStatus{}, errors.New("invalid work pointer")
	}
	r, err := OpenReader(dir)
	if err != nil {
		return WorkStatus{}, err
	}
	defer r.Close()
	j, err := workReadDir(r, project)
	if err != nil {
		return WorkStatus{}, err
	}
	defer j.Close()
	status, err := workInspect(r, j, project, id)
	if err == nil {
		err = workOutputBound(status)
	}
	return status, err
}

func workOutputBound(value any) error {
	b, err := json.Marshal(value)
	if err == nil && len(b) > WorkResponseBytes {
		err = errors.New("work output exceeds 1 MiB; inspect narrower exact pointers")
	}
	return err
}

func workEntries(dir *os.File) ([]os.DirEntry, error) {
	entries, err := dir.ReadDir(workFileLimit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > workFileLimit {
		return nil, errors.New("work journal file bound exceeded; use exact request pointer")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

// Fingerprint only this project's fixed journal. No SQL revision is manufactured.
func workFingerprint(dir *os.File, project string) (string, error) {
	entries, err := workEntries(dir)
	if err != nil {
		return "", err
	}
	var parts []any
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		f, err := composePrivateAt(dir, e.Name(), syscall.O_RDONLY)
		if err != nil {
			return "", err
		}
		info, err := f.Stat()
		f.Close()
		if err != nil {
			return "", err
		}
		st := info.Sys().(*syscall.Stat_t)
		parts = append(parts, []any{e.Name(), info.Size(), info.ModTime().UnixNano(), st.Ino})
	}
	b, _ := json.Marshal([]any{project, parts})
	return sha256Hex(b), nil
}

func ListProjectWork(dir, project string) (WorkList, error) {
	out := WorkList{Protocol: WorkProtocol, ProjectID: project, Items: []WorkStatus{}, Journal: WorkJournalObservation{WorkObservationCursor: WorkObservationCursor{ProjectID: project}, Consistency: "filesystem_non_atomic", ReadAt: now()}}
	if !isValidProjectID(project) {
		return out, errors.New("invalid project")
	}
	r, err := OpenReader(dir)
	if err != nil {
		return out, err
	}
	defer r.Close()
	j, err := workReadDir(r, project)
	if errors.Is(err, syscall.ENOENT) {
		out.Journal.Fingerprint = sha256Hex([]byte(project + "\nabsent"))
		return out, nil
	}
	if err != nil {
		return out, err
	}
	defer j.Close()
	// A creation during listing must trigger the next lease, not be hidden by
	// an after-only cursor. Filesystem and SQLite observations are non-atomic.
	out.Journal.Fingerprint, err = workFingerprint(j, project)
	if err != nil {
		return out, err
	}
	if _, err = j.Seek(0, 0); err != nil {
		return out, err
	}
	entries, err := workEntries(j)
	if err != nil {
		return out, err
	}
	ids := map[string]bool{}
	for _, e := range entries {
		id, _, found := strings.Cut(e.Name(), ".")
		if found && strings.HasSuffix(e.Name(), ".json") && workID(id) {
			ids[id] = true
		}
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		if len(out.Items) >= workIntentLimit {
			return out, errors.New("work intent listing bound exceeded")
		}
		s, err := workInspect(r, j, project, id)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, s)
	}
	return out, workOutputBound(out)
}

// Lease verification reads metadata only, not all job/status/terminal evidence.
func ProjectWorkJournalObservation(dir, project string) (WorkJournalObservation, error) {
	out := WorkJournalObservation{WorkObservationCursor: WorkObservationCursor{ProjectID: project}, Consistency: "filesystem_non_atomic", ReadAt: now()}
	if !isValidProjectID(project) {
		return out, errors.New("invalid project")
	}
	r, err := OpenReader(dir)
	if err != nil {
		return out, err
	}
	defer r.Close()
	j, err := workReadDir(r, project)
	if errors.Is(err, syscall.ENOENT) {
		out.Fingerprint = sha256Hex([]byte(project + "\nabsent"))
		return out, nil
	}
	if err != nil {
		return out, err
	}
	defer j.Close()
	out.Fingerprint, err = workFingerprint(j, project)
	return out, err
}

func workBinding(r *Reader, project string) (ProjectBinding, error) {
	var b ProjectBinding
	err := r.store.readSchema5(func(q sqlQueryer) error {
		var e error
		b, e = r.store.currentProjectBinding(context.Background(), q, project)
		return e
	})
	return b, err
}

// Persist caller-known identity before capture reads. Any interrupted preparation
// stays preparing: same-ID recovery is observational, never a recapture retry.
func PrepareProjectWork(dir string, raw []byte) (WorkStatus, error) {
	return prepareProjectWork(dir, raw, nil)
}

// PrepareStandaloneTask uses the normal work intent and immutable queue receipt.
// Only the call that created the complete intent may proceed to admission.
func PrepareStandaloneTask(dir string, raw []byte) (out WorkStatus, fresh bool, err error) {
	out, err = prepareProjectWork(dir, raw, &fresh)
	return
}

func standaloneTaskGoal(rec workRecord, in WorkPrepareRequest) TaskGoal {
	return TaskGoal{IntentSHA256: rec.Digest, Title: goalTitle(in.Brief, ""), BaseCommit: rec.BaseCommit, GoalSHA256: sha256Hex([]byte(in.Brief))}
}

func prepareProjectWork(dir string, raw []byte, standalone *bool) (WorkStatus, error) {
	in, err := decodeWork(raw)
	if err != nil {
		return WorkStatus{}, err
	}
	j, err := openLocalJournal(dir, workJournal(in.ProjectID))
	if err != nil {
		return WorkStatus{}, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return WorkStatus{}, err
	}
	r, err := OpenReader(dir)
	if err != nil {
		return WorkStatus{}, err
	}
	defer r.Close()
	if b, e := workRead(j.dir, in.RequestID+".started.json"); e == nil {
		var rec workRecord
		if json.Unmarshal(b, &rec) != nil || rec.Input != string(raw) {
			return WorkStatus{}, errors.New("request ID already binds different bytes")
		}
		status, err := workInspect(r, j.dir, in.ProjectID, in.RequestID)
		if err == nil && standalone != nil && status.TaskGoal == nil {
			return status, errors.New("request has no retained standalone identity")
		}
		return status, err
	} else if !errors.Is(e, syscall.ENOENT) {
		return WorkStatus{}, e
	}
	entries, err := workEntries(j.dir)
	if err != nil {
		return WorkStatus{}, err
	}
	count := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), in.RequestID+".") {
			return WorkStatus{}, errors.New("orphan work evidence exists; never repeat preparation")
		}
		if strings.HasSuffix(e.Name(), ".started.json") {
			count++
		}
	}
	if count >= workIntentLimit {
		return WorkStatus{}, errors.New("project work intent limit reached; retain historical evidence")
	}
	binding, err := workBinding(r, in.ProjectID)
	if err != nil {
		return WorkStatus{}, err
	}
	if strconv.Itoa(binding.Revision) != in.BindingRevision {
		return WorkStatus{}, errors.New("exact current binding revision required")
	}
	if standalone != nil && (in.Kind != "implementation" || in.BaseRef != binding.CommitAnchor) {
		return WorkStatus{}, errors.New("standalone task requires the exact saved project base")
	}
	dev, ino, err := j.queueIdentity()
	if err != nil {
		return WorkStatus{}, err
	}
	baseCommit := ""
	if in.BaseRef != "" {
		baseCommit, err = gitRead(binding.RepoRoot, "rev-parse", "--verify", "--end-of-options", in.BaseRef+"^{commit}")
		if err != nil || !isValidHexLen(baseCommit, 40) {
			return WorkStatus{}, errors.New("worker base ref must resolve to a local commit in the bound repository")
		}
	}
	rec := workRecord{Protocol: WorkProtocol, Input: string(raw), Digest: sha256Hex(raw), Binding: binding, Queue: r.store.dir, Device: dev, Inode: ino, Created: now(), BaseCommit: baseCommit}
	if err = j.create(in.RequestID+".started.json", rec); err != nil {
		return WorkStatus{}, err
	}
	if standalone != nil {
		if err = j.create(in.RequestID+".task.json", standaloneTaskGoal(rec, in)); err != nil {
			return WorkStatus{}, err
		}
	}
	payload, err := buildWorkPayload(r, j.dir, in, binding, baseCommit)
	if err != nil {
		return WorkStatus{}, err
	}
	if len(payload) > projectPayloadBytes {
		return WorkStatus{}, errors.New("work payload exceeds bound")
	}
	record := workPayload{string(payload), sha256Hex(payload)}
	if err = workOutputBound(record); err != nil {
		return WorkStatus{}, err
	}
	if err = j.create(in.RequestID+".intent.json", record); err != nil {
		return WorkStatus{}, err
	}
	if standalone != nil {
		*standalone = true
	}
	return workInspect(r, j.dir, in.ProjectID, in.RequestID)
}

func buildWorkPayload(r *Reader, dir *os.File, in WorkPrepareRequest, b ProjectBinding, baseCommit string) ([]byte, error) {
	if in.Kind == "implementation" {
		p, err := workProfile(in.Profile)
		if err != nil {
			return nil, err
		}
		p.Key = workSubmissionKey(in.ProjectID, in.RequestID)
		p.ProjectID = in.ProjectID
		p.BindingRevision = b.Revision
		p.Repo = b.RepoRoot
		p.Base = b.CommitAnchor
		references := []string{}
		if in.BaseRef != "" {
			if !isValidHexLen(baseCommit, 40) {
				return nil, errors.New("explicit worker base must be frozen before payload construction")
			}
			p.Base = baseCommit
			references = append(references, "Worker base ref: "+in.BaseRef+"; resolved at preparation: "+baseCommit)
		}
		return r.BuildProjectImplementationDraft(ProjectImplementationDraft{ProjectJobDraftProfile: p, Prompt: in.Brief, Goal: in.Brief, Scope: []string{"The supplied brief within the bound repository"}, Exclusions: []string{"commit, merge, push, deploy, shared service/config changes"}, AcceptanceCriteria: []string{"Complete the supplied brief and report exact checks and gaps"}, AllowedActions: []string{"read", "edit", "write", "patch", "glob", "grep", "shell"}, ContextReferences: references})
	}
	parent, err := workInspect(r, dir, in.ProjectID, in.ParentRequestID)
	if err != nil || parent.State != "recorded_observed" || parent.BindingRevision != in.BindingRevision {
		return nil, errors.New("exact confirmed parent in same binding required")
	}
	switch in.Kind {
	case "capture":
		if parent.Kind != "implementation" || parent.Job == nil || !parent.Job.TerminalProven {
			return nil, errors.New("capture requires separately proven terminal implementation")
		}
		c, err := r.CapturePublicationContext(workPublicationSpace, in.ProjectID, parent.Job.JobID)
		if err != nil {
			return nil, err
		}
		prepared, err := r.store.prepareCapture(CaptureRequest{ProjectID: in.ProjectID, JobID: parent.Job.JobID, Worktree: c.Worktree, IgnoredExclusions: []string{}})
		if err != nil {
			return nil, err
		}
		var a ProjectResultAssociation
		request := CapturePreparationRequest{Context: c, IgnoredExclusions: []string{}}
		if err = r.store.publishCapture(prepared, &a, captureFence(r.store, request), true, false); err != nil {
			return nil, err
		}
		manifest, err := marshalCanonicalManifestJSON(prepared.manifest)
		if err != nil {
			return nil, err
		}
		return json.Marshal(ResultPublicationPayload{c.Target, c.OwnershipDigest, string(manifest), ""})
	case "review":
		if parent.Kind != "capture" || parent.Result == nil || parent.Result.Source != "machine_capture" || parent.Result.Verification != "machine_verified_snapshot" {
			return nil, errors.New("review requires exact published machine capture")
		}
		p, err := workProfile(in.Profile)
		if err != nil {
			return nil, err
		}
		p.Key = "work-" + sha256Hex([]byte(in.ProjectID+"\n"+in.RequestID))
		p.ProjectID = in.ProjectID
		p.BindingRevision = b.Revision
		p.Repo = b.RepoRoot
		manifestBytes, err := r.store.projectManifestBytes(in.ProjectID, parent.Result.ManifestDigest)
		if err != nil {
			return nil, err
		}
		target, err := parseManifestBytes(manifestBytes, "result", in.ProjectID)
		if err != nil || target.Digest != parent.Result.ManifestDigest {
			return nil, errors.New("exact captured implementation base unavailable")
		}
		p.Base = target.Result.Base
		rules := in.Rules + "\nReturn your final report as exactly one JSON object, without fences or surrounding prose: {\"verdict\":\"approved|needs_revision|unable_to_review\",\"findings\":[{\"location\":\"path:line\",\"severity\":\"critical|high|medium|low|info\",\"message\":\"finding\"}],\"limitations\":\"what was not verified\"}. Findings may be empty. Limitations must be nonempty. Do not claim tests ran. This is static review, not integration authorization."
		return r.BuildProjectReviewDraft(ProjectReviewDraft{ProjectJobDraftProfile: p, TargetAssociationID: parent.Result.AssociationID, TargetJobID: parent.Result.JobID, TargetResultDigest: parent.Result.ManifestDigest, Criteria: in.Criteria, Rules: rules})
	case "review_result":
		if parent.Kind != "review" || parent.Job == nil || !parent.Job.TerminalProven || parent.Receipt == nil || parent.Receipt.TargetJobID == nil || parent.Receipt.TargetResultDigest == nil {
			return nil, errors.New("review result requires separately proven terminal reviewer")
		}
		var authored workReviewReport
		report := in.ReviewResult
		if report == "" {
			output, err := workFinalReport(r, parent)
			if err != nil {
				return nil, err
			}
			report = output.Text
		}
		if _, err := decodeStrictJSONObject([]byte(report), maxManifestBytes, &authored); err != nil {
			return nil, err
		}
		m := reviewResultManifest{Version: 1, Kind: "review_result", ProjectID: in.ProjectID, Source: "author_import", ReviewJobID: parent.Job.JobID, ReviewSessionID: parent.Job.SessionID, TargetJobID: *parent.Receipt.TargetJobID, ReviewedResultDigest: *parent.Receipt.TargetResultDigest, Verdict: authored.Verdict, Findings: authored.Findings, Limitations: authored.Limitations}
		raw, err := marshalCanonicalManifestJSON(m)
		if err != nil {
			return nil, err
		}
		if _, err = parseManifestBytes(raw, "review_result", in.ProjectID); err != nil {
			return nil, err
		}
		c, err := r.ResultPublicationContext(workPublicationSpace, in.ProjectID, parent.Job.JobID)
		if err != nil {
			return nil, err
		}
		payload := ResultPublicationPayload{c.Target, c.OwnershipDigest, string(raw), ""}
		if _, err = validatePublication(r, payload); err != nil {
			return nil, err
		}
		return json.Marshal(payload)
	}
	return nil, errors.New("invalid work kind")
}
