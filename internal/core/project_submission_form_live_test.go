package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// One opt-in real native implementation -> imported result -> native review
// acceptance scenario. No mock runtime, dispatcher or producer completion.
func TestSubmissionFormLiveFixture(t *testing.T) {
	root := os.Getenv("OCQ_SUBMISSION_FIXTURE_ROOT")
	if root == "" {
		t.Skip("requires explicitly owned native and OpenCode fixture")
	}
	info, err := os.Stat(root)
	if err != nil || !filepath.IsAbs(root) || info.Mode() != os.ModeDir|0700 {
		t.Fatal("private absolute fixture root required")
	}
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	encode := func(v any) []byte { t.Helper(); b, e := json.Marshal(v); must(e); return b }
	load := func(name string) []byte {
		t.Helper()
		b, e := os.ReadFile(filepath.Join(root, name))
		must(e)
		return b
	}
	write := func(name string, b []byte) { t.Helper(); must(os.WriteFile(filepath.Join(root, name), b, 0600)) }
	repo, state := filepath.Join(root, "repo"), filepath.Join(root, "queue")
	var coordinator struct {
		Data sessionInfo `json:"data"`
	}
	must(json.Unmarshal(load("coordinator-get.json"), &coordinator))
	if coordinator.Data.Location.Directory != repo || !isValidSessionID(coordinator.Data.ID) {
		t.Fatal("exact owned coordinator required")
	}
	const project = "proj-guided-submission"
	phase := os.Getenv("OCQ_SUBMISSION_FIXTURE_PHASE")
	if phase == "prepare" {
		if _, e := os.Lstat(state); !os.IsNotExist(e) {
			t.Fatal("fresh queue required; do not retry uncertain fixture")
		}
		// Real current same-location LIST readiness, not equality with a saved
		// specimen or an invented agent. Stop if the explicitly requested ID is absent.
		b, e := apiCall(repo, "get", "/api/agent?location[directory]="+url.QueryEscape(repo), nil)
		must(e)
		var available struct {
			Location struct {
				Directory string `json:"directory"`
			} `json:"location"`
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		must(json.Unmarshal(b, &available))
		found := false
		for _, agent := range available.Data {
			if agent.ID == "build" {
				found = true
			}
		}
		if available.Location.Directory != repo || !found {
			t.Fatal("explicit build reviewer unavailable at exact fixture location")
		}
		write("agents-readiness.json", b)
		base, e := gitRead(repo, "rev-parse", "HEAD")
		must(e)
		q, e := OpenQueue(state)
		must(e)
		zero := 0
		registration, e := BuildProjectRegistrationDraft(ProjectRegistrationDraft{Mode: "new", Key: "register-guided", ProjectID: project, Label: "Guided submission fixture", ExpectedRevision: &zero, Repo: repo, CommitAnchor: base, ElephantSessionID: coordinator.Data.ID, ExpectedOpenCodeProjectID: coordinator.Data.ProjectID, ExpectedOpenCodeDirectory: repo, AuthRef: "owned-fixture-reference", Intent: "PRIVATE_GUIDED_FIXTURE", Decisions: []string{}, Risks: []string{}})
		must(e)
		registered, e := q.RegisterProject(registration)
		must(e)
		must(q.Close())
		memory := int64(1048576)
		profile := ProjectJobDraftProfile{Key: "guided-implementation", ProjectID: project, BindingRevision: 1, LaneID: "lane-guided-implementation", Repo: repo, Base: base, Agent: "build", ProviderID: "fixture", ModelID: "never-run", Variant: "native-explicit", Permissions: encode([]permissionRule{{Action: "*", Resource: "*", Effect: "deny"}}), MemoryBytes: &memory, ReferencedManifestDigests: []string{registered.HandoffManifestDigest}}
		draft := ProjectImplementationDraft{ProjectJobDraftProfile: profile, Prompt: "PRIVATE_IMPL café 東京\nNever invoked in this acceptance fixture.", Goal: "PRIVATE_GOAL α\nQueued only, not validation.", Scope: []string{"review.txt"}, Exclusions: []string{}, AcceptanceCriteria: []string{"Keep exact bytes", "No execution"}, AllowedActions: []string{"read"}, ContextReferences: []string{}}
		r, e := OpenReader(state)
		must(e)
		raw, e := r.BuildProjectImplementationDraft(draft)
		must(e)
		must(r.Close())
		write("implementation-draft.json", encode(draft))
		write("expected-implementation.json", raw)
		write("fixture-meta.json", encode(map[string]string{"project": project, "handoff_digest": registered.HandoffManifestDigest}))
		t.Log("PASS real same-location reviewer list readiness, registered project, exact implementation draft; zero submissions")
		return
	}
	if phase != "implementation-accepted" && phase != "verify" && phase != "scope-alternate" {
		t.Fatal("explicit prepare, implementation-accepted, verify or scope-alternate phase required")
	}
	r, e := OpenReader(state)
	must(e)
	var implementation ProjectImplementationDraft
	must(json.Unmarshal(load("implementation-draft.json"), &implementation))
	implRaw, e := r.BuildProjectImplementationDraft(implementation)
	must(e)
	if !bytes.Equal(implRaw, load("expected-implementation.json")) {
		t.Fatal("implementation draft bytes changed")
	}
	impl, e := r.SubmissionReceipt(implRaw)
	must(e)
	if impl.Role != "implementation" || impl.BindingRevision != 1 {
		t.Fatal("implementation receipt mismatch")
	}
	implParsed, e := parseSubmissionEnvelope(implRaw)
	must(e)
	if implParsed.Request.Model.Variant != implementation.Variant || implParsed.Request.MemoryBytes != *implementation.MemoryBytes || !reflect.DeepEqual(implParsed.BriefManifest.Brief.Permissions, implParsed.Request.Permissions) {
		t.Fatal("canonical profile/brief linkage mismatch")
	}
	if phase == "scope-alternate" {
		// A real, structurally valid alternate recorded target lets native scope
		// refusal be checked BEFORE receive, rather than testing malformed JSON.
		must(r.Close())
		var original ProjectResultAssociation
		must(json.Unmarshal(load("target-association.json"), &original))
		var target ResultManifest
		must(json.Unmarshal(load("target-result.json"), &target))
		target.InlineOutput = "PRIVATE_ALTERNATE_IMPORTED_CLAIM"
		q, e := OpenExistingProjectIntake(state)
		must(e)
		alternate, e := (&Queue{store: q.store}).RecordResult(project, impl.JobID, encode(target), original.ID)
		must(e)
		must(q.Close())
		var draft ProjectReviewDraft
		must(json.Unmarshal(load("review-draft.json"), &draft))
		draft.Key, draft.LaneID = "guided-alternate-not-sent", "lane-alternate-not-sent"
		draft.TargetAssociationID, draft.TargetResultDigest = alternate.ID, alternate.ManifestDigest
		r, e = OpenReader(state)
		must(e)
		raw, e := r.BuildProjectReviewDraft(draft)
		must(e)
		must(r.Close())
		write("alternate-valid-review.json", raw)
		write("alternate-association.json", encode(alternate))
		t.Log("PASS real alternate target/core-built review, no submit/API; ready for native selected-target refusal")
		return
	}
	if phase == "implementation-accepted" {
		status, e := LocalProjectIntakeStatus(state)
		must(e)
		retained, e := ReadLocalProjectIntake(state, status.ID)
		must(e)
		if !bytes.Equal(retained.Request, implRaw) {
			t.Fatal("native implementation retained bytes differ")
		}
		must(r.Close())
		q, e := OpenExistingProjectIntake(state)
		must(e)
		contents := "PRIVATE_TARGET café 東京\nUnicode\u202e\u200b\n\x01"
		result := ResultManifest{Version: 1, Kind: "result", ProjectID: project, Source: "operator_import", JobID: impl.JobID, RequestDigest: implParsed.RequestDigest, Base: implementation.Base, Worktree: repo, StagedStatus: "clean", UnstagedStatus: "clean", UntrackedPaths: []string{"review.txt"}, BeforeStatusDigest: "not_recorded", AfterStatusDigest: "not_recorded", BeforeContentDigest: "not_recorded", AfterContentDigest: "not_recorded", IgnoredExclusions: []string{}, ChangedFiles: []ManifestChangedFile{{Path: "review.txt", OldMode: "000000", NewMode: "100644", Contents: &contents}}, Diff: "diff --git a/review.txt b/review.txt\nnew file mode 100644\n--- /dev/null\n+++ b/review.txt\n@@ -0,0 +1 @@\n+PRIVATE_TARGET\n", InlineOutput: "PRIVATE_IMPORTED_CLAIM", ClaimedValidation: ClaimedValidation{Status: "not_run", Commands: []string{}, Results: "not_run", Provenance: "not_recorded", Limitations: "Imported claims only; no producer, tests or integration"}}
		targetRaw := encode(result)
		association, e := (&Queue{store: q.store}).RecordResult(project, impl.JobID, targetRaw, "")
		must(e)
		must(q.Close())
		profile := implementation.ProjectJobDraftProfile
		profile.Key, profile.LaneID = "guided-review", "lane-guided-review"
		profile.Permissions = encode([]permissionRule{{Action: "*", Resource: "*", Effect: "deny"}, {Action: "read", Resource: "*", Effect: "allow"}, {Action: "glob", Resource: "*", Effect: "allow"}, {Action: "grep", Resource: "*", Effect: "allow"}})
		draft := ProjectReviewDraft{ProjectJobDraftProfile: profile, TargetAssociationID: association.ID, TargetJobID: impl.JobID, TargetResultDigest: association.ManifestDigest, Criteria: "PRIVATE_CRITERIA β\nInspect all target data.\u202e\u200b", Rules: "PRIVATE_RULES γ\nStatic review only; do not execute or edit."}
		r, e = OpenReader(state)
		must(e)
		raw, e := r.BuildProjectReviewDraft(draft)
		must(e)
		oversized := draft
		oversized.Criteria = strings.Repeat("x", maxReviewPromptBytes)
		tooLarge, e := r.BuildProjectReviewDraft(oversized)
		if e == nil || len(tooLarge) != 0 {
			t.Fatal("oversized core review prompt was truncated/accepted")
		}
		write("review-draft.json", encode(draft))
		write("expected-review.json", raw)
		write("target-result.json", targetRaw)
		write("target-association.json", encode(association))
		must(r.Close())
		t.Log("PASS real native implementation exact receipt; imported exact target and core-built review ready; oversized prompt rejected; no execution")
	} else {
		defer r.Close()
		var draft ProjectReviewDraft
		must(json.Unmarshal(load("review-draft.json"), &draft))
		raw, e := r.BuildProjectReviewDraft(draft)
		must(e)
		if !bytes.Equal(raw, load("expected-review.json")) {
			t.Fatal("review draft bytes changed")
		}
		status, e := LocalProjectIntakeStatus(state)
		must(e)
		intent, e := ReadLocalProjectIntake(state, status.ID)
		must(e)
		if !bytes.Equal(intent.Request, raw) {
			t.Fatal("native review envelope differs from expected explicit fields")
		}
		receipt, e := r.SubmissionReceipt(raw)
		must(e)
		parsed, e := parseSubmissionEnvelope(raw)
		must(e)
		target, e := parseManifestBytes(load("target-result.json"), "result", project)
		must(e)
		if receipt.Role != "review" || receipt.BindingRevision != 1 || receipt.EnvelopeDigest != sha256Hex(raw) || *receipt.TargetResultDigest != draft.TargetResultDigest || !reflect.DeepEqual(parsed.BriefManifest.ReviewBrief.ChangedFiles, target.Result.ChangedFiles) || parsed.BriefManifest.ReviewBrief.Diff != target.Result.Diff || parsed.Request.Model.Variant != draft.Variant || parsed.Request.MemoryBytes != *draft.MemoryBytes {
			t.Fatal("exact review receipt/profile/target contents mismatch")
		}
		if len(parsed.ReferencedManifests) != 2 {
			t.Fatal("full target and selected reference missing")
		}
		for _, ref := range parsed.ReferencedManifests {
			stored, e := r.Manifest(project, ref.Digest)
			must(e)
			if !bytes.Equal(stored, ref.Canonical) {
				t.Fatal("stored reference bytes reencoded")
			}
		}
		var queued, intents, completions int
		must(r.store.db.QueryRow("SELECT count(*) FROM jobs WHERE state='queued' AND validation='not_run'").Scan(&queued))
		must(r.store.db.QueryRow("SELECT count(*) FROM agent_operations").Scan(&intents))
		must(r.store.db.QueryRow("SELECT count(*) FROM completion_notifications").Scan(&completions))
		if queued != 2 || intents != 0 || completions != 0 {
			t.Fatal("unexpected execution/completion")
		}
		t.Log("PASS native guided implementation + review exact canonical/profile/reference/target linkage; two queued/not_run, zero execution intents")
	}
	messages, e := getMessages(context.Background(), repo, coordinator.Data.ID)
	must(e)
	if len(messages) != 0 {
		t.Fatal("coordinator received input")
	}
	b, e := apiCall(repo, "get", "/api/session?location[directory]="+url.QueryEscape(repo), nil)
	must(e)
	var sessions []sessionInfo
	must(decodeData(b, &sessions))
	if len(sessions) != 1 || sessions[0].ID != coordinator.Data.ID {
		t.Fatal("unexpected runtime session created")
	}
}
