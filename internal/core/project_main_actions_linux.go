package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"syscall"

	"github.com/ZanzyTHEbar/faults-go"

	"ocq/internal/core/dbquery"
)

func mainContext(r *Reader, rec mainRecord, c MainActionContext) error {
	if c.Protocol != MainProtocol || c.Action != "start" {
		return errors.New("Main start context required")
	}
	copy := WorkActionContext(c)
	copy.Protocol = WorkProtocol
	copy.Action = "confirm"
	return validateWorkContext(r, ProjectBinding{RepoRoot: rec.Repo}, copy)
}

func MainCLIContext(dir, project, operation string) (MainActionContext, error) {
	r, err := OpenReader(dir)
	if err != nil {
		return MainActionContext{}, err
	}
	defer r.Close()
	j, err := workReadDir(r, project)
	if err != nil {
		return MainActionContext{}, err
	}
	defer j.Close()
	rec, _, err := mainLoad(r, j, project, operation)
	if err != nil {
		return MainActionContext{}, err
	}
	queue, store, err := r.WorkQueueContext()
	if err != nil {
		return MainActionContext{}, err
	}
	exe, err := os.Executable()
	if err != nil {
		return MainActionContext{}, err
	}
	_, _, image, err := dispatcherImage(exe)
	if err != nil {
		return MainActionContext{}, err
	}
	return MainActionContext{Protocol: MainProtocol, Peer: "local", Space: "cli", Folder: rec.Repo, QueueIdentity: queue, StoreContext: store, ImageSHA256: image, ImageRole: "cli", Action: "start"}, nil
}

func PreviewProjectMain(dir, project, id string, c MainActionContext) (MainPreview, error) {
	if !isValidProjectID(project) || !workID(id) {
		return MainPreview{}, errors.New("invalid Main pointer")
	}
	j, err := openLocalJournal(dir, workJournal(project))
	if err != nil {
		return MainPreview{}, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return MainPreview{}, err
	}
	r, err := OpenReader(dir)
	if err != nil {
		return MainPreview{}, err
	}
	defer r.Close()
	rec, _, err := mainLoad(r, j.dir, project, id)
	if err != nil {
		return MainPreview{}, err
	}
	if err = mainContext(r, rec, c); err != nil {
		return MainPreview{}, err
	}
	status, err := inspectMainRecord(r, j.dir, project, id, false)
	if err != nil {
		return MainPreview{}, err
	}
	out := MainPreview{Protocol: MainProtocol, Status: status, Context: c, InputJSON: rec.Input, SessionJSON: mainPreviewSession(rec), RegistrationJSON: rec.RegistrationJSON, PromptJSON: rec.PromptJSON, Binding: rec.Binding, Role: rec.Role}
	b, _ := json.Marshal(out)
	out.PreviewSHA256 = sha256Hex(b)
	if err = workOutputBound(out); err != nil {
		return out, err
	}
	name := mainFile(id, "preview-"+out.PreviewSHA256)
	if old, e := workRead(j.dir, name); e == nil {
		var existing MainPreview
		if json.Unmarshal(old, &existing) != nil || !jsonEqual(existing, out) {
			return out, errors.New("Main retained preview mismatch")
		}
		return existing, nil
	} else if !errors.Is(e, syscall.ENOENT) {
		return out, e
	}
	if err = nativeJournalRoom(j.dir, 1); err != nil {
		return out, err
	}
	if err = j.create(name, out); err != nil {
		return out, err
	}
	return out, nil
}

type mainAttemptState uint8

const (
	mainAttemptUntrusted mainAttemptState = iota
	mainAttemptAbsent
	mainAttemptValid
)

