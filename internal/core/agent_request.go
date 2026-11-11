package core

import (
	"bytes"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/ZanzyTHEbar/faults-go"
)

// Separate from commandRequest: v1 command canonical bytes must never change.
type agentRequest struct {
	Version         int              `json:"version"`
	Kind            string           `json:"kind"`
	Repo            string           `json:"repo"`
	Base            string           `json:"base"`
	Prompt          string           `json:"prompt"`
	Agent           string           `json:"agent"`
	Model           agentModel       `json:"model"`
	Permissions     []permissionRule `json:"permissions"`
	ProvisionPolicy string           `json:"provision_policy"`
	MemoryBytes     int64            `json:"memory_bytes"`
}
type agentModel struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Variant    string `json:"variant,omitempty"`
}
type permissionRule struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Effect   string `json:"effect"`
}

const provisionPolicy = "workmux-headless-no-hooks-no-file-ops-v1"

func gitRead(dir string, args ...string) (string, error) {
	c := exec.Command("git", args...)
	c.Dir = dir
	b, err := c.Output()
	if err != nil {
		// This diagnostic is also retained verbatim in worktree observations.
		return "", diagnosticWrap(faults.CodeRuntimeFailed, "git inspection failed", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// AgentSubmission supplies the complete agent request at the core boundary.
type AgentSubmission struct {
	Key, Repo, Base, Agent, Model string
	Prompt, Permissions           []byte
	MemoryBytes                   int64
}

// SubmitAgent pins the repository and durably records the supplied request.
func (q *Queue) SubmitAgent(in AgentSubmission) (_ Job, resultErr error) {
	defer func() { resultErr = classifyDiagnostic(faults.CodeRuntimeFailed, resultErr) }()
	data, err := canonicalAgentSubmission(in)
	if err != nil {
		return Job{}, err
	}
	return q.store.acceptCanonical(in.Key, data, true)
}

// Shared by CLI intake and standalone drafts/fresh admission. This performs
// existing repository/profile validation and v2 encoding, never an API call,
// provisioning, a session creation, a prompt or dispatch.
func canonicalAgentSubmission(in AgentSubmission) ([]byte, error) {
	return canonicalAgentSubmissionUsing(in, gitRead)
}

// The CLI keeps its original Git behavior. Standalone admission supplies its
// bounded read-only Git runner here, outside any queue write transaction.
func canonicalAgentSubmissionUsing(in AgentSubmission, readGit func(string, ...string) (string, error)) ([]byte, error) {
	if in.Key == "" || len(in.Key) > 1024 || !utf8.ValidString(in.Key) || in.Agent == "" || !utf8.ValidString(in.Agent) || !utf8.ValidString(in.Model) || in.MemoryBytes < 0 {
		return nil, faults.New(faults.CodeValidationFailed, "submit-agent requires --key, --repo, --prompt-file, --agent, --model, --permissions-file")
	}
	path, err := filepath.Abs(in.Repo)
	if err != nil {
		return nil, faults.Wrap(faults.CodeStorageFailed, "resolve repository path", err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return nil, faults.Wrap(faults.CodeStorageFailed, "resolve repository path", err)
	}
	path, err = readGit(path, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return nil, faults.Wrap(faults.CodeStorageFailed, "resolve repository root", err)
	}
	pinned, err := readGit(path, "rev-parse", "--verify", "--end-of-options", in.Base+"^{commit}")
	if err != nil {
		return nil, err
	}
	if len(in.Prompt) == 0 || len(in.Prompt) > 16<<10 || !utf8.Valid(in.Prompt) || !utf8.ValidString(path) {
		return nil, faults.New(faults.CodeValidationFailed, "prompt must be nonempty UTF-8 <=16 KiB; repository path must be UTF-8")
	}
	parts := strings.SplitN(in.Model, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, faults.New(faults.CodeValidationFailed, "model must be explicit provider/model#variant")
	}
	modelParts := strings.SplitN(parts[1], "#", 2)
	if modelParts[0] == "" || (len(modelParts) == 2 && modelParts[1] == "") {
		return nil, faults.New(faults.CodeValidationFailed, "model and optional variant must be nonempty")
	}
	m := agentModel{ID: modelParts[0], ProviderID: parts[0]}
	if len(modelParts) == 2 {
		m.Variant = modelParts[1]
	}
	if !utf8.Valid(in.Permissions) {
		return nil, faults.New(faults.CodeValidationFailed, "permission JSON must be UTF-8")
	}
	var rules []permissionRule
	d := json.NewDecoder(bytes.NewReader(in.Permissions))
	d.DisallowUnknownFields()
	if err := d.Decode(&rules); err != nil {
		return nil, faults.Wrap(faults.CodeValidationFailed, "invalid permission JSON", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return nil, faults.New(faults.CodeValidationFailed, "permission file must contain exactly one JSON array")
	}
	if len(rules) == 0 {
		return nil, faults.New(faults.CodeValidationFailed, "explicit nonempty session permission rules required")
	}
	for _, r := range rules {
		if r.Action == "" || r.Resource == "" || (r.Effect != "allow" && r.Effect != "deny" && r.Effect != "ask") {
			return nil, faults.New(faults.CodeValidationFailed, "invalid permission rule")
		}
	}
	r := agentRequest{Version: 2, Kind: "agent", Repo: path, Base: pinned, Prompt: string(in.Prompt), Agent: in.Agent, Model: m, Permissions: rules, ProvisionPolicy: provisionPolicy, MemoryBytes: in.MemoryBytes}
	data, err := json.Marshal(r)
	if err != nil {
		return nil, classifyDiagnostic(faults.CodeInvariantFailed, err)
	}
	return data, nil
}

func storedRequest(data []byte) (*commandRequest, *agentRequest, error) {
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, nil, classifyDiagnostic(faults.CodeInvariantFailed, err)
	}
	switch header.Version {
	case 1:
		r, err := decodeCommandRequest(data)
		return &r, nil, err
	case 2:
		var r agentRequest
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, nil, classifyDiagnostic(faults.CodeInvariantFailed, err)
		}
		if r.Kind != "agent" || r.ProvisionPolicy != provisionPolicy || r.MemoryBytes < 0 || r.Repo == "" || r.Base == "" || r.Prompt == "" {
			return nil, nil, faults.New(faults.CodeInvariantFailed, "invalid stored agent request")
		}
		return nil, &r, nil
	default:
		return nil, nil, faults.New(faults.CodeInvariantFailed, "unsupported immutable request version")
	}
}

func (j Job) memoryDemand() int64 {
	if j.agentRequest != nil {
		return j.agentRequest.MemoryBytes
	}
	return j.commandRequest.MemoryBytes
}
