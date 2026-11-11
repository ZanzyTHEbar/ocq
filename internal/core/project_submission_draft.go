package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"unicode/utf8"

	"ocq/internal/core/dbquery"
)

// ProjectJobDraftProfile is explicit private form input. No agent/model or
// permission profile is inferred. Permissions remain an ordered JSON list.
type ProjectJobDraftProfile struct {
	Key                       string          `json:"key"`
	ProjectID                 string          `json:"project_id"`
	BindingRevision           int             `json:"binding_revision"`
	LaneID                    string          `json:"lane_id"`
	Repo                      string          `json:"repo"`
	Base                      string          `json:"base"`
	Agent                     string          `json:"agent"`
	ProviderID                string          `json:"provider_id"`
	ModelID                   string          `json:"model_id"`
	Variant                   string          `json:"variant"`
	Permissions               json.RawMessage `json:"permissions"`
	MemoryBytes               *int64          `json:"memory_bytes"`
	ReferencedManifestDigests []string        `json:"referenced_manifest_digests"`
}

type ProjectImplementationDraft struct {
	ProjectJobDraftProfile
	Prompt             string   `json:"prompt"`
	Goal               string   `json:"goal"`
	Scope              []string `json:"scope"`
	Exclusions         []string `json:"exclusions"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	AllowedActions     []string `json:"allowed_actions"`
	ContextReferences  []string `json:"context_references"`
}

type ProjectReviewDraft struct {
	ProjectJobDraftProfile
	TargetAssociationID string `json:"target_association_id"`
	TargetJobID         string `json:"target_job_id"`
	TargetResultDigest  string `json:"target_result_digest"`
	Criteria            string `json:"criteria"`
	Rules               string `json:"rules"`
}

func (p ProjectJobDraftProfile) request(prompt string) (agentRequest, error) {
	if p.MemoryBytes == nil || *p.MemoryBytes < 0 || !utf8.Valid(p.Permissions) {
		return agentRequest{}, errors.New("explicit memory and UTF-8 permissions required")
	}
	if err := jobDraftText([]string{p.Key, p.ProjectID, p.LaneID, p.Repo, p.Base, prompt, p.Agent, p.ProviderID, p.ModelID, p.Variant}); err != nil {
		return agentRequest{}, err
	}
	rules, err := parseJobDraftPermissions(p.Permissions)
	if err != nil {
		return agentRequest{}, err
	}
	return agentRequest{Version: 2, Kind: "agent", Repo: p.Repo, Base: p.Base, Prompt: prompt, Agent: p.Agent, Model: agentModel{ID: p.ModelID, ProviderID: p.ProviderID, Variant: p.Variant}, Permissions: rules, ProvisionPolicy: provisionPolicy, MemoryBytes: *p.MemoryBytes}, nil
}

// Same strict ordered permission decoder for project and standalone forms.
// This is syntax validation, not a replacement policy or a permission approval.
func parseJobDraftPermissions(raw json.RawMessage) ([]permissionRule, error) {
	permissionBytes := []byte(raw)
	// Native form input keeps the ORIGINAL permission JSON text in a string;
	// do not let a JavaScript parse/stringify erase duplicate keys or ordering.
	if trimmed := bytes.TrimSpace(permissionBytes); len(trimmed) > 0 && trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return nil, err
		}
		permissionBytes = []byte(text)
	}
	var rules []permissionRule
	decoder := json.NewDecoder(bytes.NewReader(permissionBytes))
	decoder.DisallowUnknownFields()
	if err := checkNoDuplicateJSONKeys([]byte(`{"permissions":` + string(permissionBytes) + `}`)); err != nil {
		return nil, err
	}
	var ruleFields []map[string]json.RawMessage
	if err := json.Unmarshal(permissionBytes, &ruleFields); err != nil {
		return nil, err
	}
	for _, fields := range ruleFields {
		for key := range fields {
			if key != "action" && key != "resource" && key != "effect" {
				return nil, errors.New("exact action/resource/effect permission fields required")
			}
		}
	}
	if err := decoder.Decode(&rules); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("exact permission array required")
	}
	if err := validatePermissionRules(rules); err != nil {
		return nil, err
	}
	return rules, nil
}

func jobDraftText(groups ...[]string) error {
	for _, group := range groups {
		for _, value := range group {
			if !utf8.ValidString(value) {
				return errors.New("valid UTF-8 draft required")
			}
		}
	}
	return nil
}

// Both builders use one non-initializing read-only SQLite snapshot. They never
// admit work, inspect a live runtime, change a revision or create a session.
func (r *Reader) BuildProjectImplementationDraft(d ProjectImplementationDraft) ([]byte, error) {
	if err := jobDraftText([]string{d.Goal}, d.Scope, d.Exclusions, d.AcceptanceCriteria, d.AllowedActions, d.ContextReferences); err != nil {
		return nil, err
	}
	req, err := d.ProjectJobDraftProfile.request(d.Prompt)
	if err != nil {
		return nil, err
	}
	brief := briefManifest{Version: 1, Kind: "brief", ProjectID: d.ProjectID, Source: "operator_import", Goal: d.Goal, Scope: d.Scope, Exclusions: d.Exclusions, AcceptanceCriteria: d.AcceptanceCriteria, Base: d.Base, Agent: d.Agent, Model: req.Model, Permissions: req.Permissions, AllowedActions: d.AllowedActions, ContextReferences: d.ContextReferences}
	var raw []byte
	err = r.store.readSchema5(func(tx sqlQueryer) error {
		var err error
		raw, err = r.buildJobDraft(tx, d.ProjectJobDraftProfile, req, brief, "implementation", nil, nil, d.ReferencedManifestDigests)
		return err
	})
	return raw, err
}

func (r *Reader) BuildProjectReviewDraft(d ProjectReviewDraft) ([]byte, error) {
	if err := jobDraftText([]string{d.Criteria, d.Rules}); err != nil {
		return nil, err
	}
	var raw []byte
	err := r.store.readSchema5(func(tx sqlQueryer) error {
		ctx := context.Background()
		queries := dbquery.New(tx)
		association, err := queries.ExistingAssociation(ctx, dbquery.ExistingAssociationParams{ProjectID: d.ProjectID, JobID: d.TargetJobID, Kind: "result", ManifestDigest: d.TargetResultDigest})
		if err != nil || association.ID != d.TargetAssociationID {
			return errors.New("exact selected result association unavailable")
		}
		row, err := queries.ProjectManifest(ctx, d.TargetResultDigest)
		if err != nil {
			return err
		}
		target, err := parseManifestBytes(row.Canonical, "result", d.ProjectID)
		if err != nil || target.Digest != d.TargetResultDigest {
			return errors.New("exact target bytes unavailable")
		}
		// Copy ALL core-supported diff/files/content from the immutable result.
		// Never accept caller-supplied substitutes, truncate, or invent summaries.
		brief := reviewBriefManifest{Version: 1, Kind: "review_brief", ProjectID: d.ProjectID, Source: "operator_import", TargetJobID: d.TargetJobID, TargetResultDigest: d.TargetResultDigest, Base: d.Base, Criteria: d.Criteria, Rules: d.Rules, Diff: target.Result.Diff, ChangedFiles: target.Result.ChangedFiles}
		prompt, err := buildReviewPrompt(brief)
		if err != nil {
			return err
		}
		req, err := d.ProjectJobDraftProfile.request(prompt)
		if err != nil {
			return err
		}
		digests := append(append([]string{}, d.ReferencedManifestDigests...), d.TargetResultDigest)
		raw, err = r.buildJobDraft(tx, d.ProjectJobDraftProfile, req, brief, "review", &d.TargetJobID, &d.TargetResultDigest, digests)
		if err != nil {
			return err
		}
		parsed, err := parseSubmissionEnvelope(raw)
		if err != nil {
			return err
		}
		return verifyReviewTarget(ctx, tx, parsed)
	})
	return raw, err
}

func (r *Reader) buildJobDraft(tx sqlQueryer, p ProjectJobDraftProfile, req agentRequest, brief any, role string, targetJob, targetDigest *string, digests []string) ([]byte, error) {
	ctx := context.Background()
	binding, err := r.store.currentProjectBinding(ctx, tx, p.ProjectID)
	if err != nil {
		return nil, err
	}
	if p.BindingRevision < 1 || p.BindingRevision != binding.Revision || p.Repo != binding.RepoRoot {
		return nil, errors.New("exact current binding revision and repository required")
	}
	// json.Marshal is the existing canonical v2 request encoder, deliberately
	// distinct from manifest serialization. Validate that exact result below.
	request, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	b, err := marshalCanonicalManifestJSON(brief)
	if err != nil {
		return nil, err
	}
	requestString, err := json.Marshal(string(request))
	if err != nil {
		return nil, err
	}
	briefString, err := json.Marshal(string(b))
	if err != nil {
		return nil, err
	}
	refs := []json.RawMessage{}
	ordered := append([]string{}, digests...)
	sort.Strings(ordered)
	for n, digest := range ordered {
		if !isValidHexLen(digest, 64) || (n > 0 && digest == ordered[n-1]) {
			return nil, errors.New("unique exact reference digests required (review includes its target automatically)")
		}
		row, err := dbquery.New(tx).ProjectManifest(ctx, digest)
		if err != nil {
			return nil, err
		}
		manifest, err := parseManifestBytes(row.Canonical, "", p.ProjectID)
		if err != nil || manifest.Digest != digest {
			return nil, errors.New("exact stored reference unavailable")
		}
		embedded, err := json.Marshal(string(manifest.Canonical))
		if err != nil {
			return nil, err
		}
		refs = append(refs, embedded)
	}
	raw, err := marshalCanonicalManifestJSON(projectSubmissionEnvelope{Version: 1, Key: p.Key, ProjectID: p.ProjectID, BindingRevision: p.BindingRevision, Role: role, LaneID: p.LaneID, FsforkDriver: "git_worktree", Request: requestString, BriefDigest: sha256Hex(b), BriefManifest: briefString, TargetJobID: targetJob, TargetResultDigest: targetDigest, ReferencedManifests: refs})
	if err != nil {
		return nil, err
	}
	if _, err := parseSubmissionEnvelope(raw); err != nil {
		return nil, err
	}
	return raw, nil
}
