package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// One opt-in acceptance scenario around the real native form/private bridge.
// No fake API, dispatcher, prompt, or replacement registration implementation.
func TestRegistrationFormLiveFixture(t *testing.T) {
	root := os.Getenv("OCQ_REGISTRATION_FIXTURE_ROOT")
	if root == "" {
		t.Skip("requires explicitly owned native/live fixture")
	}
	info, err := os.Stat(root)
	if err != nil || !filepath.IsAbs(root) || info.Mode() != os.ModeDir|0700 {
		t.Fatal("private absolute fixture root required")
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	load := func(name string) []byte {
		t.Helper()
		b, e := os.ReadFile(filepath.Join(root, name))
		must(e)
		return b
	}
	write := func(name string, b []byte) { t.Helper(); must(os.WriteFile(filepath.Join(root, name), b, 0600)) }
	var session struct {
		Data sessionInfo `json:"data"`
	}
	must(json.Unmarshal(load("coordinator-get.json"), &session))
	repo, state := filepath.Join(root, "repo"), filepath.Join(root, "queue")
	if session.Data.Location.Directory != repo || !isValidSessionID(session.Data.ID) {
		t.Fatal("exact supplied coordinator location required")
	}
	if os.Getenv("OCQ_REGISTRATION_FIXTURE_PHASE") == "prepare" {
		if _, err := os.Lstat(state); !os.IsNotExist(err) {
			t.Fatal("fresh fixture queue required; do not retry uncertain prepare")
		}
		base, err := gitRead(repo, "rev-parse", "HEAD")
		must(err)
		zero := 0
		d := ProjectRegistrationDraft{Mode: "new", Key: "native-guided-registration", ProjectID: "proj-native-guided", Label: "Projet café 東京", ExpectedRevision: &zero, Repo: repo, CommitAnchor: base, ElephantSessionID: session.Data.ID, ExpectedOpenCodeProjectID: session.Data.ProjectID, ExpectedOpenCodeDirectory: repo, AuthRef: "owned-fixture-reference-only", Intent: "Créer un projet 東京\nDeuxième ligne\u202e\u200b", Decisions: []string{"Décision α\nGarder les octets exacts"}, Risks: []string{"Risque β\nAucune exécution autorisée"}}
		input, err := json.Marshal(d)
		must(err)
		write("draft-input.json", input)
		raw, err := BuildProjectRegistrationDraft(d)
		must(err)
		write("expected-registration.json", raw)
		_, handoff, _, err := parseRegistrationPayload(raw)
		must(err)
		write("expected-handoff.json", handoff.Canonical)
		q, err := OpenQueue(state)
		must(err)
		must(q.Close())
		t.Log("PASS exact Go-built registration/decoder; fresh empty queue, no registration")
		return
	}
	var draft ProjectRegistrationDraft
	must(json.Unmarshal(load("draft-input.json"), &draft))
	raw, err := BuildProjectRegistrationDraft(draft)
	must(err)
	if !bytes.Equal(raw, load("expected-registration.json")) {
		t.Fatal("generated draft changed")
	}
	status, err := LocalProjectIntakeStatus(state)
	must(err)
	intent, err := ReadLocalProjectIntake(state, status.ID)
	must(err)
	if intent.Kind != "registration" || !bytes.Equal(intent.Request, raw) {
		t.Fatal("native form retained bytes differ from actual builder")
	}
	req, handoff, digest, err := parseRegistrationPayload(intent.Request)
	must(err)
	if !bytes.Equal(handoff.Canonical, load("expected-handoff.json")) || req.HandoffManifestDigest != handoff.Digest {
		t.Fatal("exact handoff/digest changed")
	}
	w, err := OpenExistingProjectIntake(state)
	must(err)
	defer w.Close()
	expected := 0
	if os.Getenv("OCQ_REGISTRATION_FIXTURE_PHASE") == "verify" {
		expected = 1
	}
	for _, table := range []string{"projects", "project_registrations", "project_bindings"} {
		var n int
		must(w.store.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n))
		if n != expected {
			t.Fatalf("%s count %d want %d", table, n, expected)
		}
	}
	for _, table := range []string{"jobs", "agent_operations", "agent_ownership", "completion_notifications"} {
		var n int
		must(w.store.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n))
		if n != 0 {
			t.Fatal("unexpected execution or completion")
		}
	}
	if expected == 1 {
		receipt, found, err := w.store.lookupProjectRegistration(w.store.db, req.Key, raw, digest)
		must(err)
		if !found || receipt.ProjectID != draft.ProjectID || receipt.BindingRevision != 1 || receipt.HandoffManifestDigest != handoff.Digest || receipt.ElephantSessionID != session.Data.ID {
			t.Fatal("exact registration receipt mismatch")
		}
		messages, err := getMessages(context.Background(), repo, session.Data.ID)
		must(err)
		if len(messages) != 0 {
			t.Fatal("coordinator received input")
		}
		b, err := apiCall(repo, "get", "/api/session?location[directory]="+url.QueryEscape(repo), nil)
		must(err)
		var sessions []sessionInfo
		must(decodeData(b, &sessions))
		if len(sessions) != 1 || sessions[0].ID != session.Data.ID {
			t.Fatal("unexpected runtime session")
		}
	}
	t.Logf("PASS exact Unicode/multiline native retained envelope/handoff; registrations=%d, zero execution", expected)
}
