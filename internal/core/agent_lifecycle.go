package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/ZanzyTHEbar/faults-go"

	"ocq/internal/core/dbquery"
)

func ownershipMetadata(j Job) any {
	sum := sha256.Sum256([]byte(j.agentRequest.Prompt))
	return map[string]any{"ocq": map[string]any{"job_id": j.ID, "request_digest": j.Digest, "prompt_digest": hex.EncodeToString(sum[:]), "owner_token": j.Agent.Token}}
}

func validateSession(j Job, info sessionInfo) error {
	r := j.agentRequest
	if info.ID != j.Agent.SessionID || info.ParentID != "" || info.Location.Directory != j.Agent.Worktree || info.Agent != r.Agent || info.Model != r.Model || !jsonEqual(info.Metadata, ownershipMetadata(j)) || !reflect.DeepEqual(info.Permissions, r.Permissions) || !jsonEqual(info.Permissions, r.Permissions) || info.ProjectID == "" {
		return diagnostic(faults.CodeRuntimeFailed, "exact session/location/agent/model/permissions/ownership metadata mismatch")
	}
	if j.Agent.ProjectID != "" && info.ProjectID != j.Agent.ProjectID {
		return diagnostic(faults.CodeRuntimeFailed, "session project identity changed")
	}
	return nil
}

