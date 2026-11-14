package core

import (
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"syscall"
)

func DecodeClientTerminalReceipt(raw []byte) (ClientTerminalReceipt, error) {
	var receipt ClientTerminalReceipt
	_, err := decodeStrictJSONObject(raw, 16<<10, &receipt)
	return receipt, err
}

func clientContext(r *Reader, p ClientPointer, c ClientActionContext) error {
	if c.Protocol != ClientProtocol || c.Action != "client-open" || c.Space != p.Space {
		return errors.New("exact native client context/space required")
	}
	binding, err := workBinding(r, p.ProjectID)
	if err != nil {
		return err
	}
	if nativeHash(binding) != p.BindingSHA256 {
		return errors.New("native client binding changed")
	}
	copy := WorkActionContext(c)
	copy.Protocol = WorkProtocol
	copy.Action = "confirm"
	return validateWorkContext(r, binding, copy)
}

func inspectClient(r *Reader, j *os.File, project, digest string) (ClientStatus, error) {
	p, err := loadClientPointer(r, j, project, digest)
	if err != nil {
		return ClientStatus{}, err
	}
	out := ClientStatus{Protocol: ClientProtocol, ProjectID: project, PointerSHA256: digest, State: "ready_to_open", Pointer: p, CurrentRoute: "unverified"}
	var armed workClaim
	for _, stage := range []struct{ name, state string }{{"armed", "armed"}, {"attempt", "attempted_unknown"}} {
		b, e := workRead(j, clientFile(digest, stage.name))
		if errors.Is(e, syscall.ENOENT) {
			continue
		}
		var claim workClaim
		if e != nil || json.Unmarshal(b, &claim) != nil || claim.Intent != digest || claim.Payload != digest || claim.Preview != digest || stage.name == "attempt" && (out.State != "armed" || claim != armed) {
			out.State = "blocked"
			return out, nil
		}
		armed = claim
		out.State = stage.state
	}
	b, e := workRead(j, clientFile(digest, "terminal"))
	if e == nil {
		var receipt ClientTerminalReceipt
		if json.Unmarshal(b, &receipt) != nil || out.State != "attempted_unknown" || validClientReceipt(p, ClientActionContext(armed.Context), receipt) != nil {
			out.State = "blocked"
			return out, nil
		}
		out.Terminal = &receipt
		out.State = "terminal_recorded"
	} else if !errors.Is(e, syscall.ENOENT) {
		out.State = "blocked"
	}
	return out, workOutputBound(out)
}

func InspectProjectClient(dir, project, digest string) (ClientStatus, error) {
	if !isValidProjectID(project) || !isValidHexLen(digest, 64) {
		return ClientStatus{}, errors.New("invalid client pointer")
	}
	r, err := OpenReader(dir)
	if err != nil {
		return ClientStatus{}, err
	}
	defer r.Close()
	j, err := workReadDir(r, project)
	if err != nil {
		return ClientStatus{}, err
	}
	defer j.Close()
	return inspectClient(r, j, project, digest)
}

