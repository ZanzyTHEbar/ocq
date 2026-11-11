package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"unicode/utf8"

	"ocq/internal/core/dbquery"
)

// Same local queue/space identity, not a project or live OpenCode identity.
type AgentSubmissionScope = CommandSubmissionScope
type AgentSubmissionStatus = CommandSubmissionStatus

type AgentSubmissionDraft struct {
	Key         string `json:"key"`
	Repo        string `json:"repo"`
	Base        string `json:"base"`
	Prompt      string `json:"prompt"`
	Agent       string `json:"agent"`
	ProviderID  string `json:"provider_id"`
	ModelID     string `json:"model_id"`
	Variant     string `json:"variant"`
	Memory      string `json:"memory_bytes"`
	Permissions string `json:"permissions"`
}

// Private envelope; canonical is an EXACT v2 JSON string, never a JS encoder.
// Input provenance is documentary, not authority or runtime verification.
type preparedAgentSubmission struct {
	Scope            AgentSubmissionScope `json:"scope"`
	Key              string               `json:"key"`
	Canonical        string               `json:"canonical"`
	Source           string               `json:"source"`
	SuppliedRepo     string               `json:"supplied_repo,omitempty"`
	SuppliedBase     string               `json:"supplied_base,omitempty"`
	PermissionsInput string               `json:"permissions_input,omitempty"`
	Import           string               `json:"import_json,omitempty"`
}

func agentModelSelector(m agentModel) string {
	s := m.ProviderID + "/" + m.ID
	if m.Variant != "" {
		s += "#" + m.Variant
	}
	return s
}

// Uses the actual standalone CLI resolver/serializer and shared strict form
// permissions. No API/profile discovery, provisioning, session, prompt or job.
func BuildAgentSubmissionDraft(s AgentSubmissionScope, d AgentSubmissionDraft) ([]byte, error) {
	m, e := strconv.ParseInt(d.Memory, 10, 64)
	if e != nil || m < 0 || strconv.FormatInt(m, 10) != d.Memory || !s.valid() || !validCommandKey(d.Key) || d.Repo == "" || d.Base == "" {
		return nil, errors.New("explicit standalone agent fields required")
	}
	if e = jobDraftText([]string{d.Key, d.Repo, d.Base, d.Prompt, d.Agent, d.ProviderID, d.ModelID, d.Variant, d.Permissions}); e != nil {
		return nil, e
	}
	text, e := json.Marshal(d.Permissions)
	if e != nil {
		return nil, e
	}
	rules, e := parseJobDraftPermissions(text)
	if e != nil {
		return nil, e
	}
	permissionBytes, e := json.Marshal(rules)
	if e != nil {
		return nil, e
	}
	model := agentModel{ProviderID: d.ProviderID, ID: d.ModelID, Variant: d.Variant}
	canonical, e := canonicalStandaloneAgentSubmission(context.Background(), AgentSubmission{Key: d.Key, Repo: d.Repo, Base: d.Base, Prompt: []byte(d.Prompt), Agent: d.Agent, Model: agentModelSelector(model), MemoryBytes: m, Permissions: permissionBytes})
	if e != nil {
		return nil, e
	}
	r, e := parseCanonicalAgentRequest(canonical)
	if e != nil {
		return nil, e
	}
	if r.Model != model {
		return nil, errors.New("model selector must preserve the exact supplied profile; ambiguous delimiters refused")
	}
	return json.Marshal(preparedAgentSubmission{Scope: s, Key: d.Key, Canonical: string(canonical), Source: "guided_core_resolved", SuppliedRepo: d.Repo, SuppliedBase: d.Base, PermissionsInput: d.Permissions})
}

// Advanced exact JSON input is {key,request}; the existing embedded-byte parser
// supports canonical objects or strings (and its exact canonical wrapper).
// Never normalize or reserialize the imported v2 bytes or original import text.
func ImportAgentSubmissionDraft(s AgentSubmissionScope, raw []byte) ([]byte, error) {
	var in struct {
		Key     string          `json:"key"`
		Request json.RawMessage `json:"request"`
	}
	if _, e := decodeStrictJSONObject(raw, composeLimit, &in); e != nil {
		return nil, e
	}
	if !s.valid() || !validCommandKey(in.Key) {
		return nil, errors.New("exact local scope/key required")
	}
	canonical, _, e := extractEmbeddedBytes(in.Request, "")
	if e != nil {
		return nil, e
	}
	if _, e = parseCanonicalAgentRequest(canonical); e != nil {
		return nil, e
	}
	return json.Marshal(preparedAgentSubmission{Scope: s, Key: in.Key, Canonical: string(canonical), Source: "exact_json_import", Import: string(raw)})
}