func validateMainPreview(j *os.File, id, digest string, rec mainRecord, c MainActionContext) error {
	b, err := workRead(j, mainFile(id, "preview-"+digest))
	if err != nil {
		return err
	}
	var preview MainPreview
	if _, err = decodeStrictJSONObject(b, WorkResponseBytes, &preview); err != nil {
		return err
	}
	if preview.Protocol != MainProtocol || preview.Context != c || preview.InputJSON != rec.Input || preview.SessionJSON != mainPreviewSession(rec) || preview.RegistrationJSON != rec.RegistrationJSON || preview.PromptJSON != rec.PromptJSON || preview.PreviewSHA256 != digest || preview.Binding != rec.Binding || !jsonEqual(preview.Role, rec.Role) {
		return errors.New("Main exact preview/context mismatch")
	}
	preview.PreviewSHA256 = ""
	b, _ = json.Marshal(preview)
	if sha256Hex(b) != digest {
		return errors.New("Main preview digest mismatch")
	}
	return nil
}

func readMainStageClaim(j *os.File, name, intent string) (workClaim, error) {
	var claim workClaim
	b, err := workRead(j, name)
	if err != nil {
		return claim, err
	}
	if _, err = decodeStrictJSONObject(b, WorkResponseBytes, &claim); err != nil ||
		claim.Intent != intent || claim.Payload != intent || !isValidHexLen(claim.Preview, 64) ||
		claim.Context.Protocol != MainProtocol || claim.Context.Action != "start" {
		return workClaim{}, errors.New("Main stage evidence/authorization mismatch")
	}
	return claim, nil
}

// Validate against the retained arm, not today's caller or store schema.
// Re-previewing can change the preview digest, but not the armed context.
// Even a valid attempt only fences replay; it does not prove a native effect.
func readMainAttempt(j *os.File, id, stage, intent string, expected *WorkActionContext) (workClaim, mainAttemptState, error) {
	attempt, err := readMainStageClaim(j, mainFile(id, stage+".attempt"), intent)
	if errors.Is(err, syscall.ENOENT) {
		return workClaim{}, mainAttemptAbsent, nil
	}
	if err != nil {
		return workClaim{}, mainAttemptUntrusted, err
	}
	arm, err := readMainStageClaim(j, mainFile(id, stage+".armed"), intent)
	if err != nil || arm.Context != attempt.Context || expected != nil && attempt.Context != *expected {
		return workClaim{}, mainAttemptUntrusted, errors.Join(err, errors.New("Main stage attempt/authorization mismatch"))
	}
	return attempt, mainAttemptValid, nil
}

func readMainPromptAttempt(j *os.File, id string, rec mainRecord) (mainAttemptState, error) {
	attempt, state, err := readMainAttempt(j, id, "prompt", rec.Digest, nil)
	if state != mainAttemptValid {
		return state, err
	}
	// A prior attempt is a replay fence, not authorization to send from
	// today's context. Validate the preview that actually authorized it.
	if err := validateMainPreview(j, id, attempt.Preview, rec, MainActionContext(attempt.Context)); err != nil {
		return mainAttemptUntrusted, errors.Join(errors.New("Main prompt attempt's original preview unavailable or mismatched; never resend"), err)
	}
	return mainAttemptValid, nil
}

// Each stage has its own arm/attempt. An attempted stage is NEVER posted twice.
// Explicit Start may only advance later unattempted stages after exact readback.
func mainStage(j *composeJournal, id, stage string, claim workClaim) (bool, error) {
	if _, state, err := readMainAttempt(j.dir, id, stage, claim.Intent, &claim.Context); state != mainAttemptAbsent {
		return false, err
	}
	return recordMainStage(j, id, stage, claim, nil)
}

// The caller holds the journal lock and has checked that the attempt is absent.
func recordMainStage(j *composeJournal, id, stage string, claim workClaim, beforeAttempt func() error) (bool, error) {
	if err := nativeJournalRoom(j.dir, 2); err != nil {
		return false, err
	}
	arm := mainFile(id, stage+".armed")
	if old, e := readMainStageClaim(j.dir, arm, claim.Intent); e == nil {
		if old.Context != claim.Context {
			return false, errors.New("Main arm mismatch")
		}
		if err := j.syncFile(arm); err != nil {
			return false, err
		}
	} else if errors.Is(e, syscall.ENOENT) {
		if e = j.create(arm, claim); e != nil {
			return false, e
		}
	} else {
		return false, e
	}
	if beforeAttempt != nil {
		if err := beforeAttempt(); err != nil {
			return false, err
		}
	}
	if err := j.create(mainFile(id, stage+".attempt"), claim); err != nil {
		return false, err
	}
	return true, nil
}

