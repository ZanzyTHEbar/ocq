package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"reflect"
	"strings"
	"time"

	"github.com/ZanzyTHEbar/faults-go"
)

type sessionInfo struct {
	ID        string     `json:"id"`
	ProjectID string     `json:"projectID"`
	ParentID  string     `json:"parentID"`
	Agent     string     `json:"agent"`
	Model     agentModel `json:"model"`
	Location  struct {
		Directory string `json:"directory"`
	} `json:"location"`
	Metadata    json.RawMessage  `json:"metadata"`
	Permissions []permissionRule `json:"permissions"`
	Time        struct {
		Updated float64 `json:"updated"`
	} `json:"time"`
}
type message struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Text     string            `json:"text"`
	Metadata json.RawMessage   `json:"metadata"`
	Files    []json.RawMessage `json:"files"`
	Agents   []json.RawMessage `json:"agents"`
	Skills   []json.RawMessage `json:"skills"`
	Agent    string            `json:"agent"`
	Model    agentModel        `json:"model"`
	Outcome  string            `json:"outcome"`
	Time     struct {
		Created   float64 `json:"created"`
		Completed float64 `json:"completed"`
	} `json:"time"`
	Content []messagePart `json:"content"`
}

// Tool input/output and metadata stay in private evidence, never stdout.
type messagePart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	Executed *bool  `json:"executed,omitempty"`
	State    struct {
		Status   string          `json:"status"`
		Input    json.RawMessage `json:"input,omitempty"`
		Content  json.RawMessage `json:"content,omitempty"`
		Error    json.RawMessage `json:"error,omitempty"`
		Metadata json.RawMessage `json:"metadata,omitempty"`
	} `json:"state"`
	Time struct {
		Created   float64 `json:"created"`
		Ran       float64 `json:"ran,omitempty"`
		Completed float64 `json:"completed,omitempty"`
	} `json:"time"`
}
type inboxItem struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
	Type      string `json:"type"`
	Payload   struct {
		Text     string            `json:"text"`
		Metadata json.RawMessage   `json:"metadata"`
		Files    []json.RawMessage `json:"files"`
		Agents   []json.RawMessage `json:"agents"`
		Skills   []json.RawMessage `json:"skills"`
	} `json:"payload"`
}

func jsonEqual(a, b any) bool {
	x, err := json.Marshal(a)
	if err != nil {
		return false
	}
	y, err := json.Marshal(b)
	if err != nil {
		return false
	}
	var left, right any
	return json.Unmarshal(x, &left) == nil && json.Unmarshal(y, &right) == nil && reflect.DeepEqual(left, right)
}

// The supported CLI owns shared-service discovery/authentication. Never replace
// it with an unauthenticated localhost client or a private/standalone server.
func apiCall(directory, method, path string, body any) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	args := []string{"api", method, path}
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, classifyDiagnostic(faults.CodeInvariantFailed, err)
		}
		args = append(args, "--data", string(b))
	}
	c := exec.CommandContext(ctx, "opencode", args...)
	c.Dir = directory
	b, err := c.Output() // normal environment preserves configured discovery/auth context
	if err != nil {
		return nil, diagnostic(faults.CodeRuntimeFailed, "OpenCode CLI request failed; mutation acknowledgment may be lost; never retry POST")
	}
	if !json.Valid(b) {
		return nil, diagnostic(faults.CodeRuntimeFailed, "OpenCode response was not JSON; acknowledgment uncertain")
	}
	return b, nil
}

func decodeData(b []byte, out any) error {
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return classifyDiagnostic(faults.CodeRuntimeFailed, err)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return diagnostic(faults.CodeRuntimeFailed, "installed API response has no data envelope")
	}
	return classifyDiagnostic(faults.CodeRuntimeFailed, json.Unmarshal(env.Data, out))
}