func parsePreparedAgentSubmission(raw []byte) (preparedAgentSubmission, error) {
	var p preparedAgentSubmission
	if _, e := decodeStrictJSONObject(raw, composeLimit, &p); e != nil {
		return p, e
	}
	if !p.Scope.valid() || !validCommandKey(p.Key) {
		return p, errors.New("exact agent scope/key required")
	}
	r, e := parseCanonicalAgentRequest([]byte(p.Canonical))
	if e != nil {
		return p, e
	}
	switch p.Source {
	case "guided_core_resolved":
		if p.SuppliedRepo == "" || p.SuppliedBase == "" || p.PermissionsInput == "" || p.Import != "" {
			return p, errors.New("guided provenance required")
		}
		text, e := json.Marshal(p.PermissionsInput)
		if e != nil {
			return p, e
		}
		rules, e := parseJobDraftPermissions(text)
		if e != nil {
			return p, e
		}
		actual, e := json.Marshal(rules)
		if e != nil {
			return p, e
		}
		expected, e := json.Marshal(r.Permissions)
		if e != nil || !bytes.Equal(actual, expected) {
			return p, errors.New("guided ordered permission linkage changed")
		}
	case "exact_json_import":
		if p.Import == "" || p.SuppliedRepo != "" || p.SuppliedBase != "" || p.PermissionsInput != "" {
			return p, errors.New("exact import provenance required")
		}
		built, e := ImportAgentSubmissionDraft(p.Scope, []byte(p.Import))
		if e != nil {
			return p, e
		}
		var imported preparedAgentSubmission
		if json.Unmarshal(built, &imported) != nil || imported.Key != p.Key || imported.Canonical != p.Canonical {
			return p, errors.New("imported request linkage changed")
		}
	default:
		return p, errors.New("unsupported agent input provenance")
	}
	return p, nil
}

// Receipt recovery precedes all path checks. Fresh validation runs without a
// write lock; the transaction then repeats the immutable receipt/linkage check.
type AgentSubmitter struct{ store *store }

func OpenExistingAgentSubmitter(dir string) (*AgentSubmitter, error) {
	s, e := openExistingWritableStore(dir)
	if e != nil {
		return nil, e
	}
	s.existingSchemaOnly = true
	return &AgentSubmitter{s}, nil
}
func (w *AgentSubmitter) Close() error { return w.store.database.Close() }
func (w *AgentSubmitter) SubmitPreparedAgent(key string, raw []byte) (Job, error) {
	return w.submitPreparedAgent(context.Background(), key, raw)
}

func (w *AgentSubmitter) submitPreparedAgent(ctx context.Context, key string, raw []byte) (Job, error) {
	raw = append([]byte(nil), raw...)
	if !validCommandKey(key) {
		return Job{}, errors.New("explicit agent key required")
	}
	r, e := parseCanonicalAgentRequest(raw)
	if e != nil {
		return Job{}, e
	}
	digest := sha256Hex(raw)
	id, e := preparedAgentReceipt(dbquery.New(w.store.db), key, raw, digest)
	if e != nil {
		return Job{}, e
	}
	if id != "" {
		return w.store.get(id)
	}
	permissions, e := json.Marshal(r.Permissions)
	if e != nil {
		return Job{}, e
	}
	fresh, validationErr := canonicalStandaloneAgentSubmission(ctx, AgentSubmission{Key: key, Repo: r.Repo, Base: r.Base, Prompt: []byte(r.Prompt), Agent: r.Agent, Model: agentModelSelector(r.Model), Permissions: permissions, MemoryBytes: r.MemoryBytes})
	if validationErr == nil && !bytes.Equal(raw, fresh) {
		validationErr = errors.New("fresh repository/base/profile no longer match reviewed canonical bytes")
	}
	// A competing writer may have committed while validation ran, even if Git
	// failed. Recover exact matches; never overwrite conflicts or insert on error.
	e = w.store.write(func(c *sql.Conn) error {
		var err error
		id, err = preparedAgentReceipt(dbquery.New(c), key, raw, digest)
		if err != nil || id != "" {
			return err
		}
		if validationErr != nil {
			return validationErr
		}
		id, err = insertCanonicalReceipt(c, key, raw, digest, true)
		return err
	})
	if e != nil {
		return Job{}, e
	}
	return w.store.get(id)
}

