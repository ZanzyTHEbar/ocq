package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
)

// Append-only selection edge. Original journal contracts and operations keep
// their bytes; explicit refresh replaces the ONE active native instruction value.
// SQL still selects the same Main setup and session.
type mainRoleSuccessor struct {
	Version     int    `json:"version"`
	OperationID string `json:"operation_id"`
	Intent      string `json:"intent_sha256"`
	Role        string `json:"role_sha256"`
}

func mainRoleSuccessorFile(previous string) string {
	return ".main-role-" + previous + ".successor.json"
}

func readMainRoleSuccessor(j *os.File, previous string) (mainRoleSuccessor, bool, error) {
	var edge mainRoleSuccessor
	b, err := workRead(j, mainRoleSuccessorFile(previous))
	if os.IsNotExist(err) {
		return edge, false, nil
	}
	if err != nil {
		return edge, false, err
	}
	if _, err = decodeStrictJSONObject(b, WorkResponseBytes, &edge); err != nil ||
		edge.Version != 1 || !workID(edge.OperationID) || !isValidHexLen(edge.Intent, 64) || !isValidHexLen(edge.Role, 64) {
		return edge, true, errors.New("Main contract successor evidence corrupt; never guess selection")
	}
	return edge, true, nil
}

func selectedMainRole(r *Reader, j *os.File, project string, rec mainRecord) (mainRecord, error) {
	for n := 0; n < workIntentLimit; n++ {
		edge, present, err := readMainRoleSuccessor(j, rec.Role.SHA256)
		if err != nil || !present {
			return rec, err
		}
		next, in, err := mainLoad(r, j, project, edge.OperationID)
		if err != nil || in.Mode != "role-refresh" || in.ExpectedRoleSHA256 != rec.Role.SHA256 ||
			next.Digest != edge.Intent || next.Role.SHA256 != edge.Role || next.Role.PreviousSHA256 != rec.Role.SHA256 ||
			next.SetupID != rec.SetupID || next.SessionID != rec.SessionID || next.Repo != rec.Repo ||
			next.Common != rec.Common || next.Branch != rec.Branch || !jsonEqual(next.Role.Selection, rec.Role.Selection) {
			return rec, errors.New("Main contract successor does not match exact immutable setup/selection")
		}
		rec.Role = next.Role
	}
	return rec, errors.New("Main contract successor chain bound reached")
}

func roleRefreshEdge(rec mainRecord, in MainPrepareRequest) mainRoleSuccessor {
	return mainRoleSuccessor{Version: 1, OperationID: in.OperationID, Intent: rec.Digest, Role: rec.Role.SHA256}
}

func inspectMainRoleRefresh(j *os.File, rec mainRecord, in MainPrepareRequest, out MainStatus) (MainStatus, error) {
	edge, present, err := readMainRoleSuccessor(j, in.ExpectedRoleSHA256)
	if err != nil {
		return out, err
	}
	out.State, out.Reason = "prepared", "operating-contract replacement prepared only; no active instructions, profile, branch, binding or input changed"
	if _, attempted := mainAttempt(j, in.OperationID, "role"); attempted {
		out.State, out.Reason = "role_attempted_unknown", "one instructions attempt retained; exact GET recovery only, never repeat PUT"
	}
	if present {
		if edge != roleRefreshEdge(rec, in) {
			out.State, out.Reason = "blocked", "another explicitly confirmed successor selected; this operation cannot replace it"
		} else {
			out.State, out.Reason = "ready", "explicit operating-contract successor selected under one active key; journal history retained; no prompt or profile/binding change"
		}
	}
	return out, workOutputBound(out)
}

