package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"

	"ocq/internal/core"
)

const (
	privacyProject   = "proj-bridge-privacy"
	privacyJob       = "job-bridge-privacy"
	privacySession   = "ses_bridge_privacy"
	promptSentinel   = "PRIVATE_PROMPT_SENTINEL"
	tokenSentinel    = "PRIVATE_OWNER_TOKEN_SENTINEL"
	evidenceSentinel = "PRIVATE_EVIDENCE_BODY_SENTINEL"
	authSentinel     = "PRIVATE_AUTH_REF_SENTINEL"
)

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func encode(value any) []byte   { data, err := json.Marshal(value); must(err); return data }

// Persist a terminal agent fixture without provisioning, prompting or executing
// an agent. Then use the real core publication flow to create its result/mail.
func seedProject(state, root string) (string, []byte) {
	worktree := filepath.Join(root, "recorded-worktree")
	base := strings.Repeat("a", 40)
	request := encode(map[string]any{"version": 2, "kind": "agent", "repo": root, "base": base, "prompt": promptSentinel, "agent": "fixture", "model": map[string]string{"id": "fixture", "providerID": "fixture"}, "permissions": []map[string]string{{"action": "*", "resource": "*", "effect": "deny"}}, "provision_policy": "workmux-headless-no-hooks-no-file-ops-v1", "memory_bytes": 0})
	handoff := encode(map[string]any{"version": 1, "kind": "handoff", "project_id": privacyProject, "source": "operator_import", "observed_at": nil, "intent": "Inspect stored bridge metadata", "decisions": []string{}, "risks": []string{}})
	brief := encode(map[string]any{"version": 1, "kind": "brief", "project_id": privacyProject, "source": "operator_import", "observed_at": nil,
		"goal": "Inspect stored bridge metadata", "scope": []string{"stored views"}, "exclusions": []string{}, "acceptance_criteria": []string{"no private input in summaries"}, "base": base, "agent": "fixture",
		"model": map[string]string{"id": "fixture", "providerID": "fixture"}, "permissions": []map[string]string{{"action": "*", "resource": "*", "effect": "deny"}}, "allowed_actions": []string{"read"}, "context_references": []string{}})
	db, err := sql.Open("sqlite", filepath.Join(state, "queue.db"))
	must(err)
	_, err = db.Exec("PRAGMA foreign_keys=ON")
	must(err)
	tx, err := db.Begin()
	must(err)
	exec := func(query string, args ...any) { _, err := tx.Exec(query, args...); must(err) }
	ts := "2026-11-11T00:00:00Z"
	exec("INSERT INTO projects(id,label,created_at) VALUES(?,?,?)", privacyProject, "Bridge privacy fixture", ts)
	for _, m := range []struct {
		kind string
		data []byte
	}{{"handoff", handoff}, {"brief", brief}} {
		exec("INSERT INTO project_manifests(digest,project_id,version,kind,canonical,recorded_at) VALUES(?,?,1,?,?,?)", digest(m.data), privacyProject, m.kind, m.data, ts)
	}
	exec("INSERT INTO project_bindings(project_id,revision,repo_root,git_common_dir,commit_anchor,elephant_session_id,opencode_project_id,opencode_directory,handoff_manifest_digest,auth_ref,verified_at,recorded_at) VALUES(?,1,?,?,?,?,?,?,?,?,?,?)", privacyProject, root, filepath.Join(root, ".git"), base, "ses_integrator_fixture", "fixture-project", root, digest(handoff), authSentinel, ts, ts)
	exec("INSERT INTO receipts(key,job_id,canonical,digest,accepted_at) VALUES(?,?,?,?,?)", "privacy-fixture", privacyJob, request, digest(request), ts)
	exec("INSERT INTO jobs(id,state,evidence) VALUES(?,'queued',?)", privacyJob, evidenceSentinel)
	exec("INSERT INTO agent_ownership(job_id,session_id,prompt_id,token,phase,worktree,project_id,server_outcome,terminal_evidence) VALUES(?,?,?,?,?,?,?,?,?)", privacyJob, privacySession, "msg_PRIVATE_PROMPT_ID_SENTINEL", tokenSentinel, "completed", worktree, "fixture-project", "completed", evidenceSentinel)
	exec("INSERT INTO project_jobs(job_id,project_id,binding_revision,role,brief_digest,recorded_at) VALUES(?,?,1,'implementation',?,?)", privacyJob, privacyProject, digest(brief), ts)
	exec("INSERT INTO project_lanes(lane_id,project_id,owner_job_id,role,base_commit,fsfork_driver,fsfork_path,created_at) VALUES(?,?,?,'implementation',?,'git_worktree',?,?)", "lane-privacy", privacyProject, privacyJob, base, worktree, ts)
	exec("UPDATE jobs SET state='finished',outcome='completed',reason='seeded terminal observation; no agent executed' WHERE id=?", privacyJob)
	must(tx.Commit())
	must(db.Close())
	manifest := encode(core.ResultManifest{Version: 1, Kind: "result", ProjectID: privacyProject, Source: "operator_import", JobID: privacyJob, RequestDigest: digest(request), Base: base, Worktree: worktree,
		StagedStatus: "clean", UnstagedStatus: "clean", UntrackedPaths: []string{}, IgnoredExclusions: []string{}, ChangedFiles: []core.ManifestChangedFile{},
		BeforeStatusDigest: "not_recorded", AfterStatusDigest: "not_recorded", BeforeContentDigest: "not_recorded", AfterContentDigest: "not_recorded",
		InlineOutput: evidenceSentinel, ClaimedValidation: core.ClaimedValidation{Status: "claimed_pass", Commands: []string{evidenceSentinel}, Results: evidenceSentinel, Provenance: "operator_assertion", Limitations: evidenceSentinel}})
	q, err := core.OpenQueue(state)
	must(err)
	_, err = q.RecordResult(privacyProject, privacyJob, manifest, "")
	must(err)
	must(q.Close())
	return worktree, manifest
}

