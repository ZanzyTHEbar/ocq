package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ocq/internal/core"
)

// Focused correction proof, deliberately not a replacement for the outstanding
// real selected-machine race. Uses the running native mod and real receivers.
func nativeCorrections(root, app string, origin map[string]string, command func(string), recheck func() bool, q *core.Queue) {
	waitStatus := func(key string) {
		wait("native form not loaded", func() bool {
			command("compat-snapshot")
			var s map[string]any
			load(filepath.Join(root, "snapshot.json"), &s)
			v, _ := s[key].(map[string]any)
			return v != nil && v["state"] == "idle"
		})
	}
	command("compat-command-open")
	waitStatus("submission")
	jobsBefore, e := q.Jobs()
	must(e)
	check(len(jobsBefore) == 1, "unexpected fixture jobs")
	composeRaw, e := json.Marshal(map[string]string{"ProjectID": "proj-correction", "LaneID": "lane-correction", "JobID": jobsBefore[0].ID, "From": "lane:lane-correction", "To": "integrator:proj-correction", "Kind": "lane.blocked", "Subject": "private transfer fixture", "Body": "PRIVATE_BODY_MUST_NOT_BE_WRITTEN"})
	must(e)
	zero := 0
	registration, e := core.BuildProjectRegistrationDraft(core.ProjectRegistrationDraft{Mode: "new", Key: "private-transfer-registration", ProjectID: "proj-correction", Label: "Fixture", ExpectedRevision: &zero, Repo: root, CommitAnchor: strings.Repeat("a", 40), ElephantSessionID: "ses_fixture", ExpectedOpenCodeProjectID: "fixture", ExpectedOpenCodeDirectory: root, AuthRef: "fixture-not-used", Intent: "PRIVATE_TRANSFER_MUST_NOT_BE_WRITTEN", Decisions: []string{}, Risks: []string{}})
	must(e)
	for _, test := range []struct{ family, file, step string }{{"compose", "main.js", "2"}, {"intake", "main.js", "2"}, {"command", "command-submission.js", "2"}, {"command", "command-submission.js", "4"}} {
		for _, name := range []string{"transfer.waiting", "transfer.release", "transfer-done.json"} {
			mustRemove(filepath.Join(root, name))
		}
		raw := ""
		if test.family == "compose" {
			raw = string(composeRaw)
		}
		if test.family == "intake" {
			raw = string(registration)
		}
		save(filepath.Join(root, "transfer-config.json"), map[string]string{"Family": test.family, "File": test.file, "Step": test.step, "Raw": raw})
		check(recheck(), "matched transfer setup failed")
		command("compat-transfer")
		wait("real receiver checkpoint absent", func() bool { return exists(filepath.Join(root, "transfer.waiting")) })
		must(os.WriteFile(filepath.Join(root, "mode"), []byte("missing"), 0600))
		check(!recheck(), "failed compatibility did not disable transfer")
		must(os.WriteFile(filepath.Join(root, "mode"), []byte("matched"), 0600))
		check(recheck(), "fresh matching handshake unavailable")
		must(os.WriteFile(filepath.Join(root, "transfer.release"), nil, 0600))
		wait("invalidated exchange did not finish", func() bool { return exists(filepath.Join(root, "transfer-done.json")) })
		var done struct{ Refused bool }
		load(filepath.Join(root, "transfer-done.json"), &done)
		check(done.Refused, "new handshake revived old transfer")
		var state core.CommandSubmissionStatus
		if test.family == "compose" {
			s, e := core.LocalComposeStatus(filepath.Join(root, "queue"))
			must(e)
			check(s.State == "idle", "blocked compose prepared an intent")
		}
		if test.family == "intake" {
			s, e := core.LocalProjectIntakeStatus(filepath.Join(root, "queue"))
			must(e)
			check(s.State == "idle", "blocked intake prepared an intent")
		}
		if test.family == "command" {
			r, e := core.OpenReader(filepath.Join(root, "queue"))
			must(e)
			s, e := r.CommandSubmissionContext(origin["space"])
			must(e)
			must(r.Close())
			state, e = core.InspectCommandSubmission(filepath.Join(root, "queue"), s)
			must(e)
			check(state.State == "idle", "blocked command transfer prepared a journal")
		}
		save(filepath.Join(root, "transfer-"+test.family+"-"+test.step+".json"), map[string]any{"refused": true, "resurrection": false, "checkpoint": test.step, "journal": state.State})
	}
	// The actual agent form replacement and inherited call path, with a real
	// immutable request journal but no admission, API lookup or agent execution.
	repo := filepath.Join(root, "agent-repo")
	run("git", "init", "-q", repo)
	run("git", "-C", repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-qm", "fixture")
	base := strings.TrimSpace(string(run("git", "-C", repo, "rev-parse", "HEAD")))
	r, e := core.OpenReader(filepath.Join(root, "queue"))
	must(e)
	scope, e := r.CommandSubmissionContext(origin["space"])
	must(e)
	must(r.Close())
	built, e := core.BuildAgentSubmissionDraft(scope, core.AgentSubmissionDraft{Key: "replacement-cancel-no-admission", Repo: repo, Base: base, Prompt: "Never execute this acceptance request", Agent: "fixture-never-executed", ProviderID: "fixture", ModelID: "never-run", Memory: "0", Permissions: `[{"action":"*","resource":"*","effect":"deny"}]`})
	must(e)
	var request struct {
		Canonical string `json:"canonical"`
	}
	must(json.Unmarshal(built, &request))
	importBytes, e := json.Marshal(map[string]string{"key": "replacement-cancel-no-admission", "request": request.Canonical})
	must(e)
	save(filepath.Join(root, "transfer-config.json"), map[string]string{"Import": string(importBytes)})
	command("compat-agent-open")
	waitStatus("agentSubmission")
	command("compat-agent-mode-cancel")
	agent, e := core.InspectAgentSubmission(filepath.Join(root, "queue"), scope)
	must(e)
	check(agent.State == "cancelled", "replacement agent request not cancelled")
	jobs, e := q.Jobs()
	must(e)
	check(len(jobs) == 1, "correction flow admitted/duplicated a job")
	remainingTransfers(root, origin, command, recheck, q)
	save(filepath.Join(root, "corrections-result.json"), map[string]any{"agent_mode": "guided_import_guided_import_prepared_armed_cancelled", "jobs": len(jobs), "private_transfers": "invalidated_before_auth_and_between_framing_writes", "selected_machine": "NOT_RUN_separate_gap"})
	run(app, "do", "space.close", "--id", origin["space"], "--confirmed", "true")
	fmt.Println("PASS focused native corrections: real receiver transfers refused after invalidation/new handshake; replaced agent form prepare/arm/cancel, zero admissions; selected-machine gap remains")
}

func mustRemove(path string) {
	e := os.Remove(path)
	if !os.IsNotExist(e) {
		must(e)
	}
}