func startMainRoleRefresh(r *Reader, j *composeJournal, rec mainRecord, in MainPrepareRequest, claim workClaim, recoveryOnly bool) (MainStatus, error) {
	if rec.Role.PreviousSHA256 != in.ExpectedRoleSHA256 || rec.RegistrationJSON != "" || rec.PromptJSON != "" || rec.PromptID != "" {
		return MainStatus{}, errors.New("contract refresh may not register, rebind or send input")
	}
	if edge, present, err := readMainRoleSuccessor(j.dir, in.ExpectedRoleSHA256); err != nil {
		return MainStatus{}, err
	} else if present {
		if edge != roleRefreshEdge(rec, in) {
			return MainStatus{}, errors.New("Main contract CAS lost; never replace a selected successor")
		}
		if err = j.syncFile(mainRoleSuccessorFile(in.ExpectedRoleSHA256)); err != nil {
			return MainStatus{}, err
		}
		return inspectMainRecord(r, j.dir, in.ProjectID, in.OperationID, false)
	}
	selected, _, binding, err := selectedMain(r, j.dir, in.ProjectID)
	if err != nil || binding != rec.Binding || selected.Role.SHA256 != in.ExpectedRoleSHA256 ||
		selected.SessionID != rec.SessionID || selected.SetupID != rec.SetupID ||
		!jsonEqual(selected.Role.Selection, rec.Role.Selection) {
		return MainStatus{}, errors.Join(err, errors.New("Main contract/binding CAS changed; no instructions update"))
	}
	_, _, image, err := dispatcherImage(rec.Role.CLI)
	if err != nil || image != rec.Role.CLISHA256 {
		return MainStatus{}, errors.New("successor CLI image changed/unavailable")
	}
	queue, store, err := r.WorkQueueContext()
	if err != nil || queue != rec.Role.QueueIdentity || store != rec.Role.StoreContext {
		return MainStatus{}, errors.New("successor queue/store identity changed")
	}
	common, head, _, err := inspectCanonicalMain(rec.Repo, rec.Branch)
	if err != nil || common != rec.Common || head != rec.Head {
		return MainStatus{}, errors.New("canonical Main changed since contract preview; no branch/anchor change is attempted")
	}
	service, err := discoverNativeService(rec.Repo)
	if err != nil || service != rec.Server {
		return MainStatus{}, errors.New("Main service changed; no instructions mutation")
	}
	checkSession := func() error {
		s, err := nativeSession(rec.Repo, service, rec.SessionID)
		if err != nil {
			return err
		}
		if err = nativeIdentity(s, rec.SessionID, rec.Repo, rec.ProjectID, nativeHash(json.RawMessage(rec.Metadata))); err != nil {
			return err
		}
		if nativeProfile(s) != rec.ProfileSHA {
			return errors.New("human Main profile changed since preview; never restore or replace it")
		}
		return mainContractIdle(r, j.dir, rec, s)
	}
	if err = checkSession(); err != nil {
		return MainStatus{}, err
	}
	seen, present, err := readMainRoleEntry(context.Background(), rec)
	if err != nil || !present {
		return MainStatus{}, errors.Join(err, errors.New("active contract unavailable; no repair or overwrite"))
	}
	_, attempted := mainAttempt(j.dir, in.OperationID, "role")
	if jsonEqual(seen, rec.Role) {
		if !attempted {
			return MainStatus{}, errors.New("successor appeared without this operation's attempt; never adopt another writer's change")
		}
	} else {
		if !jsonEqual(seen, selected.Role) {
			return MainStatus{}, errors.New("exact predecessor value changed; no instructions overwrite")
		}
		if recoveryOnly || attempted {
			return MainStatus{}, errors.New("successor not proven by exact GET; never repeat PUT")
		}
		fresh, err := mainStage(j, in.OperationID, "role", claim)
		if err != nil || !fresh {
			return MainStatus{}, errors.Join(err, errors.New("contract replacement attempt already retained"))
		}
		// The API has no conditional PUT. Require an operator-exclusive update
		// window, and fence the one attempt with exact predecessor/idle reads.
		if err = checkSession(); err != nil {
			return MainStatus{}, err
		}
		before, present, err := readMainRoleEntry(context.Background(), rec)
		if err != nil || !present || !jsonEqual(before, selected.Role) {
			return MainStatus{}, errors.Join(err, errors.New("predecessor changed before replacement; attempt held, no retry"))
		}
		_, putErr := nativeCall(rec.Repo, service, "put", mainRolePath(rec)+"/"+mainRoleKey, map[string]any{"value": rec.Role})
		matches, _, readErr := observeMainRole(rec)
		if readErr != nil || !matches {
			return MainStatus{}, errors.Join(putErr, readErr, errors.New("contract replacement read-back unproven; GET recovery only"))
		}
	}
	if err = retainMainRoleObservation(j, rec, in.OperationID); err != nil {
		return MainStatus{}, err
	}
	// The native entry and journal selector cannot be one atomic transaction.
	// Unknown read-back keeps the old selector. Recovery can only GET the exact
	// new entry, then durably commit this edge under the same journal lock.
	if err = checkSession(); err != nil {
		return MainStatus{}, err
	}
	if matches, _, err := observeMainRole(rec); err != nil || !matches {
		return MainStatus{}, errors.Join(err, errors.New("successor changed during update; selection held"))
	}
	if err = nativeJournalRoom(j.dir, 1); err != nil {
		return MainStatus{}, err
	}
	w, err := OpenExistingProjectIntake(r.store.dir)
	if err != nil {
		return MainStatus{}, err
	}
	defer w.Close()
	// Serialize the short durable selector write against SQL binding CAS too.
	// No SQL row, receipt, observation revision or binding is changed here.
	err = w.store.write(func(_ *sql.Conn) error {
		if current, err := workBinding(r, in.ProjectID); err != nil || current != rec.Binding {
			return errors.Join(err, errors.New("binding changed during update; selection held"))
		}
		return j.create(mainRoleSuccessorFile(in.ExpectedRoleSHA256), roleRefreshEdge(rec, in))
	})
	if err != nil {
		return MainStatus{}, err
	}
	return inspectMainRecord(r, j.dir, in.ProjectID, in.OperationID, false)
}

