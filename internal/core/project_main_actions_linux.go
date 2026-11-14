package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/ZanzyTHEbar/faults-go"
	"net/url"
	"ocq/internal/core/dbquery"
	"os"
	"syscall"
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

// Each stage has its own arm/attempt. An attempted stage is NEVER posted twice.
// Explicit Start may only advance later unattempted stages after exact readback.
func mainStage(j *composeJournal, id, stage string, claim workClaim) (bool, error) {
	name := mainFile(id, stage+".attempt")
	if b, e := workRead(j.dir, name); e == nil {
		var old workClaim
		if json.Unmarshal(b, &old) != nil || old.Intent != claim.Intent || old.Payload != claim.Payload || old.Context != claim.Context || !isValidHexLen(old.Preview, 64) {
			return false, errors.New("Main stage attempt/authorization mismatch")
		}
		return false, nil
	} else if !errors.Is(e, syscall.ENOENT) {
		return false, e
	}
	if err := nativeJournalRoom(j.dir, 2); err != nil {
		return false, err
	}
	arm := mainFile(id, stage+".armed")
	if b, e := workRead(j.dir, arm); e == nil {
		var old workClaim
		if json.Unmarshal(b, &old) != nil || old.Intent != claim.Intent || old.Payload != claim.Payload || old.Context != claim.Context || !isValidHexLen(old.Preview, 64) {
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
	if err := j.create(name, claim); err != nil {
		return false, err
	}
	return true, nil
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
	if _, superseded, err := readMainSupersession(j.dir, id, rec.Digest); err != nil || superseded {
		return MainStatus{}, errors.Join(err, errors.New("Main setup superseded; this intent can never start"))
	}
	if err = mainContext(r, rec, c); err != nil {
		return MainStatus{}, err
	}
	b, err := workRead(j.dir, mainFile(id, "preview-"+digest))
	if err != nil {
		return MainStatus{}, err
	}
	var preview MainPreview
	if _, err = decodeStrictJSONObject(b, WorkResponseBytes, &preview); err != nil {
		return MainStatus{}, err
	}
	if preview.Context != c || preview.InputJSON != rec.Input || preview.SessionJSON != mainPreviewSession(rec) || preview.RegistrationJSON != rec.RegistrationJSON || preview.PromptJSON != rec.PromptJSON || preview.PreviewSHA256 != digest || preview.Binding != rec.Binding || !jsonEqual(preview.Role, rec.Role) {
		return MainStatus{}, errors.New("Main exact preview/context mismatch")
	}
	preview.PreviewSHA256 = ""
	b, _ = json.Marshal(preview)
	if sha256Hex(b) != digest {
		return MainStatus{}, errors.New("Main preview digest mismatch")
	}
	claim := workClaim{Intent: rec.Digest, Payload: rec.Digest, Preview: digest, Context: WorkActionContext(c)}
	for _, file := range []string{mainFile(id, "intent"), mainFile(id, "preview-"+digest)} {
		if err = j.syncFile(file); err != nil {
			return MainStatus{}, err
		}
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
	_, alreadySent := mainAttempt(j.dir, id, "prompt")
	if registered && (rec.PromptID == "" || alreadySent) {
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
		}
	}
	if in.Mode == "create" {
		fresh, e := mainStage(j, id, "session", claim)
		if e != nil {
			return MainStatus{}, e
		}
		if fresh {
			var create any
			if json.Unmarshal([]byte(rec.SessionJSON), &create) != nil {
				return MainStatus{}, errors.New("invalid retained session creation")
			}
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
	_, promptAttempted := mainAttempt(j.dir, id, "prompt")
	if !promptAttempted && nativeProfile(session) != rec.ProfileSHA {
		return MainStatus{}, errors.New("Main profile changed since preview; do not overwrite it")
	}
	if in.Mode == "create" || in.Mode == "adopt" {
		if err = ensureMainRole(j, rec, id, claim); err != nil {
			return inspectMainUncertain(r, j.dir, project, id, err)
		}
	} else {
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
	if rec.PromptJSON != "" {
		// Exact profile/location check immediately before the sole prompt attempt.
		current, e := nativeSession(rec.Repo, service, rec.SessionID)
		if e != nil {
			return inspectMainUncertain(r, j.dir, project, id, e)
		}
		if e = nativeIdentity(current, rec.SessionID, rec.Repo, rec.ProjectID, nativeHash(json.RawMessage(rec.Metadata))); e != nil {
			return MainStatus{}, e
		}
		fresh, e := mainStage(j, id, "prompt", claim)
		if e != nil {
			return MainStatus{}, e
		}
		if fresh {
			if nativeProfile(current) != rec.ProfileSHA {
				return MainStatus{}, errors.New("Main current profile differs; no input sent")
			}
			var prompt any
			_ = json.Unmarshal([]byte(rec.PromptJSON), &prompt)
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
