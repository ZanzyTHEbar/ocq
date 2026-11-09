package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ZanzyTHEbar/faults-go"

	"ocq/internal/core/dbquery"
)

const agentCompletionPolicy = "session-scoped-v1"

// All reconciliation is observational. There is intentionally no POST, resume,
// permission reply, cancellation or process-liveness shortcut in this function.
func (s *store) observeAgent(j Job) error {
	if j.Agent == nil || j.agentRequest == nil {
		return diagnostic(faults.CodeInvariantFailed, "missing exact agent ownership")
	}
	if j.State == "queued" || j.State == "cancelled" {
		return diagnostic(faults.CodeValidationFailed, "agent job has not been admitted")
	}
	if j.Agent.Worktree == "" {
		return s.agentUnknown(j.ID, "Workmux acknowledgment/ownership unresolved; no name/branch adoption and no mutation retry", false)
	}
	// Preserve the superseded causal-provenance diagnosis before re-evaluating
	// its entire transcript. New-policy interference remains latched separately.
	priorPath := filepath.Join(s.dir, "jobs", j.ID, "prior-correlation-diagnostic.json")
	priorDiagnostic, err := os.ReadFile(priorPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if j.Agent.Phase == "interference" && errors.Is(err, os.ErrNotExist) {
		priorDiagnostic, err = json.Marshal(map[string]any{"state": j.State, "outcome": j.Outcome, "phase": j.Agent.Phase, "reason": j.Reason, "superseded_by": agentCompletionPolicy})
		if err != nil {
			return err
		}
		if err := persistEvidence(priorPath, priorDiagnostic); err != nil {
			return err
		}
	}
	var before sessionInfo
	path := "/api/session/" + j.Agent.SessionID
	if err := s.inspectAPI(j, "session-observed-before", path, &before); err != nil {
		return s.agentFailure(j, err)
	}
	if err := validateSession(j, before); err != nil {
		return s.interference(j, err.Error())
	}
	if err := s.verifyProjectSessionPolicy(context.Background(), j, before); err != nil {
		return s.interference(j, err.Error())
	}
	isReview, err := s.isProjectReviewJob(context.Background(), j.ID)
	if err != nil {
		return err
	}
	intent, err := dbquery.New(s.db).AgentPromptIntent(context.Background(), j.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return s.agentUnknown(j.ID, "exact session inspected, but no durable prompt intent; provisioning will not be resumed", false)
		}
		return err
	}
	expected := map[string]any{"id": j.Agent.PromptID, "text": j.agentRequest.Prompt, "metadata": ownershipMetadata(j)}
	if !jsonEqual(json.RawMessage(intent), expected) {
		return s.interference(j, "durable prompt intent differs from immutable request/ownership")
	}
	var permissions, forms []json.RawMessage
	if err := s.inspectAPI(j, "pending-permissions", path+"/permission", &permissions); err != nil {
		return s.agentFailure(j, err)
	}
	if err := s.inspectAPI(j, "pending-forms", path+"/form", &forms); err != nil {
		return s.agentFailure(j, err)
	}
	if len(permissions) > 0 || len(forms) > 0 {
		return s.agentUnknown(j.ID, "blocked: pending permission/form; never auto-answer; capacity held", true)
	}
	if j.Agent.Phase == "session_interference" {
		messages, err := s.transcript(j)
		if err != nil {
			return s.agentFailure(j, err)
		}
		if err := s.unresolvedObservation(j, messages); err != nil {
			return err
		}
		return s.agentUnknown(j.ID, j.Reason, false)
	}
	resourcesBefore, err := s.observeResources(j, "resources-before", nil)
	if err != nil {
		return s.agentFailure(j, err)
	}
	var inbox []inboxItem
	if err := s.inspectAPI(j, "inbox", path+"/inbox", &inbox); err != nil {
		return s.agentFailure(j, err)
	}
	for _, in := range inbox {
		if in.SessionID == j.Agent.SessionID && (in.Type == "synthetic" || in.Type == "system") {
			continue // Auxiliary context must drain before completion, not be adopted as our prompt.
		}
		if !matchingInput(j, in) {
			return s.interference(j, "foreign or changed inbox input detected; ownership/capacity retained")
		}
	}
	if len(inbox) > 0 {
		return s.agentUnknown(j.ID, "session input pending in inbox; no completion, resend, or resume", true)
	}
	messages, err := s.transcript(j)
	if err != nil {
		return s.agentFailure(j, err)
	}
	if len(messages) == 0 {
		return s.agentUnknown(j.ID, "exact prompt absent from durable history; acceptance/completion unresolved; never resend", false)
	}
	seen := map[string]bool{}
	lastTime := float64(0)
	text := strings.Builder{}
	assistants := 0
	anchors := 0
	auxiliary := []map[string]string{}
	shellReferences := []string{}
	toolIDs := map[string]bool{}
	toolCount := 0
	toolErrors := 0
	for _, m := range messages {
		if m.ID == "" || seen[m.ID] || m.Time.Created <= 0 || m.Time.Created < lastTime {
			return s.agentUnknown(j.ID, "transcript order/identity cannot be established; no completion inferred", false)
		}
		seen[m.ID] = true
		lastTime = m.Time.Created
		switch m.Type {
		case "user":
			if anchors != 0 || m.ID != j.Agent.PromptID || m.Text != j.agentRequest.Prompt || !jsonEqual(m.Metadata, ownershipMetadata(j)) || len(m.Files)+len(m.Agents)+len(m.Skills) != 0 {
				return s.interference(j, "additional or mismatched user input; exact single owned prompt required")
			}
			anchors++
		case "system", "synthetic":
			auxiliary = append(auxiliary, map[string]string{"id": m.ID, "type": m.Type})
		case "assistant":
			if anchors != 1 {
				return s.interference(j, "assistant execution before the owned prompt anchor")
			}
			if m.Agent != j.agentRequest.Agent || m.Model != j.agentRequest.Model {
				return s.interference(j, "unexpected assistant agent/model mutation")
			}
			last := messages[len(messages)-1]
			aborted := last.Type == "idle" && (last.Outcome == "failed" || last.Outcome == "interrupted")
			assistants++
			for _, part := range m.Content {
				switch part.Type {
				case "text":
					text.WriteString(part.Text)
					text.WriteByte('\n')
				case "reasoning": // retained privately, never presented as user output
				case "tool":
					if toolIDs[part.ID] {
						return s.agentUnknown(j.ID, "duplicate tool call identity in transcript; inspect exact runtime history", false)
					}
					if isReview && part.Name != "read" && part.Name != "glob" && part.Name != "grep" {
						return s.interference(j, fmt.Sprintf("reviewer job invoked non-read-only tool %q; capacity held", part.Name))
					}
					id, err := terminalTool(part)
					if err != nil {
						return s.agentUnknown(j.ID, err.Error()+"; capacity held", false)
					}
					toolIDs[part.ID] = true
					toolCount++
					if part.State.Status == "error" {
						toolErrors++
					}
					if id != "" {
						shellReferences = append(shellReferences, id)
					}
				default:
					return s.agentUnknown(j.ID, "unsupported transcript content; resource lifetime unaccounted; capacity held", false)
				}
			}
			if m.Time.Completed <= 0 && !aborted {
				return s.agentUnknown(j.ID, "assistant still incomplete; capacity held", false)
			}
			if m.Time.Completed > 0 && (m.Time.Completed < m.Time.Created || (last.Type == "idle" && m.Time.Completed > last.Time.Created)) {
				return s.agentUnknown(j.ID, "assistant completion is not coherently before the terminal boundary", false)
			}
		case "idle":
			if anchors != 1 || (m.Outcome != "succeeded" && m.Outcome != "failed" && m.Outcome != "interrupted") {
				return s.interference(j, "idle without owned prompt or supported explicit outcome")
			}
		default:
			return s.interference(j, "command/shell/unsupported history or unexpected agent/model/location mutation")
		}
	}
	if anchors != 1 {
		return s.agentUnknown(j.ID, "exact prompt not consumed into user history; never resend", false)
	}
	terminal := messages[len(messages)-1]
	if terminal.Type != "idle" {
		return s.agentUnknown(j.ID, "no explicit idle outcome after exact prompt interval; capacity held", false)
	}
	if terminal.Outcome != "succeeded" && terminal.Outcome != "failed" && terminal.Outcome != "interrupted" {
		return s.agentUnknown(j.ID, "explicit idle outcome unsupported", false)
	}
	if terminal.Outcome == "succeeded" && assistants == 0 {
		return s.agentUnknown(j.ID, "idle success has no complete assistant response in owned interval", false)
	}
	// Read only the known session's activity bit; never persist peer session data.
	activeBody, err := apiCall(j.Agent.Worktree, "get", "/api/session/active", nil)
	if err != nil {
		return s.agentFailure(j, err)
	}
	var active map[string]json.RawMessage
	if err := decodeData(activeBody, &active); err != nil {
		return s.agentFailure(j, err)
	}
	if _, running := active[j.Agent.SessionID]; running {
		return s.agentUnknown(j.ID, "exact session agent loop is active; capacity held", false)
	}
	var after sessionInfo
	if err := s.inspectAPI(j, "session-observed-after", path, &after); err != nil {
		return s.agentFailure(j, err)
	}
	if err := validateSession(j, after); err != nil {
		return s.interference(j, err.Error())
	}
	if err := s.verifyProjectSessionPolicy(context.Background(), j, after); err != nil {
		return s.interference(j, err.Error())
	}
	if before.Time.Updated != after.Time.Updated {
		return s.agentUnknown(j.ID, "session changed across transcript inspection; retry GET inspection only", false)
	}
	// Fence pagination against concurrent appended history, without treating a
	// latest-message query or session timestamp as the completion criterion.
	latestBody, err := apiCall(j.Agent.Worktree, "get", path+"/message?limit=1&order=desc", nil)
	if err != nil {
		return s.agentFailure(j, err)
	}
	var latest []message
	if err := decodeData(latestBody, &latest); err != nil {
		return s.agentFailure(j, err)
	}
	if len(latest) != 1 || latest[0].ID != terminal.ID {
		return s.agentUnknown(j.ID, "history changed across inspection; completion unresolved", false)
	}
	var finalInbox []inboxItem
	if err := s.inspectAPI(j, "inbox-final", path+"/inbox", &finalInbox); err != nil {
		return s.agentFailure(j, err)
	}
	if len(finalInbox) > 0 {
		for _, in := range finalInbox {
			if in.SessionID != j.Agent.SessionID || (in.Type != "system" && in.Type != "synthetic" && !matchingInput(j, in)) {
				return s.interference(j, "foreign input arrived across terminal inspection; capacity held")
			}
		}
		return s.agentUnknown(j.ID, "session input arrived across terminal inspection; wait for inbox to drain; capacity held", true)
	}
	var finalPermissions, finalForms []json.RawMessage
	if err := s.inspectAPI(j, "pending-permissions-final", path+"/permission", &finalPermissions); err != nil {
		return s.agentFailure(j, err)
	}
	if err := s.inspectAPI(j, "pending-forms-final", path+"/form", &finalForms); err != nil {
		return s.agentFailure(j, err)
	}
	if len(finalPermissions)+len(finalForms) > 0 {
		return s.agentUnknown(j.ID, "blocked: permission/form appeared during terminal inspection; capacity held", true)
	}
	resourcesFinal, err := s.observeResources(j, "resources-final", shellReferences)
	if err != nil {
		return s.agentFailure(j, err)
	}
	// Compare the whole projected transcript, not only its latest ID. This is a
	// coherent session observation, not a claim of trusted per-event causality.
	confirmed, err := s.transcript(j)
	if err != nil {
		return s.agentFailure(j, err)
	}
	if !jsonEqual(messages, confirmed) {
		return s.agentUnknown(j.ID, "full transcript changed across terminal inspection; retry GET inspection only", false)
	}
	activeBody, err = apiCall(j.Agent.Worktree, "get", "/api/session/active", nil)
	if err != nil {
		return s.agentFailure(j, err)
	}
	if err := decodeData(activeBody, &active); err != nil {
		return s.agentFailure(j, err)
	}
	if _, running := active[j.Agent.SessionID]; running {
		return s.agentUnknown(j.ID, "exact session agent loop became active during inspection; capacity held", false)
	}
	var finalSession sessionInfo
	if err := s.inspectAPI(j, "session-observed-final", path, &finalSession); err != nil {
		return s.agentFailure(j, err)
	}
	if err := validateSession(j, finalSession); err != nil {
		return s.interference(j, err.Error())
	}
	if err := s.verifyProjectSessionPolicy(context.Background(), j, finalSession); err != nil {
		return s.interference(j, err.Error())
	}
	if !jsonEqual(after, finalSession) {
		return s.agentUnknown(j.ID, "session changed across final inspection; retry GET inspection only", false)
	}
	evidence := map[string]any{"completion_policy": agentCompletionPolicy, "session": finalSession, "prompt_id": j.Agent.PromptID, "request_digest": j.Digest, "messages": messages, "auxiliary_events": auxiliary, "auxiliary_event_count": len(auxiliary), "auxiliary_provenance": "not asserted; session context under local single-user policy", "prior_diagnostic": json.RawMessage(priorDiagnostic), "active": false, "stable_transcript": true, "pending_permissions": len(finalPermissions), "pending_forms": len(finalForms), "inbox": 0, "tool_policy": "foreground-shell-v1: known terminal tools and owned runtime shell exit; cooperative resources only", "tool_count": toolCount, "tool_error_count": toolErrors, "resources_before": resourcesBefore, "resources_final": resourcesFinal, "validation": "not_run"}
	b, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	proof, err := s.agentEvidence(j.ID, "terminal-evidence-session-v1", b)
	if err != nil {
		return err
	}
	if err := persistEvidence(j.Stdout, []byte(text.String())); err != nil {
		return err
	}
	if err := persistEvidence(j.Stderr, []byte("server outcome: "+terminal.Outcome+"; validation: not_run\n")); err != nil {
		return err
	}
	return s.write(func(c *sql.Conn) error {
		q := dbquery.New(c)
		if err := q.CompleteAgentOwnership(context.Background(), dbquery.CompleteAgentOwnershipParams{ServerOutcome: terminal.Outcome, TerminalEvidence: proof, JobID: j.ID}); err != nil {
			return err
		}
		reason := fmt.Sprintf("session-scoped-v1 / foreground-shell-v1: one exact prompt, stable terminal session, terminal tools (%d errors recorded), no active loop/pending input/permission/form or running owned runtime resources; validation not run", toolErrors)
		return q.CompleteAgentJob(context.Background(), dbquery.CompleteAgentJobParams{Outcome: "agent_" + terminal.Outcome, Reason: reason, ID: j.ID})
	})
}

