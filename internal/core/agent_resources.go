package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ZanzyTHEbar/faults-go"
)

type shellInfo struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Cwd      string `json:"cwd"`
	PID      int    `json:"pid,omitempty"`
	Exit     *int   `json:"exit,omitempty"`
	Signal   string `json:"signal,omitempty"`
	Metadata struct {
		SessionID string `json:"sessionID"`
	} `json:"metadata"`
	Time struct {
		Started   float64 `json:"started"`
		Completed float64 `json:"completed,omitempty"`
	} `json:"time"`
}

type resourceSnapshot struct {
	Shells      []shellInfo `json:"shells"`
	RunningPTYs int         `json:"running_ptys"`
}

func locationQuery(directory string) string {
	return "?" + url.Values{"location[directory]": {directory}}.Encode()
}

// Never persist data from a mistakenly resolved peer location. Each job has an
// exclusive worktree; runtime shell metadata further ties exact IDs to session.
func (s *store) locationRead(j Job, route string, out any) error {
	b, err := apiCall(j.Agent.Worktree, "get", route+locationQuery(j.Agent.Worktree), nil)
	if err != nil {
		return err
	}
	var envelope struct {
		Location struct {
			Directory string `json:"directory"`
		} `json:"location"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &envelope); err != nil {
		return err
	}
	if envelope.Location.Directory != j.Agent.Worktree || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return diagnostic(faults.CodeRuntimeFailed, "runtime resource location/envelope mismatch; inspect exact owned location")
	}
	return json.Unmarshal(envelope.Data, out)
}

func ownedShell(j Job, shell shellInfo) bool {
	rel, err := filepath.Rel(j.Agent.Worktree, shell.Cwd)
	return err == nil && filepath.IsAbs(shell.Cwd) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && shell.Metadata.SessionID == j.Agent.SessionID && strings.HasPrefix(shell.ID, "sh_") && shell.Time.Started > 0
}

// Only cooperative runtime-tracked shells are supported. No host process scan,
// daemon detection, cancellation, or peer resource adoption occurs here.
func (s *store) observeResources(j Job, name string, references []string) (resourceSnapshot, error) {
	snapshot := resourceSnapshot{Shells: []shellInfo{}}
	knownPath := filepath.Join(s.dir, "jobs", j.ID, "known-shell-ids.json")
	var ids []string
	b, err := os.ReadFile(knownPath)
	if err == nil {
		if err := json.Unmarshal(b, &ids); err != nil {
			return snapshot, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return snapshot, err
	}
	var running []shellInfo
	if err := s.locationRead(j, "/api/shell", &running); err != nil {
		return snapshot, err
	}
	if running == nil {
		return snapshot, diagnostic(faults.CodeRuntimeFailed, "runtime shell list unavailable")
	}
	for _, shell := range running {
		if !ownedShell(j, shell) {
			return snapshot, diagnostic(faults.CodeRuntimeFailed, "unexpected shell ownership in dedicated worktree; inspect without adopting")
		}
		ids = append(ids, shell.ID)
	}
	ids = append(ids, references...)
	unique := map[string]bool{}
	for _, id := range ids {
		if !strings.HasPrefix(id, "sh_") || strings.ContainsAny(id, "/?#") {
			return snapshot, diagnostic(faults.CodeRuntimeFailed, "unsupported shell ID; inspect runtime evidence")
		}
		unique[id] = true
	}
	if len(unique) > 64 {
		return snapshot, diagnostic(faults.CodeResourceLimit, "more than 64 known shell resources; bounded observer cannot establish completion; inspect owned resources")
	}
	ids = ids[:0]
	for id := range unique {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	// Remember IDs before inspection: a missing/deleted known shell is not exit.
	b, err = json.Marshal(ids)
	if err != nil {
		return snapshot, err
	}
	if err := persistEvidence(knownPath, b); err != nil {
		return snapshot, err
	}
	pending := ""
	for _, id := range ids {
		var shell shellInfo
		if err := s.locationRead(j, "/api/shell/"+url.PathEscape(id), &shell); err != nil {
			return snapshot, diagnosticWrap(faults.CodeRuntimeFailed, fmt.Sprintf("known shell %s unobservable; do not release capacity", id), err)
		}
		if shell.ID != id || !ownedShell(j, shell) {
			return snapshot, diagnostic(faults.CodeRuntimeFailed, "exact shell ID/session/cwd mismatch")
		}
		snapshot.Shells = append(snapshot.Shells, shell)
		switch shell.Status {
		case "running":
			pending = "owned runtime shell " + id + " is running; wait for its exit, not the outer tool return"
		case "exited", "timeout", "killed":
			if shell.Time.Completed < shell.Time.Started || shell.Time.Completed <= 0 {
				pending = "runtime shell terminal time unavailable: " + id
			}
		default:
			pending = "unsupported runtime shell status for " + id
		}
	}
	var ptys []struct {
		ID     string `json:"id"`
		Cwd    string `json:"cwd"`
		Status string `json:"status"`
	}
	if err := s.locationRead(j, "/api/pty", &ptys); err != nil {
		return snapshot, err
	}
	if ptys == nil {
		return snapshot, diagnostic(faults.CodeRuntimeFailed, "runtime PTY list unavailable")
	}
	for _, pty := range ptys {
		if pty.Status != "exited" {
			snapshot.RunningPTYs++
			pending = "PTY " + pty.ID + " in owned location is running or unsupported; stop/inspect that exact resource before reconciliation"
		}
	}
	b, err = json.Marshal(snapshot)
	if err != nil {
		return snapshot, err
	}
	if _, err := s.agentEvidence(j.ID, name, b); err != nil {
		return snapshot, err
	}
	if pending != "" {
		return snapshot, diagnostic(faults.CodeRuntimeFailed, pending)
	}
	return snapshot, nil
}

func terminalTool(part messagePart) (string, error) {
	if part.ID == "" {
		return "", diagnostic(faults.CodeRuntimeFailed, "tool identity missing")
	}
	// OpenCode's executed flag means providerExecuted, not local execution.
	// A provider-hosted call cannot inherit the lifetime contract of a native
	// tool merely by reporting the same name.
	if part.Executed != nil && *part.Executed {
		return "", diagnostic(faults.CodeRuntimeFailed, "provider-executed tool is outside native foreground resource accounting")
	}
	if part.State.Status != "completed" && part.State.Status != "error" {
		return "", diagnostic(faults.CodeRuntimeFailed, fmt.Sprintf("tool %s/%s is %q; wait for completed/error", part.Name, part.ID, part.State.Status))
	}
	switch part.Name {
	case "read", "edit", "write", "patch", "glob", "grep":
		return "", nil // Known built-in foreground tools, not arbitrary plugins.
	case "shell":
		var input struct {
			Background bool `json:"background"`
		}
		var metadata struct {
			ShellID string `json:"shellID"`
			Status  string `json:"status"`
		}
		if err := json.Unmarshal(part.State.Input, &input); err != nil {
			return "", err
		}
		if len(part.State.Metadata) > 0 && string(part.State.Metadata) != "null" {
			if err := json.Unmarshal(part.State.Metadata, &metadata); err != nil {
				return "", err
			}
		}
		if metadata.Status != "" && metadata.Status != "completed" && metadata.Status != "running" {
			return "", diagnostic(faults.CodeRuntimeFailed, fmt.Sprintf("unsupported shell tool metadata status %q; inspect runtime resource", metadata.Status))
		}
		if (input.Background || metadata.Status == "running") && metadata.ShellID == "" && part.State.Status != "error" {
			return "", diagnostic(faults.CodeRuntimeFailed, "background shell has no structural shellID; inspect unsupported resource, never infer exit from tool completion")
		}
		return metadata.ShellID, nil
	default:
		return "", diagnostic(faults.CodeRuntimeFailed, fmt.Sprintf("unsupported tool %q; resource lifetime not accounted for", part.Name))
	}
}
