package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func mainAttempt(j *os.File, id, stage string) (workClaim, bool) {
	b, e := workRead(j, mainFile(id, stage+".attempt"))
	if errors.Is(e, syscall.ENOENT) {
		return workClaim{}, false
	}
	var claim workClaim
	if e != nil || json.Unmarshal(b, &claim) != nil {
		return workClaim{}, true
	}
	return claim, true
}

func inspectMainRecord(r *Reader, j *os.File, project, id string, live bool) (MainStatus, error) {
	rec, in, err := mainLoad(r, j, project, id)
	if err != nil {
		return MainStatus{}, err
	}
	out := MainStatus{Protocol: MainProtocol, ProjectID: project, OperationID: id, Mode: in.Mode, State: "prepared", Reason: "stored_only; no live session lookup", IntentSHA256: rec.Digest, SetupID: rec.SetupID, SessionID: rec.SessionID, PromptID: rec.PromptID, Repo: rec.Repo, MainBranch: rec.Branch, Head: rec.Head, CommitAnchor: rec.Head, BindingRevision: in.ExpectedRevision, OpenCodeProjectID: rec.ProjectID, OwnershipSHA256: nativeHash(json.RawMessage(rec.Metadata)), OwnershipMode: rec.OwnershipMode, ProfileSHA256: rec.ProfileSHA, ReadAt: now()}
	if _, superseded, err := readMainSupersession(j, id, rec.Digest); err != nil {
		return out, err
	} else if superseded {
		out.State = "superseded"
		out.Reason = "explicit create operation superseded this provably unattempted setup; history retained, start forbidden"
		return out, nil
	}
	for _, stage := range []string{"session", "role", "registration", "prompt"} {
		claim, present := mainAttempt(j, id, stage)
		if present {
			if claim.Intent != rec.Digest || claim.Payload != rec.Digest || !isValidHexLen(claim.Preview, 64) {
				out.State = "blocked"
				out.Reason = "Main stage evidence malformed; retain and inspect"
				return out, nil
			}
			out.State = stage + "_attempted_unknown"
		}
	}
	regObserved := rec.RegistrationJSON == ""
	if rec.RegistrationJSON != "" {
		if receipt, e := r.RegistrationReceipt([]byte(rec.RegistrationJSON)); e == nil {
			regObserved = true
			out.State = "binding_committed"
			out.BindingRevision = strconv.Itoa(receipt.Revision)
		}
	}
	setup, _, binding, selectedErr := selectedMain(r, j, project)
	selectedOK := selectedErr == nil && setup.SessionID == rec.SessionID && setup.SetupID == rec.SetupID
	_, promptAttempted := mainAttempt(j, id, "prompt")
	if selectedOK {
		out.BindingRevision = strconv.Itoa(binding.Revision)
		out.CommitAnchor = binding.CommitAnchor
		if regObserved {
			out.State = "ready"
			if rec.PromptID != "" {
				out.State = "binding_committed"
				if in.Mode == "send" {
					out.State = "prepared"
				}
				if promptAttempted {
					out.State = "prompt_attempted_unknown"
				}
			}
		}
	} else if out.State == "binding_committed" || in.Mode == "reopen" || in.Mode == "send" || in.Mode == "refresh" {
		out.State = "blocked"
		out.Reason = "current SQL binding no longer selects this Main setup"
		return out, nil
	}
	if !live {
		return out, workOutputBound(out)
	}
	common, head, dirty, e := inspectCanonicalMain(rec.Repo, rec.Branch)
	if e != nil || common != rec.Common {
		out.State = "blocked"
		out.Reason = "canonical MAIN branch/checkout/common-dir unavailable; no Git state changed"
		return out, nil
	}
	out.Head, out.Dirty, out.HeadAdvanced = head, dirty, head != out.CommitAnchor
	service, e := discoverNativeService(rec.Repo)
	if e != nil {
		out.State = "unknown"
		out.Reason = "intended Main service unavailable; never replace"
		return out, nil
	}
	s, e := nativeSession(rec.Repo, service, rec.SessionID)
	if e != nil {
		out.State = "missing_or_unknown"
		out.Reason = "exact bound/preallocated Main unavailable; no replacement or retry"
		return out, nil
	}
	if e = nativeIdentity(s, rec.SessionID, rec.Repo, rec.ProjectID, out.OwnershipSHA256); e != nil {
		out.State = "mismatch"
		out.Reason = "exact Main ownership/location/project mismatch"
		return out, nil
	}
	out.ProfileSHA256 = nativeProfile(s)
	observedRole := rec
	observedRole.Server = service
	if matches, _, err := observeMainRole(observedRole); err != nil || !matches {
		out.State = "role_unverified"
		out.Reason = "exact session exists but persistent Main role is absent/mismatched/unknown; no automatic overwrite or prompt"
		return out, nil
	}
	out.Reason = "explicit exact GET observation; Main is multi-turn, not job completion"
	if service != rec.Server {
		out.Reason = "current discovered service changed; exact Main identity matches by GET only; original mutations are never replayed"
	}
	if !selectedOK {
		out.State = "session_observed"
		out.Reason = "session exists but is not the current SQL-bound Main; explicit registration/CAS still required"
		return out, nil
	}
	if rec.PromptID != "" {
		if !promptAttempted {
			return out, nil
		}
		observed := rec
		observed.Server = service
		found, pending, e := observeMainPrompt(observed, in)
		if e != nil {
			out.State = "prompt_attempted_unknown"
			out.Reason = "prompt observation incomplete; never resend"
			return out, nil
		}
		if !found {
			if pending {
				out.State = "prompt_pending"
				out.Reason = "exact input acknowledged in inbox, consumption not established"
			} else {
				out.State = "prompt_attempted_unknown"
				out.Reason = "exact retained input not observed; never resend"
			}
			return out, nil
		}
	}
	b, e := nativeCall(rec.Repo, service, "get", "/api/session/active", nil)
	if e != nil {
		out.State = "unknown"
		out.Reason = "Main activity unavailable; no completion inferred"
		return out, nil
	}
	var active map[string]json.RawMessage
	if decodeData(b, &active) != nil {
		out.State = "unknown"
		return out, nil
	}
	out.State = "ready"
	if _, ok := active[rec.SessionID]; ok {
		out.State = "busy"
	}
	return out, workOutputBound(out)
}

