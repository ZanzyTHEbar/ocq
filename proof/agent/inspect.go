package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// This mode only reconciles recorded ownership and reads the real API/database.
// It never enters the full runner's provisioning or visibility mutation flows.
func (s *scenario) reconcileExisting() {
	b, err := os.ReadFile(filepath.Join(s.root, "queued-job.json"))
	must(err)
	must(json.Unmarshal(b, &s.job))
	before := s.status()
	check(before.Agent != nil && before.Agent.Session != "" && before.Agent.Prompt != "", "missing retained ownership")
	save(filepath.Join(s.root, "session-completion-before.json"), s.cli("status", s.job.ID))
	db, err := sql.Open("sqlite", "file:"+filepath.Join(s.state, "queue.db")+"?mode=ro&_pragma=busy_timeout(5000)")
	must(err)
	defer db.Close()
	var legacyBytes []byte
	var legacyDigest string
	must(db.QueryRow("SELECT canonical,digest FROM receipts WHERE key='legacy-receipt'").Scan(&legacyBytes, &legacyDigest))
	originalLegacy, err := os.ReadFile(filepath.Join(s.root, "legacy-canonical.json"))
	must(err)
	legacySum := sha256.Sum256(legacyBytes)
	check(string(legacyBytes) == string(originalLegacy) && legacyDigest == hex.EncodeToString(legacySum[:]), "original v1 receipt bytes/digest changed")
	// Snapshot immutable receipts, exact external intents/acks and ownership, not
	// merely their counts. Compare again after normal product reconciliation.
	snapshot := func() string {
		var receipts, operations, owner string
		must(db.QueryRow(`SELECT json_group_array(json_object('sequence',sequence,'key',key,'id',job_id,'canonical',hex(canonical),'digest',digest,'accepted_at',accepted_at)) FROM (SELECT * FROM receipts ORDER BY sequence)`).Scan(&receipts))
		must(db.QueryRow(`SELECT json_group_array(json_object('kind',kind,'intent',hex(intent),'response',response_path)) FROM (SELECT * FROM agent_operations WHERE job_id=? ORDER BY rowid)`, s.job.ID).Scan(&operations))
		must(db.QueryRow(`SELECT json_object('session',session_id,'prompt',prompt_id,'token',token,'worktree',worktree,'project',project_id) FROM agent_ownership WHERE job_id=?`, s.job.ID).Scan(&owner))
		return receipts + "\n" + operations + "\n" + owner
	}
	immutable := snapshot()
	save(filepath.Join(s.root, "session-completion-immutable-before.jsonl"), []byte(immutable))
	var heldBefore, heldAfter int
	must(db.QueryRow("SELECT count(*) FROM jobs WHERE state IN ('launching','running','unknown') AND id=?", s.job.ID).Scan(&heldBefore))
	b = s.cli("reconcile", s.job.ID)
	save(filepath.Join(s.root, "session-completion-job.json"), b)
	must(json.Unmarshal(b, &s.job))
	check(s.job.State == "finished" && s.job.Outcome == "agent_succeeded" && s.job.Agent.Phase == "terminal" && s.job.Agent.ServerOutcome == "succeeded", "retained session did not complete through normal API-only reconcile")
	check(s.job.Agent.Session == before.Agent.Session && s.job.Agent.Prompt == before.Agent.Prompt && s.job.Exit == nil && s.job.Validation == "not_run", "identity/exit/validation changed")
	check(snapshot() == immutable, "receipt, ownership, or external mutation intent/ack changed")
	var order string
	must(db.QueryRow("SELECT group_concat(kind,',') FROM (SELECT kind FROM agent_operations WHERE job_id=? AND response_path<>'' ORDER BY rowid)", s.job.ID).Scan(&order))
	check(order == "worktree,session,prompt", "not exactly one acknowledged worktree/session/prompt operation")
	must(db.QueryRow("SELECT count(*) FROM jobs WHERE state IN ('launching','running','unknown') AND id=?", s.job.ID).Scan(&heldAfter))
	check(heldAfter == 0, "terminal agent still consumes a slot")
	var state, outcome, validation, evidencePath string
	var exit sql.NullInt64
	must(db.QueryRow(`SELECT j.state,j.outcome,j.validation,j.exit_code,a.terminal_evidence FROM jobs j JOIN agent_ownership a ON a.job_id=j.id WHERE j.id=?`, s.job.ID).Scan(&state, &outcome, &validation, &exit, &evidencePath))
	check(state == "finished" && outcome == "agent_succeeded" && validation == "not_run" && !exit.Valid && evidencePath == s.job.Agent.TerminalEvidence, "database disagrees with durable terminal result")
	proof, err := os.ReadFile(evidencePath)
	must(err)
	var evidence struct {
		Policy         string              `json:"completion_policy"`
		Prompt         string              `json:"prompt_id"`
		Stable         bool                `json:"stable_transcript"`
		Auxiliary      []map[string]string `json:"auxiliary_events"`
		AuxiliaryCount int                 `json:"auxiliary_event_count"`
		Prior          json.RawMessage     `json:"prior_diagnostic"`
		Messages       []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	must(json.Unmarshal(proof, &evidence))
	check(evidence.Policy == "session-scoped-v1" && evidence.Prompt == s.job.Agent.Prompt && evidence.Stable, "versioned stable session-completion evidence absent")
	if before.Agent.Phase == "interference" {
		check(len(evidence.Prior) > 0 && string(evidence.Prior) != "null", "superseded diagnostic was lost")
	}
	// Independent one-message pagination verifies the real transcript, not only
	// the observer's result. No assertion depends on reminder or response wording.
	users, assistants, pages := 0, 0, 0
	cursor, lastType, lastOutcome := "", "", ""
	seen := map[string]bool{}
	ids := []string{}
	auxiliary := []map[string]string{}
	var lastTime float64
	var output strings.Builder
	promptSum := sha256.Sum256([]byte(s.job.Request.Prompt))
	expectedMetadata, err := json.Marshal(map[string]any{"ocq": map[string]any{"job_id": s.job.ID, "request_digest": s.job.Digest, "prompt_digest": hex.EncodeToString(promptSum[:]), "owner_token": s.job.Agent.Token}})
	must(err)
	equalJSON := func(a, b []byte) bool {
		var x, y any
		must(json.Unmarshal(a, &x))
		must(json.Unmarshal(b, &y))
		return reflect.DeepEqual(x, y)
	}
	for {
		query := url.Values{"limit": {"1"}}
		if cursor == "" {
			query.Set("order", "asc")
		} else {
			query.Set("cursor", cursor)
		}
		b := s.api("get", "/api/session/"+s.job.Agent.Session+"/message?"+query.Encode(), fmt.Sprintf("session-completion-page-%03d", pages), nil)
		pages++
		var page struct {
			Data []struct {
				ID, Type, Text, Agent, Outcome string
				Model, Metadata                json.RawMessage
				Files, Agents, Skills          []json.RawMessage
				Time                           struct{ Created, Completed float64 }
				Content                        []struct{ Type, Text string }
			} `json:"data"`
			Cursor *struct {
				Next *string `json:"next"`
			} `json:"cursor"`
		}
		must(json.Unmarshal(b, &page))
		check(page.Cursor != nil && page.Data != nil, "missing pagination envelope")
		for _, m := range page.Data {
			check(m.ID != "" && !seen[m.ID] && m.Time.Created > 0 && m.Time.Created >= lastTime, "transcript identity/order invalid")
			check(lastType != "idle", "extra execution interval after idle")
			seen[m.ID] = true
			ids = append(ids, m.ID)
			lastTime, lastType, lastOutcome = m.Time.Created, m.Type, m.Outcome
			switch m.Type {
			case "user":
				users++
				check(users == 1 && m.ID == s.job.Agent.Prompt && m.Text == s.job.Request.Prompt && equalJSON(m.Metadata, expectedMetadata) && len(m.Files)+len(m.Agents)+len(m.Skills) == 0, "extra/mismatched user execution input")
			case "system", "synthetic":
				auxiliary = append(auxiliary, map[string]string{"id": m.ID, "type": m.Type})
			case "assistant":
				check(users == 1 && m.Agent == s.job.Request.Agent && equalJSON(m.Model, s.job.Request.Model) && m.Time.Completed >= m.Time.Created, "unowned/profile-mismatched/incomplete assistant")
				assistants++
				for _, part := range m.Content {
					check(part.Type == "text" || part.Type == "reasoning", "tool/background/unsupported content entered the no-tool scenario")
					if part.Type == "text" {
						output.WriteString(part.Text)
						output.WriteByte('\n')
					}
				}
			case "idle":
				check(users == 1, "idle preceded the owned prompt")
			default:
				panic("unexpected execution input/profile mutation: " + m.Type)
			}
		}
		if page.Cursor.Next == nil {
			break
		}
		cursor = *page.Cursor.Next
		check(cursor != "" && pages < 100, "pagination did not terminate")
	}
	check(users == 1 && assistants > 0 && lastType == "idle" && lastOutcome == "succeeded", "not one consumed prompt followed by explicit success")
	check(len(ids) == len(evidence.Messages), "history changed since terminal observation")
	for i, id := range ids {
		check(id == evidence.Messages[i].ID, "evidence/history order mismatch")
	}
	check(evidence.AuxiliaryCount == len(auxiliary) && reflect.DeepEqual(evidence.Auxiliary, auxiliary), "auxiliary context not recorded accurately")
	for _, suffix := range []string{"inbox", "permission", "form"} {
		var pending struct {
			Data []json.RawMessage `json:"data"`
		}
		must(json.Unmarshal(s.api("get", "/api/session/"+s.job.Agent.Session+"/"+suffix, "session-completion-"+suffix, nil), &pending))
		check(pending.Data != nil && len(pending.Data) == 0, "pending input/permission/form remains")
	}
	check(!s.active(), "exact session loop remains active")
	var session struct {
		Data struct {
			ID, ParentID, Agent          string
			Model, Metadata, Permissions json.RawMessage
			Location                     struct{ Directory string }
		} `json:"data"`
	}
	must(json.Unmarshal(s.api("get", "/api/session/"+s.job.Agent.Session, "session-completion-session", nil), &session))
	check(session.Data.ID == s.job.Agent.Session && session.Data.ParentID == "" && session.Data.Agent == s.job.Request.Agent && session.Data.Location.Directory == s.job.Agent.Worktree && equalJSON(session.Data.Model, s.job.Request.Model) && equalJSON(session.Data.Metadata, expectedMetadata) && equalJSON(session.Data.Permissions, s.job.Request.Permissions), "session profile/ownership mismatch")
	storedOutput, err := os.ReadFile(s.job.Stdout)
	must(err)
	check(string(storedOutput) == output.String() && output.Len() > 0, "durable user output does not match real assistant text")
	check(snapshot() == immutable, "independent inspection changed immutable state or external mutations")
	s.note(fmt.Sprintf("PASS session-scoped-v1: %d API pages; one exact consumed user prompt; %d auxiliary events recorded; no tools; explicit succeeded/inactive/empty queues; validation not_run; held slots %d->%d; immutable receipts and exactly one worktree/session/prompt mutation unchanged", pages, len(auxiliary), heldBefore, heldAfter))
}