// An unprompted Main has no terminal idle message. Do not manufacture one by
// sending its prepared goal merely to update ordinary operating context.
// This exception is Main-contract-only; worker/client completion rules do not
// change. Any past input attempt, usage, changed session or pending input holds.
func mainContractIdle(r *Reader, j *os.File, rec mainRecord, before map[string]any) error {
	body, err := nativeCall(rec.Repo, rec.Server, "get", "/api/session/"+rec.SessionID+"/message?limit=1&order=desc", nil)
	if err != nil {
		return err
	}
	var latest []message
	if err = decodeData(body, &latest); err != nil {
		return err
	}
	if len(latest) != 0 {
		return nativeClientIdle(rec.Repo, rec.Server, rec.SessionID, before)
	}
	t, ok := before["time"].(map[string]any)
	created, valid := t["created"].(float64)
	zeroTokens := map[string]any{"input": 0, "output": 0, "reasoning": 0, "cache": map[string]any{"read": 0, "write": 0}}
	if !ok || !valid || created <= 0 || t["updated"] != created || before["cost"] != float64(0) || !jsonEqual(before["tokens"], zeroTokens) {
		return errors.New("empty Main is not provably pristine; no prompt or context repair")
	}
	if _, err = j.Seek(0, 0); err != nil {
		return err
	}
	entries, err := workEntries(j)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ".main-") || !strings.HasSuffix(name, ".intent.json") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(name, ".main-"), ".intent.json")
		history, _, err := mainLoad(r, j, rec.Role.ProjectID, id)
		if err != nil {
			return err
		}
		if history.SessionID == rec.SessionID {
			if _, attempted := mainAttempt(j, id, "prompt"); attempted {
				return errors.New("empty Main has a retained input attempt; no pristine exception")
			}
		}
	}
	body, err = nativeCall(rec.Repo, rec.Server, "get", "/api/session/active", nil)
	var active map[string]json.RawMessage
	if err != nil || decodeData(body, &active) != nil {
		return errors.New("pristine Main active state unavailable")
	}
	if _, running := active[rec.SessionID]; running {
		return errors.New("pristine Main became active")
	}
	for _, suffix := range []string{"/inbox", "/permission", "/form", "/message?limit=1&order=desc"} {
		body, err = nativeCall(rec.Repo, rec.Server, "get", "/api/session/"+rec.SessionID+suffix, nil)
		var pending []json.RawMessage
		if err != nil || decodeData(body, &pending) != nil || len(pending) != 0 {
			return errors.New("pristine Main has pending/unknown input or history")
		}
	}
	after, err := nativeSession(rec.Repo, rec.Server, rec.SessionID)
	if err != nil || !jsonEqual(before, after) {
		return errors.New("pristine Main changed during inspection")
	}
	return nil
}

func recoverMainRoleRefresh(dir, project, id string) (MainStatus, bool, error) {
	if !isValidProjectID(project) || !workID(id) {
		return MainStatus{}, true, errors.New("invalid exact Main recovery pointer")
	}
	r, err := OpenReader(dir)
	if err != nil {
		return MainStatus{}, true, err
	}
	defer r.Close()
	read, err := workReadDir(r, project)
	if err != nil {
		return MainStatus{}, true, err
	}
	rec, in, err := mainLoad(r, read, project, id)
	read.Close()
	if err != nil || in.Mode != "role-refresh" {
		return MainStatus{}, err != nil, err
	}
	j, err := openLocalJournal(dir, workJournal(project))
	if err != nil {
		return MainStatus{}, true, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return MainStatus{}, true, err
	}
	claim, attempted := mainAttempt(j.dir, id, "role")
	if !attempted {
		out, err := inspectMainRecord(r, j.dir, project, id, false)
		return out, true, err
	}
	if claim.Intent != rec.Digest || claim.Payload != rec.Digest || !isValidHexLen(claim.Preview, 64) {
		return MainStatus{}, true, errors.New("contract refresh attempt evidence mismatch")
	}
	out, err := startMainRoleRefresh(r, j, rec, in, claim, true)
	return out, true, err
}