func (s *store) launchAgent(j Job) error {
	a, err := s.agentOwnership(j.ID)
	if err != nil {
		return err
	}
	j.Agent = a
	r := j.agentRequest
	branch := "ocq-" + j.ID
	baseDir := filepath.Join(s.dir, "worktrees")
	if err := privateDir(baseDir); err != nil {
		return err
	}
	config, _ := json.Marshal(map[string]string{"worktree_dir": baseDir})
	configPath, err := s.agentEvidence(j.ID, "workmux-config", config)
	if err != nil {
		return err
	}
	args := []string{"add", branch, "--headless", "--json", "--no-hooks", "--no-file-ops", "--base", r.Base, "--name", branch, "--config", configPath}
	if err := s.agentIntent(j.ID, "worktree", map[string]any{"repo": r.Repo, "argv": args, "base": r.Base}); err != nil {
		return err
	}
	c := exec.Command("workmux", args...)
	c.Dir = r.Repo
	c.Env, err = provisionEnvironment()
	if err != nil {
		return err
	}
	b, err := c.Output()
	if err != nil {
		return s.agentUnknown(j.ID, "Workmux creation acknowledgment unavailable; retained unknown; do not adopt by branch/name or retry", false)
	}
	var receipt struct {
		Schema int    `json:"schema_version"`
		Handle string `json:"handle"`
		Branch string `json:"branch"`
		Path   string `json:"worktree_path"`
		Cwd    string `json:"working_directory"`
		Base   string `json:"base_branch"`
		Hooks  *int   `json:"post_create_hooks_run"`
	}
	path, err := s.agentEvidence(j.ID, "worktree-response", b)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &receipt); err != nil {
		return s.agentUnknown(j.ID, "Workmux receipt schema unavailable; no retry", false)
	}
	wanted := filepath.Join(baseDir, branch)
	if receipt.Schema != 1 || receipt.Branch != branch || receipt.Handle != branch || receipt.Path != wanted || receipt.Cwd != wanted || receipt.Base != r.Base || receipt.Hooks == nil || *receipt.Hooks != 0 {
		return s.agentUnknown(j.ID, "Workmux receipt failed exact schema/path/branch/base/no-hooks validation; no retry", false)
	}
	real, err := filepath.EvalSymlinks(wanted)
	if err != nil || real != wanted {
		return s.agentUnknown(j.ID, "Workmux path is missing or not canonical; ownership unresolved", false)
	}
	head, err := gitRead(wanted, "rev-parse", "HEAD")
	if err != nil || head != r.Base {
		return s.agentUnknown(j.ID, "worktree HEAD does not equal pinned base", false)
	}
	checkedBranch, err := gitRead(wanted, "symbolic-ref", "--short", "HEAD")
	if err != nil || checkedBranch != branch {
		return s.agentUnknown(j.ID, "worktree branch mismatch", false)
	}
	common, err := gitRead(r.Repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	actual, err := gitRead(wanted, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || actual != common {
		return s.agentUnknown(j.ID, "worktree repository identity mismatch", false)
	}
	if err := s.write(func(c *sql.Conn) error {
		q := dbquery.New(c)
		if err := q.SetAgentWorktree(context.Background(), dbquery.SetAgentWorktreeParams{Worktree: wanted, JobID: j.ID}); err != nil {
			return err
		}
		return q.SetLaneWorktree(context.Background(), dbquery.SetLaneWorktreeParams{FsforkPath: wanted, OwnerJobID: j.ID})
	}); err != nil {
		return err
	}
	if err := s.agentAck(j.ID, "worktree", path, "worktree_owned"); err != nil {
		return err
	}
	j.Agent.Worktree = wanted
	body := map[string]any{"id": a.SessionID, "title": "ocq " + j.ID, "agent": r.Agent, "model": r.Model, "location": map[string]string{"directory": wanted}, "metadata": ownershipMetadata(j), "permissions": r.Permissions}
	if err := s.agentIntent(j.ID, "session", body); err != nil {
		return err
	}
	b, err = apiCall(wanted, "post", "/api/session", body)
	if err != nil {
		return s.agentUnknown(j.ID, "session creation acknowledgment unavailable; inspect only preallocated exact session ID; no POST retry", false)
	}
	path, err = s.agentEvidence(j.ID, "session-create-response", b)
	if err != nil {
		return err
	}
	var created sessionInfo
	if err := decodeData(b, &created); err != nil {
		return s.agentUnknown(j.ID, "installed session-create response schema unavailable", false)
	}
	if err := validateSession(j, created); err != nil {
		return s.agentUnknown(j.ID, err.Error(), false)
	}
	if err := s.verifyProjectSessionPolicy(context.Background(), j, created); err != nil {
		return s.agentUnknown(j.ID, err.Error(), false)
	}
	j.Agent.ProjectID = created.ProjectID
	var inspected sessionInfo
	if err := s.inspectAPI(j, "session-before-prompt", "/api/session/"+a.SessionID, &inspected); err != nil {
		return s.agentUnknown(j.ID, "cannot verify exact session ownership before prompt; prompt not sent", false)
	}
	if err := validateSession(j, inspected); err != nil {
		return s.agentUnknown(j.ID, err.Error(), false)
	}
	if err := s.verifyProjectLaunchGate(context.Background(), j, inspected); err != nil {
		return s.agentUnknown(j.ID, err.Error(), false)
	}
	initial, err := s.transcript(j)
	if err != nil {
		return s.agentFailure(j, err)
	}
	var initialInbox []inboxItem
	if err := s.inspectAPI(j, "inbox-before-prompt", "/api/session/"+a.SessionID+"/inbox", &initialInbox); err != nil {
		return s.agentFailure(j, err)
	}
	for _, m := range initial {
		if m.Type != "system" && m.Type != "synthetic" {
			return s.interference(j, "new dedicated session already has execution history; own prompt not sent")
		}
	}
	for _, in := range initialInbox {
		if in.SessionID != a.SessionID || (in.Type != "system" && in.Type != "synthetic") {
			return s.interference(j, "new dedicated session already has execution input; own prompt not sent")
		}
	}
	if _, err := s.observeResources(j, "resources-before-prompt", nil); err != nil {
		return s.agentFailure(j, err)
	}
	if err := s.write(func(c *sql.Conn) error {
		return dbquery.New(c).SetAgentProject(context.Background(), dbquery.SetAgentProjectParams{ProjectID: inspected.ProjectID, JobID: j.ID})
	}); err != nil {
		return err
	}
	if err := s.agentAck(j.ID, "session", path, "session_owned"); err != nil {
		return err
	}
	prompt := map[string]any{"id": a.PromptID, "text": r.Prompt, "metadata": ownershipMetadata(j)}
	if err := s.agentIntent(j.ID, "prompt", prompt); err != nil {
		return err
	}
	b, err = apiCall(wanted, "post", "/api/session/"+a.SessionID+"/prompt", prompt)
	if err != nil {
		return s.agentUnknown(j.ID, "prompt acknowledgment unavailable; exact-ID API inspection only, never resend", false)
	}
	path, err = s.agentEvidence(j.ID, "prompt-response", b)
	if err != nil {
		return err
	}
	var accepted inboxItem
	if err := decodeData(b, &accepted); err != nil {
		return s.agentUnknown(j.ID, "installed prompt acknowledgment schema unavailable; never resend", false)
	}
	if !matchingInput(j, accepted) {
		return s.agentUnknown(j.ID, "prompt acknowledgment identity/payload mismatch; never resend", false)
	}
	if err := s.agentAck(j.ID, "prompt", path, "prompt_accepted"); err != nil {
		return err
	}
	return s.write(func(c *sql.Conn) error {
		return dbquery.New(c).MarkAgentRunning(context.Background(), j.ID)
	})
}

func matchingInput(j Job, in inboxItem) bool {
	return in.ID == j.Agent.PromptID && in.SessionID == j.Agent.SessionID && in.Type == "user" && in.Payload.Text == j.agentRequest.Prompt && jsonEqual(in.Payload.Metadata, ownershipMetadata(j)) && len(in.Payload.Files)+len(in.Payload.Agents)+len(in.Payload.Skills) == 0
}

func (s *store) interference(j Job, reason string) error {
	return s.write(func(c *sql.Conn) error {
		q := dbquery.New(c)
		if err := q.MarkSessionInterference(context.Background(), j.ID); err != nil {
			return err
		}
		return q.MarkInterferenceUnknown(context.Background(), dbquery.MarkInterferenceUnknownParams{Reason: reason, ID: j.ID})
	})
}

func (s *store) agentFailure(j Job, err error) error {
	return s.agentUnknown(j.ID, fmt.Sprintf("API-only inspection incomplete: %s; capacity and ownership retained", err), false)
}

// Workmux --no-hooks controls Workmux hooks. Disable Git post-checkout hooks too,
// scoped to this child only; preserve all other normal discovery/auth context.
func provisionEnvironment() ([]string, error) {
	env := os.Environ()
	count := 0
	if value := os.Getenv("GIT_CONFIG_COUNT"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return nil, diagnostic(faults.CodeConfigInvalid, "invalid inherited Git configuration count")
		}
		count = n
	}
	filtered := env[:0]
	for _, entry := range env {
		if !strings.HasPrefix(entry, "GIT_CONFIG_COUNT=") {
			filtered = append(filtered, entry)
		}
	}
	return append(filtered, "GIT_CONFIG_COUNT="+strconv.Itoa(count+1), "GIT_CONFIG_KEY_"+strconv.Itoa(count)+"=core.hooksPath", "GIT_CONFIG_VALUE_"+strconv.Itoa(count)+"=/dev/null"), nil
}