func preparedAgentReceipt(q *dbquery.Queries, key string, raw []byte, digest string) (string, error) {
	row, err := q.PreparedAgentReceiptByKey(context.Background(), key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !bytes.Equal(row.Canonical, raw) {
		return "", errors.New("key belongs to different canonical bytes")
	}
	if row.Digest != digest || row.JobID == "" || row.Sequence <= 0 || !row.JobPresent || !row.AgentPresent {
		return "", errors.New("agent receipt digest/job/ownership linkage missing or inconsistent")
	}
	return row.JobID, nil
}
func (r *Reader) AgentSubmissionReceipt(key string, raw []byte) (QueueJobOutput, error) {
	if !validCommandKey(key) {
		return QueueJobOutput{}, errors.New("explicit agent key required")
	}
	if _, e := parseCanonicalAgentRequest(raw); e != nil {
		return QueueJobOutput{}, e
	}
	id, e := preparedAgentReceipt(dbquery.New(r.store.db), key, raw, sha256Hex(raw))
	if e != nil {
		return QueueJobOutput{}, e
	}
	if id == "" {
		return QueueJobOutput{}, sql.ErrNoRows
	}
	out, e := r.QueueJob(id)
	if e != nil {
		return out, e
	}
	if out.Job.Kind != "agent" {
		return QueueJobOutput{}, errors.New("agent receipt/ownership linkage missing")
	}
	return out, nil
}

// This projection is private authenticated transport only, not a stored DTO.
func agentSubmissionReview(raw []byte) ([]byte, error) {
	p, e := parsePreparedAgentSubmission(raw)
	if e != nil {
		return nil, e
	}
	r, e := parseCanonicalAgentRequest([]byte(p.Canonical))
	if e != nil {
		return nil, e
	}
	type field struct {
		Label string `json:"label"`
		Value string `json:"value"`
	}
	fields := []field{{"Machine", p.Scope.Machine}, {"Space", p.Scope.Space}, {"Queue identity", p.Scope.Queue}, {"Immutable key", p.Key}, {"Input provenance", p.Source}, {"Supplied repository (empty for exact import)", p.SuppliedRepo}, {"Supplied base (empty for exact import)", p.SuppliedBase}, {"Version", strconv.Itoa(r.Version)}, {"Kind", r.Kind}, {"Canonical repository", r.Repo}, {"Pinned base commit", r.Base}, {"Prompt (complete exact UTF-8)", r.Prompt}, {"Prompt byte count", strconv.Itoa(len(r.Prompt))}, {"Prompt SHA-256", sha256Hex([]byte(r.Prompt))}, {"Exact agent ID", r.Agent}, {"Provider ID", r.Model.ProviderID}, {"Model ID", r.Model.ID}, {"Variant (empty means omitted by canonical core encoder)", r.Model.Variant}, {"Memory bytes (exact int64)", strconv.FormatInt(r.MemoryBytes, 10)}, {"Ordered permission count", strconv.Itoa(len(r.Permissions))}}
	for n, rule := range r.Permissions {
		prefix := "permissions[" + strconv.Itoa(n) + "]"
		fields = append(fields, field{prefix + ".action", rule.Action}, field{prefix + ".resource", rule.Resource}, field{prefix + ".effect", rule.Effect})
	}
	fields = append(fields, field{"Original permission input (guided only)", p.PermissionsInput}, field{"Provision policy (fixed core constant)", r.ProvisionPolicy}, field{"Effective defaults / authority", "No HEAD, agent/model, memory or permissions guessed. Guided empty variant is omitted by the existing v2 encoder; provisioning policy is the fixed core constant. Imported canonical fields must already be complete. Rules retain order; syntax validation is NOT approval. No live API/provider availability, session or worktree is discovered or verified."}, field{"Original advanced JSON import (empty for guided)", p.Import}, field{"Exact v2 canonical request", p.Canonical}, field{"Canonical SHA-256", sha256Hex([]byte(p.Canonical))}, field{"Exact retained envelope", string(raw)}, field{"Envelope SHA-256", sha256Hex(raw)})
	b, e := json.Marshal(struct {
		Raw             string  `json:"raw"`
		Digest          string  `json:"sha256"`
		CanonicalDigest string  `json:"canonical_sha256"`
		Fields          []field `json:"fields"`
	}{string(raw), sha256Hex(raw), sha256Hex([]byte(p.Canonical)), fields})
	if e != nil || len(b) > 8*composeLimit || !utf8.Valid(b) {
		return nil, errors.New("agent review exceeds private bound")
	}
	return b, nil
}