func checkProjectPrivacy(decode func(...string) map[string]any, worktree string) {
	checkAgent := func(job map[string]any) {
		check(job["id"] == privacyJob && job["state"] == "finished" && job["outcome"] == "completed" && job["validation"] == "not_run", "stored project job metadata mismatch")
		agent := job["agent"].(map[string]any)
		check(agent["session_id"] == privacySession && agent["worktree"] == worktree && agent["phase"] == "completed", "recorded agent metadata missing")
		check(len(agent) == 5, "agent projection unexpectedly includes private fields")
	}
	detail := decode("job", privacyProject, privacyJob)
	check(detail["project_id"] == privacyProject && detail["role"] == "implementation" && detail["fsfork_path"] == worktree, "project detail metadata mismatch")
	checkAgent(detail["job"].(map[string]any))
	validation := detail["validation"].(map[string]any)
	claim := validation["claimed_validation"].(map[string]any)
	check(claim["status"] == "claimed_pass" && claim["verified"] == false && len(claim) == 7, "claimed validation metadata mismatch")
	for _, args := range [][]string{{"recent-results", privacyProject}, {"results", privacyProject, "0"}} {
		page := decode(args...)
		items := page["items"].([]any)
		check(len(items) == 1, "project result missing")
		item := items[0].(map[string]any)
		checkAgent(item["current_job"].(map[string]any))
		event := item["event"].(map[string]any)
		check(event["job_id"] == privacyJob && event["acknowledged_at"] == nil, "frozen project event mismatch")
		summary := item["result_summary"].(map[string]any)
		check(summary["association_count"] == float64(1) && summary["latest_manifest_digest"] == claim["manifest_digest"], "result association metadata mismatch")
	}
	board := decode("board", privacyProject)
	project := board["project"].(map[string]any)
	jobs := project["jobs"].([]any)
	check(len(jobs) == 1, "board job missing")
	boardClaim := jobs[0].(map[string]any)["validation"].(map[string]any)["claimed_validation"].(map[string]any)
	check(boardClaim["manifest_digest"] == claim["manifest_digest"] && len(boardClaim) == 7, "board validation metadata mismatch")
	projects := decode("projects")["projects"].([]any)
	check(len(projects) == 1 && projects[0].(map[string]any)["project_id"] == privacyProject, "project summary missing")
	for _, args := range [][]string{{"recent-inbox", privacyProject}, {"inbox", privacyProject, "0"}} {
		check(len(decode(args...)["messages"].([]any)) == 1, "published mail missing")
	}
	mail := board["messages"].([]any)[0].(map[string]any)
	exact := decode("mail", privacyProject, mail["id"].(string), "1")["message"].(map[string]any)
	check(exact["body"] == mail["body"] && exact["kind"] == "work.submitted", "exact mail content changed")
}
