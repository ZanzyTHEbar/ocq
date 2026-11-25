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

func inspectMainRecord(r *Reader, j *os.File, project, id string, live bool) (MainStatus, error) {
	rec, in, err := mainLoad(r, j, project, id)
	if err != nil {
		return MainStatus{}, err
	}
	return inspectMainIntent(r, j, rec, in, live, nil)
}

type mainSelection struct {
	setup   mainRecord
	binding ProjectBinding
	err     error
}

func loadMainSelection(r *Reader, j *os.File, project string) mainSelection {
	setup, _, binding, err := selectedMain(r, j, project)
	return mainSelection{setup, binding, err}
}

func inspectMainIntent(r *Reader, j *os.File, rec mainRecord, in MainPrepareRequest, live bool, selection *mainSelection) (MainStatus, error) {
	project, id := in.ProjectID, in.OperationID
	out := MainStatus{Protocol: MainProtocol, ProjectID: project, OperationID: id, Mode: in.Mode, State: "prepared", Reason: "stored_only; no live session lookup", IntentSHA256: rec.Digest, SetupID: rec.SetupID, SessionID: rec.SessionID, PromptID: rec.PromptID, Repo: rec.Repo, MainBranch: rec.Branch, Head: rec.Head, CommitAnchor: rec.Head, BindingRevision: in.ExpectedRevision, OpenCodeProjectID: rec.ProjectID, OwnershipSHA256: nativeHash(json.RawMessage(rec.Metadata)), OwnershipMode: rec.OwnershipMode, ProfileSHA256: rec.ProfileSHA, ReadAt: now()}
	if out.BindingRevision == "" && rec.Binding.Revision > 0 {
		// Historical operations without an explicit CAS retain the binding
		// observed at preparation, even after their Main stops being selected.
		out.BindingRevision = strconv.Itoa(rec.Binding.Revision)
	}
	if _, superseded, err := readMainSupersession(j, id, rec.Digest); err != nil {
		return out, err
	} else if superseded {
		out.State = "superseded"
		out.Reason = "explicit create operation superseded this provably unattempted setup; history retained, start forbidden"
		return out, nil
	}
	promptAttempted, roleAttempted := false, false
	for _, stage := range []string{"session", "role", "registration", "prompt"} {
		var state mainAttemptState
		if stage == "prompt" && live {
			state, _ = readMainPromptAttempt(j, id, rec)
		} else {
			_, state, _ = readMainAttempt(j, id, stage, rec.Digest, nil)
		}
		switch state {
		case mainAttemptUntrusted:
			out.State = "blocked"
			out.Reason = "Main " + stage + " attempt evidence malformed or unreadable; outcome unknown, never replay"
			return out, nil
		case mainAttemptValid:
			out.State = stage + "_attempted_unknown"
			promptAttempted = promptAttempted || stage == "prompt"
			roleAttempted = roleAttempted || stage == "role"
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
	if selection == nil {
		s := loadMainSelection(r, j, project)
		selection = &s
	}
	setup, binding := selection.setup, selection.binding
	selectedOK := selection.err == nil && setup.SessionID == rec.SessionID && setup.SetupID == rec.SetupID
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
		if in.Mode == "role-refresh" {
			return inspectMainRoleRefresh(j, rec, in, out, roleAttempted)
		}
		return out, workOutputBound(out)
	}
	if in.Mode == "role-refresh" {
		return inspectMainRoleRefresh(j, rec, in, out, roleAttempted)
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
	if selectedOK {
		observedRole.Role = setup.Role
	}
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
	// Main is multi-turn, not a bounded-worker completion proof. Inspect its
	// exact reserved input, not unrelated tool output in an expanding transcript.
	b, messageErr := nativeCallContext(ctx, rec.Repo, rec.Server, "get", "/api/session/"+url.PathEscape(rec.SessionID)+"/message/"+url.PathEscape(rec.PromptID), nil)
	if messageErr == nil {
		var m message
		if err := decodeData(b, &m); err != nil || m.ID != rec.PromptID {
			return false, false, errors.New("exact Main message identity unavailable")
		}
		return m.Type == "user" && m.Text == in.Prompt && jsonEqual(m.Metadata, expected["metadata"]) && len(m.Files)+len(m.Agents)+len(m.Skills) == 0, false, nil
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
			return false, item.SessionID == rec.SessionID && item.Type == "user" && item.Payload.Text == in.Prompt && jsonEqual(item.Payload.Metadata, expected["metadata"]) && len(item.Payload.Files)+len(item.Payload.Agents)+len(item.Payload.Skills) == 0, nil
		}
	}
	return false, false, messageErr
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
	if id != "" {
		if out, handled, err := recoverMainRoleRefresh(dir, project, id); handled || err != nil {
			return out, err
		}
	}
	return mainInspect(dir, project, id, true)
}

func ListProjectMain(dir, project string) (MainList, error) {
	return listProjectMain(dir, project, nil)
}

// sampledWork is the filesystem projection already read for the observation
// response. A standalone Main list reads Work only when there are send goals.
func listProjectMain(dir, project string, sampledWork *WorkList) (MainList, error) {
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
	selection := loadMainSelection(r, j, project)
	for _, id := range ids {
		rec, in, e := mainLoad(r, j, project, id)
		if e != nil {
			return out, e
		}
		s, e := inspectMainIntent(r, j, rec, in, false, &selection)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, s)
		if selection.err == nil && id == selection.setup.SetupID {
			out.Selected = &s
		}
		if s.Mode == "send" {
			out.Goals = append(out.Goals, newGoalView(s, in))
		}
	}
	if len(out.Goals) > 0 {
		if sampledWork == nil {
			work, err := ListProjectWork(dir, project)
			if err != nil {
				return out, err
			}
			sampledWork = &work
		}
		lineage, err := readGoalLineage(j, project, sampledWork.Items)
		if err != nil {
			return out, err
		}
		for i := range out.Goals {
			goal := &out.Goals[i]
			if err := goal.addLineage(lineage[goal.Main.OperationID]); err != nil {
				return out, err
			}
		}
	}
	if selection.err == nil {
		if out.Selected == nil {
			// The journal and the SQL binding are not sampled atomically: an
			// exact selected setup may have appeared after directory listing.
			id := selection.setup.SetupID
			rec, in, e := mainLoad(r, j, project, id)
			if e == nil {
				if selected, e := inspectMainIntent(r, j, rec, in, false, &selection); e == nil {
					out.Selected = &selected
				}
			}
		}
	} else if rec, _, _, e := selectedMainRecord(r, j, project); e == nil {
		out.Retirement = mainDeletionState(j, project, rec)
	}
	return out, workOutputBound(out)
}