// Project-scoped discovery of retained navigation evidence, never live terminal
// adoption or a latest-session/label search. Every returned attempt is consumed.
func ListProjectClients(dir, project string) ([]ClientStatus, error) {
	out := []ClientStatus{}
	if !isValidProjectID(project) {
		return out, errors.New("invalid native client project")
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
		if strings.HasPrefix(name, ".client-") && strings.HasSuffix(name, ".pointer.json") {
			id := strings.TrimSuffix(strings.TrimPrefix(name, ".client-"), ".pointer.json")
			if isValidHexLen(id, 64) {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) > workIntentLimit {
		return out, errors.New("native client history bound reached")
	}
	sort.Strings(ids)
	for _, id := range ids {
		s, e := inspectClient(r, j, project, id)
		if e != nil {
			return out, e
		}
		out = append(out, s)
	}
	return out, workOutputBound(out)
}

// VerifyProjectClient verifies backend and launch context, NOT the terminal's
// current TUI route. Focus must say "Focus existing inspection terminal".
// It never spawns, resumes or prompts.
func VerifyProjectClient(dir, project, digest string, c ClientActionContext) (ClientStatus, error) {
	r, err := OpenReader(dir)
	if err != nil {
		return ClientStatus{}, err
	}
	defer r.Close()
	j, err := workReadDir(r, project)
	if err != nil {
		return ClientStatus{}, err
	}
	defer j.Close()
	status, err := inspectClient(r, j, project, digest)
	if err != nil {
		return status, err
	}
	if err = clientContext(r, status.Pointer, c); err != nil {
		return status, err
	}
	_, _, err = verifyClientPointer(r, j, status.Pointer)
	return status, err
}

func ClientCLIContext(dir, project, digest string) (ClientActionContext, error) {
	status, err := InspectProjectClient(dir, project, digest)
	if err != nil {
		return ClientActionContext{}, err
	}
	r, err := OpenReader(dir)
	if err != nil {
		return ClientActionContext{}, err
	}
	defer r.Close()
	binding, err := workBinding(r, project)
	if err != nil {
		return ClientActionContext{}, err
	}
	exe, err := os.Executable()
	if err != nil {
		return ClientActionContext{}, err
	}
	_, _, image, err := dispatcherImage(exe)
	if err != nil {
		return ClientActionContext{}, err
	}
	return ClientActionContext{Protocol: ClientProtocol, Peer: "local", Space: status.Pointer.Space, Folder: binding.RepoRoot, QueueIdentity: status.Pointer.QueueIdentity, StoreContext: status.Pointer.StoreContext, ImageSHA256: image, ImageRole: "cli", Action: "client-open"}, nil
}

func ArmProjectClient(dir, project, digest string, c ClientActionContext) (ClientStatus, error) {
	return markProjectClient(dir, project, digest, c, false)
}
func AttemptProjectClient(dir, project, digest string, c ClientActionContext) (ClientStatus, error) {
	return markProjectClient(dir, project, digest, c, true)
}

func markProjectClient(dir, project, digest string, c ClientActionContext, attempt bool) (ClientStatus, error) {
	if !isValidProjectID(project) || !isValidHexLen(digest, 64) {
		return ClientStatus{}, errors.New("invalid client pointer")
	}
	j, err := openLocalJournal(dir, workJournal(project))
	if err != nil {
		return ClientStatus{}, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return ClientStatus{}, err
	}
	r, err := OpenReader(dir)
	if err != nil {
		return ClientStatus{}, err
	}
	defer r.Close()
	p, err := loadClientPointer(r, j.dir, project, digest)
	if err != nil {
		return ClientStatus{}, err
	}
	if err = clientContext(r, p, c); err != nil {
		return ClientStatus{}, err
	}
	status, err := inspectClient(r, j.dir, project, digest)
	if err != nil {
		return status, err
	}
	claim := workClaim{Intent: digest, Payload: digest, Preview: digest, Context: WorkActionContext(c)}
	if b, e := workRead(j.dir, clientFile(digest, "armed")); e == nil {
		var old workClaim
		if json.Unmarshal(b, &old) != nil || old != claim {
			return status, errors.New("native client authorization context changed")
		}
	}
	if status.State == "attempted_unknown" || status.State == "terminal_recorded" {
		return status, nil
	}
	if status.State == "armed" && !attempt {
		return status, nil
	}
	expected, suffix := "ready_to_open", "armed"
	if attempt {
		expected, suffix = "armed", "attempt"
	}
	if status.State != expected {
		return status, errors.New("native client state does not authorize this stage")
	}
	if _, _, err = verifyClientPointer(r, j.dir, p); err != nil {
		return status, err
	}
	if err = j.syncFile(clientFile(digest, "pointer")); err != nil {
		return status, err
	}
	if attempt {
		if err = j.syncFile(clientFile(digest, "armed")); err != nil {
			return status, err
		}
	}
	if err = nativeJournalRoom(j.dir, 1); err != nil {
		return status, err
	}
	if err = j.create(clientFile(digest, suffix), claim); err != nil {
		return status, err
	}
	out, err := inspectClient(r, j.dir, project, digest)
	if err == nil && attempt {
		out.AttemptPermitted = true
	}
	return out, err
}

func validClientReceipt(p ClientPointer, c ClientActionContext, receipt ClientTerminalReceipt) error {
	if receipt.ReturnedTerminalID == "" || receipt.ReturnedTerminalID != receipt.ObservedTerminalID || len(receipt.ReturnedTerminalID) > 512 || strings.ContainsAny(receipt.ReturnedTerminalID, "\r\n\x00") || receipt.Peer != c.Peer || receipt.Cwd != p.Cwd || !jsonEqual(receipt.Argv, p.LauncherArgv) || receipt.Labels["ocq.handoff"] != p.PointerSHA256 || len(receipt.Labels) > 32 {
		return errors.New("exact returned/observed HC terminal, peer, argv, cwd and label receipt required")
	}
	for key, value := range receipt.Labels {
		if len(key) > 128 || len(value) > 1024 {
			return errors.New("HC receipt labels exceed bound")
		}
	}
	return workOutputBound(receipt)
}

func RecordProjectClient(dir, project, digest string, c ClientActionContext, receipt ClientTerminalReceipt) (ClientStatus, error) {
	if !isValidProjectID(project) || !isValidHexLen(digest, 64) {
		return ClientStatus{}, errors.New("invalid client pointer")
	}
	j, err := openLocalJournal(dir, workJournal(project))
	if err != nil {
		return ClientStatus{}, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return ClientStatus{}, err
	}
	r, err := OpenReader(dir)
	if err != nil {
		return ClientStatus{}, err
	}
	defer r.Close()
	p, err := loadClientPointer(r, j.dir, project, digest)
	if err != nil {
		return ClientStatus{}, err
	}
	status, err := inspectClient(r, j.dir, project, digest)
	if err != nil {
		return status, err
	}
	if err = validClientReceipt(p, c, receipt); err != nil {
		return status, err
	}
	if b, e := workRead(j.dir, clientFile(digest, "attempt")); e == nil {
		var claim workClaim
		if json.Unmarshal(b, &claim) != nil || claim.Context != WorkActionContext(c) {
			return status, errors.New("HC receipt authorization mismatch")
		}
		// Recording an already-created terminal is evidence retention, not a
		// fresh launch/focus authorization. Pin the consumed original context
		// and current queue/store identity, not a subsequently refreshed binding.
		if c.Protocol != ClientProtocol || c.Action != "client-open" || c.Space != p.Space || c.QueueIdentity != p.QueueIdentity || c.StoreContext != p.StoreContext {
			return status, errors.New("HC receipt original pointer context mismatch")
		}
		copy := WorkActionContext(c)
		copy.Protocol = WorkProtocol
		copy.Action = "confirm"
		if e = validateWorkContext(r, ProjectBinding{RepoRoot: claim.Context.Folder}, copy); e != nil {
			return status, e
		}
	} else {
		return status, errors.New("no exact native attempt")
	}
	if status.Terminal != nil {
		if !jsonEqual(*status.Terminal, receipt) {
			return status, errors.New("HC terminal receipt is immutable")
		}
		return status, clientReceiptContinuation(r, p, c)
	}
	if status.State != "attempted_unknown" {
		return status, errors.New("native client was not attempted")
	}
	if err = nativeJournalRoom(j.dir, 1); err != nil {
		return status, err
	}
	if err = j.create(clientFile(digest, "terminal"), receipt); err != nil {
		return status, err
	}
	recorded, err := inspectClient(r, j.dir, project, digest)
	return recorded, errors.Join(err, clientReceiptContinuation(r, p, c))
}

// The receipt is already durable before this UI continuation fence runs.
// Completed callers may show/focus a terminal after a successful Record call;
// stale context must therefore return an error WITHOUT losing its receipt.
// Stored Inspect remains available, while explicit Verify gates later focus.
func clientReceiptContinuation(r *Reader, p ClientPointer, c ClientActionContext) error {
	if err := clientContext(r, p, c); err != nil {
		return errors.Join(errors.New("HC receipt retained; current context changed, no launch/focus authorization"), err)
	}
	return nil
}