func observeMainPrompt(rec mainRecord, in MainPrepareRequest) (bool, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var expected map[string]any
	if json.Unmarshal([]byte(rec.PromptJSON), &expected) != nil {
		return false, false, errors.New("retained Main prompt unavailable")
	}
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 100; page++ {
		query := url.Values{"limit": {"100"}}
		if cursor == "" {
			query.Set("order", "asc")
		} else {
			query.Set("cursor", cursor)
		}
		b, e := nativeCallContext(ctx, rec.Repo, rec.Server, "get", "/api/session/"+url.PathEscape(rec.SessionID)+"/message?"+query.Encode(), nil)
		if e != nil {
			return false, false, e
		}
		var response struct {
			Data   []message `json:"data"`
			Cursor *struct {
				Next *string `json:"next"`
			} `json:"cursor"`
		}
		if json.Unmarshal(b, &response) != nil || response.Data == nil || response.Cursor == nil {
			return false, false, errors.New("Main message pagination unavailable")
		}
		for _, m := range response.Data {
			if m.ID == rec.PromptID {
				return m.Type == "user" && m.Text == in.Prompt && jsonEqual(m.Metadata, expected["metadata"]) && len(m.Files)+len(m.Agents)+len(m.Skills) == 0, false, nil
			}
		}
		if response.Cursor.Next == nil {
			break
		}
		cursor = *response.Cursor.Next
		if cursor == "" || seen[cursor] {
			return false, false, errors.New("Main transcript cursor repeated")
		}
		seen[cursor] = true
		if page == 99 {
			return false, false, errors.New("Main prompt inspection bound reached")
		}
	}
	b, e := nativeCallContext(ctx, rec.Repo, rec.Server, "get", "/api/session/"+url.PathEscape(rec.SessionID)+"/inbox", nil)
	if e != nil {
		return false, false, e
	}
	var inbox []inboxItem
	if e = decodeData(b, &inbox); e != nil {
		return false, false, e
	}
	for _, item := range inbox {
		if item.ID == rec.PromptID {
			return false, item.SessionID == rec.SessionID && item.Type == "user" && item.Payload.Text == in.Prompt && jsonEqual(item.Payload.Metadata, expected["metadata"]), nil
		}
	}
	return false, false, nil
}

func mainInspect(dir, project, id string, live bool) (MainStatus, error) {
	if !isValidProjectID(project) || id != "" && !workID(id) {
		return MainStatus{}, errors.New("invalid Main pointer")
	}
	r, err := OpenReader(dir)
	if err != nil {
		return MainStatus{}, err
	}
	defer r.Close()
	j, err := workReadDir(r, project)
	if err != nil {
		return MainStatus{}, err
	}
	defer j.Close()
	if id == "" {
		rec, _, _, e := selectedMain(r, j, project)
		if e != nil {
			return MainStatus{}, e
		}
		id = rec.SetupID
	}
	return inspectMainRecord(r, j, project, id, live)
}
func InspectProjectMain(dir, project, id string) (MainStatus, error) {
	return mainInspect(dir, project, id, false)
}
func RecoverProjectMain(dir, project, id string) (MainStatus, error) {
	return mainInspect(dir, project, id, true)
}

func ListProjectMain(dir, project string) (MainList, error) {
	out := MainList{Protocol: MainProtocol, ProjectID: project, Items: []MainStatus{}}
	if !isValidProjectID(project) {
		return out, errors.New("invalid Main project")
	}
	r, err := OpenReader(dir)
	if err != nil {
		return out, err
	}
	defer r.Close()
	j, err := workReadDir(r, project)
	if errors.Is(err, syscall.ENOENT) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	defer j.Close()
	entries, err := workEntries(j)
	if err != nil {
		return out, err
	}
	ids := []string{}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".main-") && strings.HasSuffix(name, ".intent.json") {
			id := strings.TrimSuffix(strings.TrimPrefix(name, ".main-"), ".intent.json")
			if workID(id) {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) > workIntentLimit {
		return out, errors.New("Main listing bound reached")
	}
	sort.Strings(ids)
	for _, id := range ids {
		s, e := inspectMainRecord(r, j, project, id, false)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, s)
	}
	if setup, _, _, e := selectedMain(r, j, project); e == nil {
		selected, e := inspectMainRecord(r, j, project, setup.SetupID, false)
		if e == nil {
			out.Selected = &selected
		}
	}
	return out, workOutputBound(out)
}