func getSession(ctx context.Context, directory, sessionID string) (sessionInfo, error) {
	var info sessionInfo
	if !strings.HasPrefix(sessionID, "ses_") || strings.ContainsAny(sessionID, "/?#\\ \t\r\n\x00") {
		return info, diagnostic(faults.CodeValidationFailed, "invalid OpenCode session ID")
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c := exec.CommandContext(callCtx, "opencode", "api", "get", "/api/session/"+url.PathEscape(sessionID))
	c.Dir = directory
	b, err := c.Output()
	if err != nil {
		return info, diagnostic(faults.CodeRuntimeFailed, "OpenCode session inspection failed")
	}
	if !json.Valid(b) {
		return info, diagnostic(faults.CodeRuntimeFailed, "OpenCode session response was not JSON")
	}
	if err := decodeData(b, &info); err != nil {
		return info, err
	}
	return info, nil
}

func getMessages(ctx context.Context, directory, sessionID string) ([]message, error) {
	if !strings.HasPrefix(sessionID, "ses_") || strings.ContainsAny(sessionID, "/?#\\ \t\r\n\x00") {
		return nil, diagnostic(faults.CodeValidationFailed, "invalid OpenCode session ID")
	}
	var all []message
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 1000; page++ {
		query := url.Values{"limit": {"100"}}
		if cursor == "" {
			query.Set("order", "asc")
		} else {
			query.Set("cursor", cursor)
		}
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		c := exec.CommandContext(callCtx, "opencode", "api", "get", "/api/session/"+url.PathEscape(sessionID)+"/message?"+query.Encode())
		c.Dir = directory
		b, err := c.Output()
		cancel()
		if err != nil {
			return nil, diagnostic(faults.CodeRuntimeFailed, "OpenCode session message inspection failed")
		}
		if !json.Valid(b) {
			return nil, diagnostic(faults.CodeRuntimeFailed, "OpenCode session message response was not JSON")
		}
		var response struct {
			Data   []message `json:"data"`
			Cursor *struct {
				Next *string `json:"next"`
			} `json:"cursor"`
		}
		if err := json.Unmarshal(b, &response); err != nil {
			return nil, classifyDiagnostic(faults.CodeRuntimeFailed, err)
		}
		if response.Cursor == nil || response.Data == nil {
			return nil, diagnostic(faults.CodeRuntimeFailed, "message pagination shape unavailable")
		}
		all = append(all, response.Data...)
		if response.Cursor.Next == nil {
			return all, nil
		}
		cursor = *response.Cursor.Next
		if cursor == "" || seen[cursor] {
			return nil, diagnostic(faults.CodeRuntimeFailed, "invalid/repeated transcript cursor")
		}
		seen[cursor] = true
	}
	return nil, diagnostic(faults.CodeResourceLimit, "transcript exceeds bounded inspection")
}

func getSessionList(ctx context.Context, directory, sessionID, suffix string, out any) error {
	if !strings.HasPrefix(sessionID, "ses_") || strings.ContainsAny(sessionID, "/?#\\ \t\r\n\x00") {
		return diagnostic(faults.CodeValidationFailed, "invalid OpenCode session ID")
	}
	switch suffix {
	case "permission", "form", "inbox":
	default:
		return diagnostic(faults.CodeValidationFailed, fmt.Sprintf("unsupported session list resource %q", suffix))
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c := exec.CommandContext(callCtx, "opencode", "api", "get", "/api/session/"+url.PathEscape(sessionID)+"/"+suffix)
	c.Dir = directory
	b, err := c.Output()
	if err != nil {
		return diagnostic(faults.CodeRuntimeFailed, fmt.Sprintf("OpenCode session %s inspection failed", suffix))
	}
	if !json.Valid(b) {
		return diagnostic(faults.CodeRuntimeFailed, fmt.Sprintf("OpenCode session %s response was not JSON", suffix))
	}
	return decodeData(b, out)
}

func (s *store) inspectAPI(j Job, name, path string, out any) error {
	b, err := apiCall(j.Agent.Worktree, "get", path, nil)
	if err != nil {
		return err
	}
	if out != nil {
		if err := decodeData(b, out); err != nil {
			return err
		}
	}
	_, err = s.agentEvidence(j.ID, name, b)
	return err
}

func (s *store) transcript(j Job) ([]message, error) {
	var all []message
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 1000; page++ {
		query := url.Values{"limit": {"100"}}
		if cursor == "" {
			query.Set("order", "asc")
		} else {
			query.Set("cursor", cursor)
		}
		b, err := apiCall(j.Agent.Worktree, "get", "/api/session/"+j.Agent.SessionID+"/message?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		var response struct {
			Data   []message `json:"data"`
			Cursor *struct {
				Next *string `json:"next"`
			} `json:"cursor"`
		}
		if err := json.Unmarshal(b, &response); err != nil {
			return nil, classifyDiagnostic(faults.CodeRuntimeFailed, err)
		}
		if response.Cursor == nil || response.Data == nil {
			return nil, diagnostic(faults.CodeRuntimeFailed, "message pagination shape unavailable")
		}
		if _, err := s.agentEvidence(j.ID, fmt.Sprintf("messages-page-%04d", page), b); err != nil {
			return nil, err
		}
		all = append(all, response.Data...)
		if response.Cursor.Next == nil {
			return all, nil
		}
		cursor = *response.Cursor.Next
		if cursor == "" || seen[cursor] {
			return nil, diagnostic(faults.CodeRuntimeFailed, "invalid/repeated transcript cursor")
		}
		seen[cursor] = true
	}
	return nil, diagnostic(faults.CodeResourceLimit, "transcript exceeds bounded inspection; ownership retained")
}
