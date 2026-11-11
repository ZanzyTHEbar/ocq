package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ocq/internal/core"
)

func encoded(v any) []byte   { b, e := json.Marshal(v); must(e); return b }
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func remainingTransfers(root string, origin map[string]string, command func(string), recheck func() bool, q *core.Queue) {
	state, repo := filepath.Join(root, "queue"), filepath.Join(root, "agent-repo")
	base := strings.TrimSpace(string(run("git", "-C", repo, "rev-parse", "HEAD")))
	job, e := q.SubmitAgent(core.AgentSubmission{Key: "publication-never-dispatched", Repo: repo, Base: base, Agent: "fixture-not-executed", Model: "fixture/never-run", Prompt: []byte("Private publication fixture; never execute"), Permissions: []byte(`[{"action":"*","resource":"*","effect":"deny"}]`)})
	must(e)
	resolutionJob, e := q.SubmitCommand("resolution-stored-unknown", root, []string{"/bin/true"}, 0)
	must(e)
	db, e := sql.Open("sqlite", filepath.Join(state, "queue.db"))
	must(e)
	defer db.Close()
	// Stored fixture seeding only, as in the existing publication acceptance.
	// No session, worktree provisioning or agent/API interaction is simulated.
	const project = "proj-private-transfer"
	const stamp = "2026-11-11T00:00:00Z"
	handoff := encoded(map[string]any{"version": 1, "kind": "handoff", "project_id": project, "source": "operator_import", "observed_at": nil, "intent": "Private local transfer fixture", "decisions": []string{}, "risks": []string{}})
	brief := encoded(map[string]any{"version": 1, "kind": "brief", "project_id": project, "source": "operator_import", "observed_at": nil, "goal": "Never execute", "scope": []string{"private transfer"}, "exclusions": []string{}, "acceptance_criteria": []string{"zero effects on invalidation"}, "base": base, "agent": "fixture-not-executed", "model": map[string]string{"id": "never-run", "providerID": "fixture"}, "permissions": json.RawMessage(`[{"action":"*","resource":"*","effect":"deny"}]`), "allowed_actions": []string{"read"}, "context_references": []string{}})
	tx, e := db.Begin()
	must(e)
	write := func(s string, args ...any) { _, e := tx.Exec(s, args...); must(e) }
	write("INSERT INTO projects(id,label,created_at) VALUES(?,?,?)", project, "Private transfer fixture", stamp)
	for _, m := range []struct {
		kind string
		b    []byte
	}{{"handoff", handoff}, {"brief", brief}} {
		write("INSERT INTO project_manifests(digest,project_id,version,kind,canonical,recorded_at) VALUES(?,?,1,?,?,?)", digest(m.b), project, m.kind, m.b, stamp)
	}
	write("INSERT INTO project_bindings(project_id,revision,repo_root,git_common_dir,commit_anchor,elephant_session_id,opencode_project_id,opencode_directory,handoff_manifest_digest,auth_ref,verified_at,recorded_at) VALUES(?,1,?,?,?,?,?,?,?,?,?,?)", project, repo, filepath.Join(repo, ".git"), base, "ses_fixture_not_created", "fixture-not-created", repo, digest(handoff), "fixture-reference-only", stamp, stamp)
	write("INSERT INTO project_jobs(job_id,project_id,binding_revision,role,brief_digest,recorded_at) VALUES(?,?,1,'implementation',?,?)", job.ID, project, digest(brief), stamp)
	write("UPDATE agent_ownership SET worktree=?,phase='worktree_created' WHERE job_id=?", repo, job.ID)
	write("INSERT INTO project_lanes(lane_id,project_id,owner_job_id,role,base_commit,fsfork_driver,fsfork_path,created_at) VALUES(?,?,?,'implementation',?,'git_worktree',?,?)", "lane-private-transfer", project, job.ID, base, repo, stamp)
	const reason = "Stored interrupted-command fixture; ownership unknown"
	write("UPDATE jobs SET state='unknown',reason=? WHERE id=?", reason, resolutionJob.ID)
	must(tx.Commit())
	must(os.WriteFile(filepath.Join(repo, "private-capture-input.txt"), []byte("PRIVATE_CAPTURE_MUST_NOT_BE_READ_BY_PREPARATION\n"), 0600))
	r, e := core.OpenReader(state)
	must(e)
	publication, e := r.ResultPublicationContext(origin["space"], project, job.ID)
	must(e)
	capture, e := r.CapturePublicationContext(origin["space"], project, job.ID)
	must(e)
	must(r.Close())
	manifest := core.ResultManifest{Version: 1, Kind: "result", ProjectID: project, Source: "operator_import", JobID: job.ID, RequestDigest: job.Digest, Base: base, Worktree: repo, StagedStatus: "clean", UnstagedStatus: "clean", UntrackedPaths: []string{}, IgnoredExclusions: []string{}, ChangedFiles: []core.ManifestChangedFile{}, BeforeStatusDigest: "not_recorded", AfterStatusDigest: "not_recorded", BeforeContentDigest: "not_recorded", AfterContentDigest: "not_recorded", InlineOutput: "PRIVATE_IMPORTED_ASSERTION", ClaimedValidation: core.ClaimedValidation{Status: "not_run", Commands: []string{}, Results: "not_run", Provenance: "operator_assertion", Limitations: "No tests run"}}
	manifestRaw := encoded(manifest)
	target := core.CommandResolutionTarget{Machine: "local", Space: origin["space"], Queue: publication.Target.Queue, Job: resolutionJob.ID, Sequence: strconv.FormatInt(resolutionJob.Sequence, 10)}
	payloads := map[string][]byte{
		"publication": encoded(core.ResultPublicationPayload{Target: publication.Target, OwnershipDigest: publication.OwnershipDigest, Manifest: string(manifestRaw)}),
		"capture":     encoded(core.CapturePreparationRequest{Context: capture, IgnoredExclusions: []string{}}),
		"resolution":  encoded(core.CommandResolutionPayload{Scope: core.CommandResolutionScope{CommandResolutionTarget: target, Kind: "command", State: "unknown", ReasonDigest: digest([]byte(reason))}, Reason: reason, Evidence: "PRIVATE_OPERATOR_ASSERTION_NOT_PROOF_OF_SUCCESS"}),
	}
	counts := func() []int {
		out := []int{}
		for _, query := range []string{"SELECT count(*) FROM project_result_associations", "SELECT count(*) FROM project_lanes WHERE sealed_manifest_digest IS NOT NULL OR sealed_at IS NOT NULL", "SELECT count(*) FROM project_inbox_messages", "SELECT count(*) FROM project_manifests", "SELECT count(*) FROM agent_operations"} {
			var n int
			must(db.QueryRow(query).Scan(&n))
			out = append(out, n)
		}
		return out
	}
	baseline := string(encoded(counts()))
	for _, family := range []string{"publication", "capture", "resolution"} {
		for _, step := range []string{"2", "4"} {
			for _, name := range []string{"transfer.waiting", "transfer.release", "transfer-done.json"} {
				mustRemove(filepath.Join(root, name))
			}
			file := "result-publication.js"
			id := job.ID
			if family == "resolution" {
				file = "command-resolution.js"
				id = resolutionJob.ID
			}
			captureID := digest([]byte(family + step + root))
			save(filepath.Join(root, "transfer-config.json"), map[string]string{"Family": family, "File": file, "Step": step, "Project": project, "Job": id, "Raw": string(payloads[family]), "Digest": digest(manifestRaw), "CaptureID": captureID})
			check(recheck(), "matched boundary setup failed")
			command("compat-boundary-open")
			wait("actual boundary context not loaded", func() bool {
				command("compat-snapshot")
				var s map[string]any
				load(filepath.Join(root, "snapshot.json"), &s)
				if family == "resolution" {
					v, _ := s["resolution"].(map[string]any)
					return v != nil && v["state"] == "none"
				}
				v, _ := s["publicationScope"].(map[string]any)
				return v != nil && s["publicationWorking"] == false
			})
			command("compat-transfer")
			wait("boundary receiver proof gate absent", func() bool { return exists(filepath.Join(root, "transfer.waiting")) })
			must(os.WriteFile(filepath.Join(root, "mode"), []byte("missing"), 0600))
			check(!recheck(), "failed check did not invalidate boundary")
			must(os.WriteFile(filepath.Join(root, "mode"), []byte("matched"), 0600))
			check(recheck(), "new matching boundary handshake unavailable")
			must(os.WriteFile(filepath.Join(root, "transfer.release"), nil, 0600))
			wait("boundary refusal missing", func() bool { return exists(filepath.Join(root, "transfer-done.json")) })
			var done struct{ Refused bool }
			load(filepath.Join(root, "transfer-done.json"), &done)
			check(done.Refused, "matching handshake resurrected boundary transfer")
			pubs, e := core.ListResultPublications(state, publication.Target)
			must(e)
			check(len(pubs) == 0, "publication intent generated")
			caps, e := core.ListCapturePublications(state, capture.Target)
			must(e)
			check(len(caps) == 0, "capture generated")
			resolution, e := core.InspectCommandResolution(state, target)
			must(e)
			check(resolution.State == "none" && resolution.IntentID == "", "resolution intent generated")
			check(string(encoded(counts())) == baseline, "invalidation changed result/lane/mail/manifest/operation state")
			save(filepath.Join(root, "transfer-"+family+"-"+step+".json"), map[string]any{"refused": true, "resurrection": false, "prepared_intents": 0, "captures": 0, "counts": counts(), "baseline": counts(), "unknown_command_seed": "stored_fixture_not_recovery_test"})
		}
	}
	for _, family := range []string{"publication", "capture", "resolution"} {
		// Positive controls AFTER all invalidation assertions prove the exact
		// requests are accepted, rather than malformed.
		// No publication/resolve attempt follows; capture control is explicitly
		// labelled as a new matched snapshot, never part of a denied exchange.
		mustRemove(filepath.Join(root, "transfer-done.json"))
		id := job.ID
		file := "result-publication.js"
		if family == "resolution" {
			id = resolutionJob.ID
			file = "command-resolution.js"
		}
		captureID := digest([]byte("matched-control" + family + root))
		save(filepath.Join(root, "transfer-config.json"), map[string]string{"Family": family, "File": file, "Step": "0", "Project": project, "Job": id, "Raw": string(payloads[family]), "Digest": digest(manifestRaw), "CaptureID": captureID})
		check(recheck(), "matched positive preparation unavailable")
		command("compat-boundary-open")
		wait("positive boundary context unavailable", func() bool {
			command("compat-snapshot")
			var s map[string]any
			load(filepath.Join(root, "snapshot.json"), &s)
			if family == "resolution" {
				v, _ := s["resolution"].(map[string]any)
				return v != nil && v["state"] == "none"
			}
			v, _ := s["publicationScope"].(map[string]any)
			return v != nil && s["publicationWorking"] == false
		})
		command("compat-transfer")
		wait("matched positive preparation did not finish", func() bool { return exists(filepath.Join(root, "transfer-done.json")) })
		var done struct{ Refused bool }
		load(filepath.Join(root, "transfer-done.json"), &done)
		check(!done.Refused, "exact private request rejected by positive control")
		if family == "publication" {
			s, e := core.InspectResultPublication(state, publication.Target, digest(manifestRaw))
			must(e)
			check(s.State == "prepared", "matched publication not prepared")
		}
		if family == "capture" {
			s, e := core.InspectCapturePublication(state, capture.Target, captureID)
			must(e)
			check(s.State == "prepared", "matched capture not prepared")
		}
		if family == "resolution" {
			s, e := core.InspectCommandResolution(state, target)
			must(e)
			check(s.State == "prepared", "matched resolution not prepared")
		}
		check(string(encoded(counts())) == baseline, "positive preparation unexpectedly published/sealed/mailed")
		save(filepath.Join(root, "matched-control-"+family+".json"), map[string]any{"prepared": true, "capture_control": family == "capture", "published": false, "resolved": false, "counts": counts()})
	}
}