func beginMainPrompt(j *composeJournal, id string, rec mainRecord, claim workClaim, current map[string]any, selectMain func() mainSelection) (bool, error) {
	if state, err := readMainPromptAttempt(j.dir, id, rec); state != mainAttemptAbsent {
		return false, err
	}
	owner := nativeHash(json.RawMessage(rec.Metadata))
	if err := nativeIdentity(current, rec.SessionID, rec.Repo, rec.ProjectID, owner); err != nil {
		return false, err
	}
	if nativeProfile(current) != rec.ProfileSHA {
		return false, errors.New("Main current profile differs; no input sent")
	}
	return recordMainStage(j, id, "prompt", claim, func() error {
		// Sample the full SQL binding and journal-selected role after the final
		// native GET. This fence is not atomic with the native session or POST.
		s := selectMain()
		if s.err != nil || s.binding != rec.Binding || s.setup.SetupID != rec.SetupID ||
			s.setup.SessionID != rec.SessionID || s.setup.Role.SHA256 != rec.Role.SHA256 ||
			nativeHash(json.RawMessage(s.setup.Metadata)) != owner {
			return errors.Join(s.err, errors.New("Main binding, setup or role changed since preview; no input sent"))
		}
		return nil
	})
}

func StartProjectMain(dir, project, id, digest string, c MainActionContext) (MainStatus, error) {
	if !isValidProjectID(project) || !workID(id) || !isValidHexLen(digest, 64) {
		return MainStatus{}, errors.New("invalid Main confirmation")
	}
	j, err := openLocalJournal(dir, workJournal(project))
	if err != nil {
		return MainStatus{}, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return MainStatus{}, err
	}
	r, err := OpenReader(dir)
	if err != nil {
		return MainStatus{}, err
	}
	defer r.Close()
	rec, in, err := mainLoad(r, j.dir, project, id)
	if err != nil {
		return MainStatus{}, err
	}
	if mainDeletionState(j.dir, project, rec) != "" {
		return MainStatus{}, errors.New("this Main session was deleted or its deletion is uncertain; no old start or retry")
	}
	if _, superseded, err := readMainSupersession(j.dir, id, rec.Digest); err != nil || superseded {
		return MainStatus{}, errors.Join(err, errors.New("Main setup superseded; this intent can never start"))
	}
	if err = mainContext(r, rec, c); err != nil {
		return MainStatus{}, err
	}
	if err = validateMainPreview(j.dir, id, digest, rec, c); err != nil {
		return MainStatus{}, err
	}
	claim := workClaim{Intent: rec.Digest, Payload: rec.Digest, Preview: digest, Context: WorkActionContext(c)}
	for _, file := range []string{mainFile(id, "intent"), mainFile(id, "preview-"+digest)} {
		if err = j.syncFile(file); err != nil {
			return MainStatus{}, err
		}
	}
	if in.Mode == "role-refresh" {
		return startMainRoleRefresh(r, j, rec, in, claim, false)
	}
	promptAttempt, err := readMainPromptAttempt(j.dir, id, rec)
	if err != nil {
		return MainStatus{}, err
	}
	if promptAttempt == mainAttemptValid {
		// Observation only, even after a legitimate human profile change.
		return inspectMainRecord(r, j.dir, project, id, true)
	}
	var prompt json.RawMessage
	if in.Mode == "send" {
		suffix, reserved := strings.CutPrefix(rec.PromptID, mainPromptPrefix)
		prompt = mainPromptPayload(in, rec.PromptID)
		if !reserved || !isValidHexLen(suffix, 32) || len(prompt) > WorkRequestBytes || string(prompt) != rec.PromptJSON {
			return MainStatus{}, errors.New("retained Main prompt payload does not match the exact input")
		}
	} else if rec.PromptID != "" || rec.PromptJSON != "" {
		return MainStatus{}, errors.New("unexpected retained Main prompt payload")
	}
	// A later explicit start of an already bound/attempted operation is recovery,
	// not a replay of the original prompt or a permanent creation-profile fence.
	registered := rec.RegistrationJSON == ""
	if rec.RegistrationJSON != "" {
		_, e := r.RegistrationReceipt([]byte(rec.RegistrationJSON))
		registered = e == nil
		if e != nil && !faults.IsCode(e, faults.CodeNotFound) {
			return MainStatus{}, e
		}
	}
	if registered && rec.PromptID == "" {
		if setup, _, _, e := selectedMain(r, j.dir, project); e == nil && setup.SetupID == rec.SetupID && setup.SessionID == rec.SessionID {
			return inspectMainRecord(r, j.dir, project, id, true)
		}
	}
	_, _, roleImage, err := dispatcherImage(rec.Role.CLI)
	if err != nil || roleImage != rec.Role.CLISHA256 {
		return MainStatus{}, errors.New("retained Main CLI image changed/unavailable; no bootstrap effects")
	}
	queue, store, err := r.WorkQueueContext()
	if err != nil || queue != rec.Role.QueueIdentity || store != rec.Role.StoreContext {
		return MainStatus{}, errors.New("retained Main role queue/store context changed")
	}
	common, head, _, err := inspectCanonicalMain(rec.Repo, rec.Branch)
	if err != nil {
		return MainStatus{}, err
	}
	if common != rec.Common || head != rec.Head {
		return MainStatus{}, errors.New("canonical Main HEAD/common-dir changed since preview; prepare a distinct explicit operation")
	}
	service, err := discoverNativeService(rec.Repo)
	if err != nil {
		return MainStatus{}, err
	}
	if service != rec.Server {
		return MainStatus{}, errors.New("Main service identity changed; no mutation or replacement")
	}
	regCommitted := false
	if rec.RegistrationJSON != "" {
		_, e := r.RegistrationReceipt([]byte(rec.RegistrationJSON))
		regCommitted = e == nil
		if e != nil && !faults.IsCode(e, faults.CodeNotFound) {
			return MainStatus{}, e
		}
	}
	if !regCommitted {
		if in.Mode == "create" {
			if _, e := dbquery.New(r.store.db).ProjectByID(context.Background(), project); !errors.Is(e, sql.ErrNoRows) {
				return MainStatus{}, errors.New("new Main requires provably absent project; existing/unknown binding is not replaced")
			}
		} else {
			current, e := workBinding(r, project)
			if e != nil || current != rec.Binding {
				return MainStatus{}, errors.New("Main selected binding changed since preparation")
			}
			if in.Mode == "replace" && !retiredMain(r, j.dir, project, current) {
				return MainStatus{}, errors.New("Main replacement requires exact acknowledged retirement")
			}
		}
	}
	if mainCreatesSession(in.Mode) {
		var create map[string]any
		if _, e := decodeStrictJSONObject([]byte(rec.SessionJSON), WorkRequestBytes, &create); e != nil {
			return MainStatus{}, errors.New("invalid retained session creation")
		}
		fresh, e := mainStage(j, id, "session", claim)
		if e != nil {
			return MainStatus{}, e
		}
		if fresh {
			if _, e = nativeCall(rec.Repo, service, "post", "/api/session", create); e != nil {
				return inspectMainUncertain(r, j.dir, project, id, e)
			}
		}
	}
	session, err := nativeSession(rec.Repo, service, rec.SessionID)
	if err != nil {
		return inspectMainUncertain(r, j.dir, project, id, err)
	}
	if err = nativeIdentity(session, rec.SessionID, rec.Repo, rec.ProjectID, nativeHash(json.RawMessage(rec.Metadata))); err != nil {
		return MainStatus{}, err
	}
	// Main's normal subsequent human profile changes are legitimate. This exact
	// operation only pins the snapshot prepared for its NEW explicit input.
	if nativeProfile(session) != rec.ProfileSHA {
		return MainStatus{}, errors.New("Main profile changed since preview; do not overwrite it")
	}
	if mainSetupMode(in.Mode) {
		if err = ensureMainRole(j, rec, id, claim); err != nil {
			return inspectMainUncertain(r, j.dir, project, id, err)
		}
	} else {
		selected, _, _, e := selectedMain(r, j.dir, project)
		if e != nil || selected.Role.SHA256 != rec.Role.SHA256 {
			return MainStatus{}, errors.Join(e, errors.New("Main operating contract changed; prepare a fresh operation, never replay"))
		}
		matches, _, e := observeMainRole(rec)
		if e != nil || !matches {
			return MainStatus{}, errors.Join(e, errors.New("Main role not proven; no automatic repair"))
		}
	}
	if rec.RegistrationJSON != "" {
		if _, e := r.RegistrationReceipt([]byte(rec.RegistrationJSON)); e != nil {
			fresh, e := mainStage(j, id, "registration", claim)
			if e != nil {
				return MainStatus{}, e
			}
			if !fresh {
				return inspectMainUncertain(r, j.dir, project, id, errors.New("registration attempt has no exact receipt; no retry"))
			}
			w, e := OpenExistingProjectIntake(dir)
			if e != nil {
				return inspectMainUncertain(r, j.dir, project, id, e)
			}
			_, e = (&Queue{store: w.store}).registerProject([]byte(rec.RegistrationJSON), func(ctx context.Context, req ProjectRegistrationRequest) (string, string, string, error) {
				root, common, e := canonicalizeGitRepo(req.Repo, req.CommitAnchor)
				if e != nil {
					return "", "", "", e
				}
				seenCommon, seenHead, _, e := inspectCanonicalMain(rec.Repo, rec.Branch)
				if e != nil || seenCommon != rec.Common || seenHead != rec.Head {
					return "", "", "", errors.New("canonical Main changed before registration CAS")
				}
				seen, e := nativeSession(req.Repo, service, req.ElephantSessionID)
				if e != nil {
					return "", "", "", e
				}
				e = nativeIdentity(seen, rec.SessionID, rec.Repo, rec.ProjectID, nativeHash(json.RawMessage(rec.Metadata)))
				if e == nil && nativeProfile(seen) != rec.ProfileSHA {
					e = errors.New("Main profile changed before registration")
				}
				return root, common, now(), e
			})
			e = errors.Join(e, w.Close())
			if e != nil {
				return inspectMainUncertain(r, j.dir, project, id, e)
			}
		}
	}
	_, _, binding, err := selectedMain(r, j.dir, project)
	if err != nil {
		return MainStatus{}, err
	}
	if binding.ElephantSessionID != rec.SessionID || binding.RepoRoot != rec.Repo {
		return MainStatus{}, errors.New("current SQL Main binding changed")
	}
	if rec.RegistrationJSON != "" {
		receipt, e := r.RegistrationReceipt([]byte(rec.RegistrationJSON))
		if e != nil || receipt.Revision != binding.Revision {
			return MainStatus{}, errors.New("Main registration is no longer the exact current binding")
		}
	}
	if prompt != nil {
		// Exact profile/location check immediately before the sole prompt attempt.
		current, e := nativeSession(rec.Repo, service, rec.SessionID)
		if e != nil {
			return inspectMainUncertain(r, j.dir, project, id, e)
		}
		fresh, e := beginMainPrompt(j, id, rec, claim, current, func() mainSelection {
			return loadMainSelection(r, j.dir, project)
		})
		if e != nil {
			return MainStatus{}, e
		}
		if fresh {
			if _, e = nativeCall(rec.Repo, service, "post", "/api/session/"+url.PathEscape(rec.SessionID)+"/prompt", prompt); e != nil {
				return inspectMainUncertain(r, j.dir, project, id, e)
			}
		}
	}
	return inspectMainRecord(r, j.dir, project, id, true)
}

func mainPreviewSession(rec mainRecord) string {
	var s map[string]any
	if json.Unmarshal([]byte(rec.SessionJSON), &s) != nil {
		return ""
	}
	delete(s, "metadata")
	s["ownership_sha256"] = nativeHash(json.RawMessage(rec.Metadata))
	b, _ := json.Marshal(s)
	return string(b)
}

func inspectMainUncertain(r *Reader, j *os.File, project, id string, cause error) (MainStatus, error) {
	status, e := inspectMainRecord(r, j, project, id, false)
	return status, errors.Join(cause, e)
}
