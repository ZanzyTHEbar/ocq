package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Explicitly opted-in acceptance fixture, not a fake OpenCode API. The caller
// owns a disposable clone, private HOME/XDG, exact empty coordinator and an
// authenticated loopback 2.2.6 server. No dispatcher or prompt ever runs.
// prepare leaves the known unsupported wrapper retained for native raw-only
// recovery; the native scenario cancels/retires it, then accepts the plain review.
// verify checks that exact acceptance, all references and zero execution intents.
func TestReviewIntakeLiveFixture(t *testing.T) {
	root := os.Getenv("OCQ_REVIEW_FIXTURE_ROOT")
	if root == "" {
		t.Skip("requires an explicitly owned real OpenCode fixture")
	}
	if !filepath.IsAbs(root) {
		t.Fatal("absolute private fixture root required")
	}
	info, err := os.Stat(root)
	if err != nil || info.Mode() != os.ModeDir|0700 {
		t.Fatal("private fixture root required")
	}
	repo, state := filepath.Join(root, "repo"), filepath.Join(root, "queue")
	load := func(name string) []byte {
		t.Helper()
		b, e := os.ReadFile(filepath.Join(root, name))
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	encode := func(v any) []byte {
		t.Helper()
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	write := func(name string, b []byte) {
		t.Helper()
		if e := os.WriteFile(filepath.Join(root, name), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	// The retirement regression reuses an explicitly copied, preserved real
	// fixture offline. Build B with the product prompt builder and a real second
	// result association, without submitting/dispatching another job or API call.
	if os.Getenv("OCQ_REVIEW_FIXTURE_PHASE") == "retire-prepare" {
		original := load("submission.json")
		a, e := parseSubmissionEnvelope(original)
		must(e)
		w, e := OpenExistingProjectIntake(state)
		must(e)
		defer w.Close()
		var target []byte
		must(w.store.db.QueryRow("SELECT canonical FROM project_manifests WHERE digest=?", *a.Envelope.TargetResultDigest).Scan(&target))
		m, e := parseManifestBytes(target, "result", a.Envelope.ProjectID)
		must(e)
		var previous string
		must(w.store.db.QueryRow("SELECT id FROM project_result_associations WHERE project_id=? AND job_id=? AND manifest_digest=?", a.Envelope.ProjectID, *a.Envelope.TargetJobID, m.Digest).Scan(&previous))
		m.Result.InlineOutput = "PRIVATE_RETIRE_ALTERNATE_TARGET"
		association, e := (&Queue{store: w.store}).RecordResult(a.Envelope.ProjectID, *a.Envelope.TargetJobID, encode(m.Result), previous)
		must(e)
		rb := *a.BriefManifest.ReviewBrief
		rb.TargetResultDigest = association.ManifestDigest
		request := a.Request
		request.Prompt, e = buildReviewPrompt(rb)
		must(e)
		envelope := a.Envelope
		envelope.Key, envelope.LaneID = "review-retire-B", "lane-retire-B"
		envelope.TargetResultDigest = &association.ManifestDigest
		envelope.Request = encode(string(encode(request)))
		envelope.BriefManifest = encode(rb)
		envelope.BriefDigest, envelope.BriefManifestDigest = sha256Hex(envelope.BriefManifest), ""
		envelope.ReferencedManifests = nil
		b := encode(envelope)
		parsed, e := parseSubmissionEnvelope(b)
		must(e)
		must(verifyReviewTarget(context.Background(), w.store.db, parsed))
		write("submission-B.json", b)
		status, e := PrepareLocalProjectIntake(state, "submission", original)
		must(e)
		write("retire-fixture.json", encode(map[string]any{"project": a.Envelope.ProjectID, "target_job": a.Envelope.TargetJobID, "target_A": a.Envelope.TargetResultDigest, "target_B": association.ManifestDigest, "association_A": previous, "association_B": association.ID, "lane_A": a.LaneID, "lane_B": parsed.LaneID, "intent_A": status.ID, "result": "PASS"}))
		t.Log("PASS real alternate target/core-built B; A prepared only; no API or execution")
		return
	}
	var coordinator struct {
		Data sessionInfo `json:"data"`
	}
	must(json.Unmarshal(load("coordinator-get.json"), &coordinator))
	if coordinator.Data.ID != "ses_ocq_review_"+strings.TrimPrefix(filepath.Base(root), "ocq-review-submit-") || coordinator.Data.Location.Directory != repo {
		t.Fatal("exact owned coordinator mismatch")
	}
	const project = "proj-review-intake-fixture"
	if os.Getenv("OCQ_REVIEW_FIXTURE_PHASE") == "prepare" {
		if _, e := os.Lstat(state); !os.IsNotExist(e) {
			t.Fatal("prepare requires a fresh queue; never retry an ambiguous fixture")
		}
		base, e := gitRead(repo, "rev-parse", "HEAD")
		must(e)
		q, e := OpenQueue(state)
		must(e)
		defer q.Close()
		handoff := encode(map[string]any{"version": 1, "kind": "handoff", "project_id": project, "source": "operator_import", "observed_at": nil, "intent": "PRIVATE_REVIEW_FIXTURE", "decisions": []string{}, "risks": []string{}})
		zero := 0
		registration := ProjectRegistrationRequest{Version: 1, Key: "register-review-fixture", ProjectID: project, Label: "Review intake fixture", ExpectedRevision: &zero, Repo: repo, CommitAnchor: base, ElephantSessionID: coordinator.Data.ID, ExpectedOpenCodeProjectID: coordinator.Data.ProjectID, ExpectedOpenCodeDirectory: repo, AuthRef: "disposable-fixture-only", HandoffManifest: handoff}
		_, e = q.RegisterProject(encode(registration))
		must(e)
		deny := []permissionRule{{Action: "*", Resource: "*", Effect: "deny"}}
		model := agentModel{ID: "never-run", ProviderID: "fixture"}
		implRequest := agentRequest{Version: 2, Kind: "agent", Repo: repo, Base: base, Prompt: "PRIVATE_NEVER_SENT_IMPLEMENTATION", Agent: "build", Model: model, Permissions: deny, ProvisionPolicy: provisionPolicy}
		brief := briefManifest{Version: 1, Kind: "brief", ProjectID: project, Source: "operator_import", Base: base, Goal: "Unexecuted implementation fixture", Scope: []string{"review.txt"}, Exclusions: []string{}, AcceptanceCriteria: []string{"queued only"}, Agent: "build", Model: model, Permissions: deny, AllowedActions: []string{"read"}, ContextReferences: []string{}}
		implEnvelope := encode(projectSubmissionEnvelope{Version: 1, Key: "implementation-review-fixture", ProjectID: project, BindingRevision: 1, Role: "implementation", Request: encode(string(encode(implRequest))), BriefManifest: encode(brief)})
		impl, e := q.SubmitProject(implEnvelope)
		must(e)
		contents := "PRIVATE_REVIEW_CONTENT opérateur\u202e\u200b\n\x01"
		result := ResultManifest{Version: 1, Kind: "result", ProjectID: project, Source: "operator_import", JobID: impl.JobID, RequestDigest: impl.Digest, Base: base, Worktree: repo, StagedStatus: "clean", UnstagedStatus: "clean", UntrackedPaths: []string{"review.txt"}, BeforeStatusDigest: "not_recorded", AfterStatusDigest: "not_recorded", BeforeContentDigest: "not_recorded", AfterContentDigest: "not_recorded", IgnoredExclusions: []string{}, ChangedFiles: []ManifestChangedFile{{Path: "review.txt", OldMode: "000000", NewMode: "100644", Contents: &contents}}, Diff: "diff --git a/review.txt b/review.txt\nnew file mode 100644\n--- /dev/null\n+++ b/review.txt\n@@ -0,0 +1 @@\n+PRIVATE_REVIEW_CONTENT\n", ClaimedValidation: ClaimedValidation{Status: "not_run", Commands: []string{}, Results: "not_run", Provenance: "not_recorded", Limitations: "Imported claims only; no tests or integration"}}
		resultRaw := encode(result)
		association, e := q.RecordResult(project, impl.JobID, resultRaw, "")
		must(e)
		rb := reviewBriefManifest{Version: 1, Kind: "review_brief", ProjectID: project, Source: "operator_import", TargetJobID: impl.JobID, TargetResultDigest: association.ManifestDigest, Base: base, Criteria: "PRIVATE_CRITERIA opérateur\u202e\u200b\n\x01", Rules: "Static data review only; do not execute or edit", Diff: result.Diff, ChangedFiles: result.ChangedFiles}
		prompt, e := buildReviewPrompt(rb)
		must(e) // the actual product builder
		rules := []permissionRule{{Action: "*", Resource: "*", Effect: "deny"}, {Action: "read", Resource: "*", Effect: "allow"}, {Action: "glob", Resource: "*", Effect: "allow"}, {Action: "grep", Resource: "*", Effect: "allow"}}
		reviewer := agentRequest{Version: 2, Kind: "agent", Repo: repo, Base: base, Prompt: prompt, Agent: "build", Model: model, Permissions: rules, ProvisionPolicy: provisionPolicy}
		briefRaw, e := json.MarshalIndent(rb, "", "  ")
		must(e)
		// Include a literal formatted object and an exact JSON string. The
		// private core projection must not compact or re-encode either one.
		refHandoff := []byte("{\n  \"version\": 1, \"kind\": \"handoff\", \"project_id\": \"" + project + "\", \"source\": \"author_import\", \"observed_at\": null, \"intent\": \"PRIVATE_REFERENCE\", \"decisions\": [], \"risks\": [], \"notes\": \"visible\\u202e\\u200b\\n\\u0001\"\n}")
		type reference struct{ raw, embedded []byte }
		refs := []reference{{refHandoff, refHandoff}, {resultRaw, encode(string(resultRaw))}}
		sort.Slice(refs, func(i, j int) bool { return sha256Hex(refs[i].raw) < sha256Hex(refs[j].raw) })
		requestRaw := encode(reviewer)
		makeEnvelope := func(request []byte) []byte {
			b := []byte(`{"version":1,"key":"review-intake-fixture","project_id":"` + project + `","binding_revision":1,"role":"review","lane_id":"lane-native-review","fsfork_driver":"git_worktree","request":`)
			b = append(b, request...)
			b = append(b, []byte(`,"brief_manifest":`)...)
			b = append(b, briefRaw...)
			b = append(b, []byte(`,"brief_digest":"`+sha256Hex(briefRaw)+`","target_job_id":"`+impl.JobID+`","target_result_digest":"`+association.ManifestDigest+`","referenced_manifests":[`)...)
			for i, r := range refs {
				if i > 0 {
					b = append(b, ',')
				}
				b = append(b, r.embedded...)
			}
			return append(b, ']', '}')
		}
		envelope := makeEnvelope(encode(string(requestRaw)))
		parsed, e := parseSubmissionEnvelope(envelope)
		must(e)
		unsafeReviewer := reviewer
		unsafeReviewer.MemoryBytes = 1 << 53
		unsafeEnvelope := makeEnvelope(encode(string(encode(unsafeReviewer))))
		_, e = parseSubmissionEnvelope(unsafeEnvelope)
		must(e) // valid for Go; deliberately unsafe for JavaScript review
		write("unsafe-number.json", unsafeEnvelope)
		// Real fresh intake gate, including exact 2.2.6 agent inspection. Stop
		// here on config errors; do not swap agents or forge API responses.
		must(q.store.verifyProjectSubmissionLive(context.Background(), parsed))
		write("submission.json", envelope)
		write("expected-request.json", requestRaw)
		write("expected-brief.json", briefRaw)
		for i, r := range refs {
			write("reference-"+string(rune('1'+i))+".json", r.raw)
		}
		wrapped := makeEnvelope(encode(map[string]any{"digest": sha256Hex(requestRaw), "canonical": string(requestRaw)}))
		write("unsupported-wrapper.json", wrapped)
		status, e := PrepareLocalProjectIntake(state, "submission", wrapped)
		must(e)
		write("fixture.json", encode(map[string]any{"project": project, "target_job": impl.JobID, "target_digest": association.ManifestDigest, "target_association": association.ID, "source": association.Source, "verification": association.Verification, "unsupported_id": status.ID, "envelope_sha256": sha256Hex(envelope), "reference_count": len(refs)}))
		t.Log("PASS real 2.2.6 fresh review policy gate; exact core-builder envelope ready; no reviewer accepted or prompted")
		return
	}
	if os.Getenv("OCQ_REVIEW_FIXTURE_PHASE") != "verify" {
		t.Fatal("explicit prepare or verify phase required")
	}
	envelope := load("submission.json")
	parsed, err := parseSubmissionEnvelope(envelope)
	must(err)
	unsafe, err := parseSubmissionEnvelope(load("unsafe-number.json"))
	must(err)
	if unsafe.Request.MemoryBytes != 1<<53 {
		t.Fatal("native unsafe-number input was not the intended valid core envelope")
	}
	r, err := OpenReader(state)
	must(err)
	defer r.Close()
	receipt, err := r.SubmissionReceipt(envelope)
	must(err)
	if receipt.Role != "review" || receipt.BindingRevision != 1 || receipt.EnvelopeDigest != sha256Hex(envelope) || receipt.TargetJobID == nil || *receipt.TargetJobID != *parsed.Envelope.TargetJobID || *receipt.TargetResultDigest != *parsed.Envelope.TargetResultDigest {
		t.Fatal("exact review receipt/linkage lost")
	}
	var stored []byte
	must(r.store.db.QueryRow("SELECT envelope_canonical FROM project_submissions WHERE key=?", parsed.Envelope.Key).Scan(&stored))
	if !bytes.Equal(stored, envelope) {
		t.Fatal("envelope bytes changed")
	}
	must(r.store.db.QueryRow("SELECT canonical FROM receipts WHERE job_id=?", receipt.JobID).Scan(&stored))
	if !bytes.Equal(stored, load("expected-request.json")) {
		t.Fatal("canonical agent request changed")
	}
	for i, ref := range parsed.ReferencedManifests {
		must(r.store.db.QueryRow("SELECT canonical FROM project_manifests WHERE digest=?", ref.Digest).Scan(&stored))
		if !bytes.Equal(stored, load("reference-"+string(rune('1'+i))+".json")) {
			t.Fatal("reference bytes changed")
		}
	}
	// Receipt acceptance preallocates owned IDs/tokens, but does not journal
	// execution intents or create runtime sessions. Check the actual boundary.
	counts := map[string]int{"SELECT count(*) FROM jobs WHERE state='queued' AND validation='not_run'": 2, "SELECT count(*) FROM jobs": 2, "SELECT count(*) FROM agent_operations": 0, "SELECT count(*) FROM agent_ownership WHERE phase='queued'": 2, "SELECT count(*) FROM completion_notifications": 0}
	for query, want := range counts {
		var got int
		must(r.store.db.QueryRow(query).Scan(&got))
		if got != want {
			t.Fatalf("execution/receipt invariant: got %d want %d", got, want)
		}
	}
	var lane, owner string
	must(r.store.db.QueryRow("SELECT lane_id,owner_job_id FROM project_lanes WHERE owner_job_id=?", receipt.JobID).Scan(&lane, &owner))
	if lane != "lane-native-review" || owner != receipt.JobID {
		t.Fatal("exact lane missing")
	}
	status, err := LocalProjectIntakeStatus(state)
	must(err)
	if status.State != "completed" {
		t.Fatal("native recovery must explicitly match exact receipt")
	}
	if _, e := DispatchLocalProjectIntake(state, status.ID); e == nil {
		t.Fatal("duplicate dispatch accepted")
	}
	msgs, err := getMessages(context.Background(), repo, coordinator.Data.ID)
	must(err)
	if len(msgs) != 0 {
		t.Fatal("coordinator received a prompt")
	}
	b, err := apiCall(repo, "get", "/api/session?location[directory]="+url.QueryEscape(repo), nil)
	must(err)
	var sessions []sessionInfo
	must(decodeData(b, &sessions))
	if len(sessions) != 1 || sessions[0].ID != coordinator.Data.ID {
		t.Fatal("unexpected runtime session created")
	}
	write("verified.json", encode(map[string]any{"result": "PASS", "job": receipt.JobID, "role": receipt.Role, "envelope_sha256": receipt.EnvelopeDigest, "target_job": receipt.TargetJobID, "target_digest": receipt.TargetResultDigest, "lane": lane, "state": "queued", "validation": "not_run", "prompts": 0, "agent_intents": 0, "references": len(parsed.ReferencedManifests)}))
	t.Log("PASS exact fresh review receipt/lane/references, queued/not_run, zero prompts/agent intents, duplicate dispatch refused")
}
