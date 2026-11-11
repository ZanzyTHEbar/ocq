// One real capture/bridge/filesystem scenario. Synthetic project ownership is
// explicitly seeded; no OpenCode service, agent, dispatcher or prompt runs.
package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"ocq/internal/core"
)

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func check(ok bool, s string) {
	if !ok {
		panic(s)
	}
}
func encode(v any) []byte  { b, e := json.Marshal(v); must(e); return b }
func hash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func run(args ...string) []byte {
	c := exec.Command(args[0], args[1:]...)
	b, e := c.CombinedOutput()
	if e != nil {
		panic(fmt.Sprintf("%s: %v: %s", args[0], e, b))
	}
	return b
}

func main() {
	check(len(os.Args) == 4 || len(os.Args) == 5 && os.Args[4] == "--fixture", "usage: herdr-worktree-check BRIDGE FRESH_PRIVATE_ROOT SOURCE_REPO [--fixture]")
	bridge, e := filepath.Abs(os.Args[1])
	must(e)
	root, e := filepath.Abs(os.Args[2])
	must(e)
	source, e := filepath.Abs(os.Args[3])
	must(e)
	must(os.Mkdir(root, 0700))
	state := filepath.Join(root, "queue")
	repo := filepath.Join(root, "repo")
	worktree := filepath.Join(root, "worktree")
	run("git", "-c", "core.hooksPath=/dev/null", "clone", "--no-hardlinks", "--no-checkout", source, repo)
	run("git", "-C", repo, "-c", "core.hooksPath=/dev/null", "worktree", "add", "--detach", worktree, "HEAD")
	base := strings.TrimSpace(string(run("git", "-C", worktree, "rev-parse", "HEAD")))
	file := filepath.Join(worktree, "ocq-disposable-result.txt")
	must(os.WriteFile(file, []byte("captured disposable result\n"), 0600))
	q, e := core.OpenQueue(state)
	must(e)
	job, e := q.SubmitAgent(core.AgentSubmission{Key: "private-never-sent-request", Repo: repo, Base: base, Agent: "fixture", Model: "fixture/never-executed", Prompt: []byte("PRIVATE_PROMPT_SENTINEL"), Permissions: []byte(`[{"action":"*","resource":"*","effect":"deny"}]`)})
	must(e)
	spare, e := q.SubmitCommand("never-executed-command", worktree, []string{"/bin/true"}, 0)
	must(e)
	const project = "proj-worktree-fixture"
	ts := "2026-11-11T00:00:00Z"
	handoff := encode(map[string]any{"version": 1, "kind": "handoff", "project_id": project, "source": "operator_import", "observed_at": nil, "intent": "Disposable filesystem verification", "decisions": []string{}, "risks": []string{}})
	brief := encode(map[string]any{"version": 1, "kind": "brief", "project_id": project, "source": "operator_import", "observed_at": nil, "goal": "Disposable capture", "scope": []string{"filesystem verification"}, "exclusions": []string{}, "acceptance_criteria": []string{"read only"}, "base": base, "agent": "fixture", "model": map[string]string{"id": "never-executed", "providerID": "fixture"}, "permissions": []map[string]string{{"action": "*", "resource": "*", "effect": "deny"}}, "allowed_actions": []string{"read"}, "context_references": []string{}})
	db, e := sql.Open("sqlite", filepath.Join(state, "queue.db"))
	must(e)
	tx, e := db.Begin()
	must(e)
	write := func(query string, args ...any) { _, e := tx.Exec(query, args...); must(e) }
	write("INSERT INTO projects(id,label,created_at) VALUES(?,?,?)", project, "Disposable worktree verification", ts)
	for _, m := range []struct {
		kind string
		b    []byte
	}{{"handoff", handoff}, {"brief", brief}} {
		write("INSERT INTO project_manifests(digest,project_id,version,kind,canonical,recorded_at) VALUES(?,?,1,?,?,?)", hash(m.b), project, m.kind, m.b, ts)
	}
	write("INSERT INTO project_bindings(project_id,revision,repo_root,git_common_dir,commit_anchor,elephant_session_id,opencode_project_id,opencode_directory,handoff_manifest_digest,auth_ref,verified_at,recorded_at) VALUES(?,1,?,?,?,?,?,?,?,?,?,?)", project, repo, filepath.Join(repo, ".git"), base, "ses_fixture_not_created", "fixture-not-created", repo, hash(handoff), "PRIVATE_AUTH_SENTINEL", ts, ts)
	write("INSERT INTO project_jobs(job_id,project_id,binding_revision,role,brief_digest,recorded_at) VALUES(?,?,1,'implementation',?,?)", job.ID, project, hash(brief), ts)
	write("UPDATE agent_ownership SET phase='worktree_created',terminal_evidence='PRIVATE_EVIDENCE_SENTINEL' WHERE job_id=?", job.ID)
	must(tx.Commit())
	must(db.Close())
	// Capture's existing explicit fallback can seal a lane before agent
	// ownership records a path. Verification must not silently prefer a later,
	// conflicting agent checkout, even if its file state is identical.
	association, e := q.CaptureResult(core.CaptureRequest{ProjectID: project, JobID: job.ID, Worktree: worktree})
	must(e)
	r, e := core.OpenReader(state)
	must(e)
	raw, e := r.Manifest(project, association.ManifestDigest)
	must(e)
	must(r.Close())
	var imported core.ResultManifest
	must(json.Unmarshal(raw, &imported))
	imported.Source = "operator_import"
	imported.ClaimLabel = ""
	imported.BeforeContentDigest = "not_recorded"
	imported.AfterContentDigest = "not_recorded"
	imported.InlineOutput = "PRIVATE_EVIDENCE_SENTINEL"
	imported.ClaimedValidation = core.ClaimedValidation{Status: "claimed_pass", Commands: []string{"PRIVATE_VALIDATION_SENTINEL"}, Results: "PRIVATE_EVIDENCE_SENTINEL", Provenance: "operator_assertion", Limitations: "not independently checked"}
	importAssociation, e := q.RecordResult(project, job.ID, encode(imported), association.ID)
	must(e)
	must(q.Close())
	alternate := filepath.Join(root, "alternate-worktree")
	run("git", "-C", repo, "-c", "core.hooksPath=/dev/null", "worktree", "add", "--detach", alternate, "HEAD")
	must(os.WriteFile(filepath.Join(alternate, filepath.Base(file)), []byte("captured disposable result\n"), 0600))
	setAgentPath := func(path string) {
		db, e := sql.Open("sqlite", filepath.Join(state, "queue.db"))
		must(e)
		_, e = db.Exec("UPDATE agent_ownership SET worktree=? WHERE job_id=?", path, job.ID)
		must(e)
		must(db.Close())
	}
	setAgentPath(worktree)
	target := []string{"verify-worktree", project, job.ID, association.ID, association.ManifestDigest, worktree}
	if len(os.Args) == 5 {
		must(json.NewEncoder(os.Stdout).Encode(map[string]any{"state_dir": state, "project": project, "job": job.ID, "spare": spare.ID, "worktree": worktree, "alternate_worktree": alternate, "file": file, "association": association, "imported": importAssociation, "args": target, "note": "seeded owned checkout only; real core capture; no agent/session/prompt/dispatch"}))
		return
	}
	// Every persisted table, including mail and notifications, remains identical
	// across the check. Git may read index metadata; SQLite is not a journal here.
	ro, e := sql.Open("sqlite", "file:"+filepath.Join(state, "queue.db")+"?mode=ro")
	must(e)
	defer ro.Close()
	snapshot := func() []byte {
		rows, e := ro.Query("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
		must(e)
		var names []string
		for rows.Next() {
			var n string
			must(rows.Scan(&n))
			names = append(names, n)
		}
		must(rows.Err())
		must(rows.Close())
		var result []any
		for _, n := range names {
			rows, e := ro.Query(`SELECT * FROM "` + strings.ReplaceAll(n, `"`, `""`) + `" ORDER BY rowid`)
			must(e)
			cols, e := rows.Columns()
			must(e)
			for rows.Next() {
				v := make([]any, len(cols))
				p := make([]any, len(cols))
				for i := range v {
					p[i] = &v[i]
				}
				must(rows.Scan(p...))
				result = append(result, []any{n, v})
			}
			must(rows.Err())
			must(rows.Close())
		}
		return encode(result)
	}
	call := func(dir string, args []string) map[string]any {
		c := exec.Command(bridge, args...)
		// Verification must override even an inherited request for optional Git
		// writes, not accidentally rely on the fixture environment disabling them.
		c.Env = append(os.Environ(), "OCQ_STATE_DIR="+dir, "GIT_OPTIONAL_LOCKS=1")
		b, e := c.Output()
		must(e)
		for _, s := range []string{"PRIVATE_", job.Agent.Token, `"request"`, `"prompt"`, `"evidence"`, `"differences"`} {
			check(!bytes.Contains(b, []byte(s)), "private data in safe bridge DTO")
		}
		var out map[string]any
		must(json.Unmarshal(b, &out))
		return out
	}
	setAgentPath(alternate)
	divergentBefore := snapshot()
	conflicting := append([]string{}, target...)
	conflicting[5] = alternate
	divergent := call(state, conflicting)
	check(divergent["worktree_match"] == "unknown" && divergent["checked_at"] == nil && strings.Contains(divergent["error"].(string), "lane"), "matching alternate agent checkout bypassed retained lane ownership")
	check(bytes.Equal(divergentBefore, snapshot()), "divergent-path verification mutated SQLite tables")
	setAgentPath(worktree)
	before := snapshot()
	indexPath := strings.TrimSpace(string(run("git", "-C", worktree, "rev-parse", "--path-format=absolute", "--git-path", "index")))
	// Make a tracked entry's cached stat stale without changing its contents.
	// Ordinary `git status` would refresh this entry and rewrite the index.
	var tracked string
	for _, name := range bytes.Split(run("git", "-C", worktree, "ls-files", "-z"), []byte{0}) {
		path := filepath.Join(worktree, string(name))
		if info, e := os.Lstat(path); e == nil && info.Mode().IsRegular() {
			tracked = path
			break
		}
	}
	check(tracked != "", "fixture needs one regular tracked file for index-write detection")
	stale := time.Now().Add(-time.Hour)
	must(os.Chtimes(tracked, stale, stale))
	indexBefore, e := os.ReadFile(indexPath)
	must(e)
	indexInfo, e := os.Stat(indexPath)
	must(e)
	checkIndex := func() {
		after, e := os.ReadFile(indexPath)
		must(e)
		info, e := os.Stat(indexPath)
		must(e)
		check(bytes.Equal(indexBefore, after) && indexInfo.ModTime().Equal(info.ModTime()), "verification refreshed/wrote Git index bytes or mtime")
	}
	match := call(state, target)
	check(match["worktree_match"] == "matches_snapshot" && match["manifest_digest"] == association.ManifestDigest && match["checked_at"] != "" && match["validation"] == "not_run" && match["integration"] == "not_authorized", "real capture did not match")
	checkIndex()
	must(os.WriteFile(file, []byte("changed after capture\n"), 0600))
	trackedContents, e := os.ReadFile(tracked)
	must(e)
	must(os.WriteFile(tracked, append(append([]byte{}, trackedContents...), []byte("\nfixture tracked edit\n")...), 0600))
	drift := call(state, target)
	check(drift["worktree_match"] == "modified_since_snapshot" && drift["difference_count"].(float64) > 0 && drift["checked_at"] != match["checked_at"] && drift["validation"] == "not_run" && drift["integration"] == "not_authorized", "explicit recheck reused old match")
	checkIndex()
	must(os.WriteFile(tracked, trackedContents, 0600))
	bad := append([]string{}, target...)
	bad[4] = strings.Repeat("a", 64)
	check(call(state, bad)["worktree_match"] == "unknown", "wrong digest accepted")
	bad = append([]string{}, target...)
	bad[5] = repo
	check(call(state, bad)["worktree_match"] == "unknown", "arbitrary path bypassed ownership")
	bad = append([]string{}, target...)
	bad[1] = "proj-missing"
	check(call(state, bad)["worktree_match"] == "unknown", "wrong project reused good check")
	bad = append([]string{}, target...)
	bad[3] = importAssociation.ID
	bad[4] = importAssociation.ManifestDigest
	claim := call(state, bad)
	check(claim["worktree_match"] == "unknown" && claim["checked_at"] == nil && claim["verification"] == "structure_hash_linkage_only" && strings.Contains(claim["error"].(string), "imported claims"), "imported claim promoted to machine verification")
	must(os.Rename(worktree, worktree+".unavailable"))
	unavailable := call(state, target)
	check(unavailable["worktree_match"] == "worktree_unavailable" && unavailable["error"] != "", "missing worktree reused match")
	must(os.Rename(worktree+".unavailable", worktree))
	checkIndex()
	check(bytes.Equal(before, snapshot()), "filesystem verification mutated SQLite tables")
	missing := filepath.Join(root, "missing")
	c := exec.Command(bridge, target...)
	c.Env = append(os.Environ(), "OCQ_STATE_DIR="+missing)
	check(c.Run() != nil, "missing store accepted")
	_, e = os.Stat(missing)
	check(os.IsNotExist(e), "reader initialized missing store")
	must(json.NewEncoder(os.Stdout).Encode(map[string]any{"result": "PASS", "state_dir": state, "project": project, "job": job.ID, "spare": spare.ID, "worktree": worktree, "alternate_worktree": alternate, "file": file, "association": association, "imported": importAssociation, "args": target, "index_sha256": hash(indexBefore), "index_mtime": indexInfo.ModTime().UTC().Format(time.RFC3339Nano), "checks": "real core fallback capture; conflicting identical agent checkout refused against retained lane; bridge match -> file edit -> differs; stale tracked stat cache with Git index bytes/mtime unchanged; exact association/digest/project/owned path; imported claims refused without filesystem check; unavailable checkout no stale match; safe DTO; all SQLite tables unchanged around each check; missing store noninitializing"}))
}