// Retain useful output/outcome observations WITHOUT claiming terminal correlation.
func (s *store) unresolvedObservation(j Job, messages []message) error {
	outcome := ""
	text := strings.Builder{}
	for _, m := range messages {
		if m.Type == "assistant" {
			for _, part := range m.Content {
				if part.Type == "text" {
					text.WriteString(part.Text)
					text.WriteByte('\n')
				}
			}
		}
	}
	if len(messages) > 0 {
		last := messages[len(messages)-1]
		if last.Type == "idle" && (last.Outcome == "succeeded" || last.Outcome == "failed" || last.Outcome == "interrupted") {
			outcome = last.Outcome
		}
	}
	b, err := json.Marshal(map[string]any{"session_id": j.Agent.SessionID, "prompt_id": j.Agent.PromptID, "messages": messages, "observed_session_outcome": outcome, "correlation": "unproved; job remains unknown", "validation": "not_run"})
	if err != nil {
		return err
	}
	if _, err := s.agentEvidence(j.ID, "unresolved-observation", b); err != nil {
		return err
	}
	if err := persistEvidence(j.Stdout, []byte("[unverified session output; prompt correlation unresolved]\n"+text.String())); err != nil {
		return err
	}
	if err := persistEvidence(j.Stderr, []byte("observed session outcome: "+outcome+"; job remains unknown; validation: not_run\n")); err != nil {
		return err
	}
	return s.write(func(c *sql.Conn) error {
		return dbquery.New(c).SetAgentServerOutcome(context.Background(), dbquery.SetAgentServerOutcomeParams{ServerOutcome: outcome, JobID: j.ID})
	})
}
