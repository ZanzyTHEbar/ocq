package core

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the real journal and the boolean that gates the native POST without
// running a server. The native session and final SQL selection are samples.
func TestMainPromptAttemptFence(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	j := &composeJournal{dir: f}
	const id = "send"
	rec := mainRecord{SetupID: "setup", SessionID: "ses_fixture", ProjectID: "fixture", Repo: dir, Metadata: json.RawMessage(`{"owner":"fixture"}`)}
	rec.Digest = sha256Hex([]byte("fixture"))
	rec.Binding = ProjectBinding{Revision: 1, RepoRoot: dir, ElephantSessionID: rec.SessionID}
	rec.Role.SHA256 = sha256Hex([]byte("role"))
	current := map[string]any{"id": rec.SessionID, "projectID": rec.ProjectID, "location": map[string]any{"directory": dir}, "metadata": map[string]any{"owner": "fixture"}, "agent": "original"}
	rec.ProfileSHA = nativeProfile(current)
	selected := mainSelection{setup: rec, binding: rec.Binding}
	selectExact := func() mainSelection { return selected }
	original := MainActionContext{Protocol: MainProtocol, Peer: "local", Space: "cli", Folder: dir, Action: "start", ImageRole: "cli", ImageSHA256: rec.Digest}
	preview := MainPreview{Protocol: MainProtocol, Context: original, Binding: rec.Binding, Role: rec.Role}
	b, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	preview.PreviewSHA256 = sha256Hex(b)
	previewName := mainFile(id, "preview-"+preview.PreviewSHA256)
	if err := j.create(previewName, preview); err != nil {
		t.Fatal(err)
	}
	claim := workClaim{Intent: rec.Digest, Payload: rec.Digest, Preview: preview.PreviewSHA256, Context: WorkActionContext(original)}
	attemptName := mainFile(id, "prompt.attempt")
	requireNoAttempt := func(fresh bool, err error, reason string) {
		t.Helper()
		if fresh || err == nil || !strings.Contains(err.Error(), reason) {
			t.Fatalf("unexpected POST authorization or error: fresh=%t, err=%v", fresh, err)
		}
		if _, err := workRead(j.dir, attemptName); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected attempt marker: %v", err)
		}
	}

	current["agent"] = "changed"
	fresh, err := beginMainPrompt(j, id, rec, claim, current, func() mainSelection {
		t.Fatal("profile drift should stop before final selection")
		return mainSelection{}
	})
	requireNoAttempt(fresh, err, "profile differs")
	current["agent"] = "original"
	fresh, err = beginMainPrompt(j, id, rec, claim, current, func() mainSelection {
		if _, err := workRead(j.dir, mainFile(id, "prompt.armed")); err != nil {
			t.Fatalf("final binding sample must be taken after arming: %v", err)
		}
		drift := selected
		drift.binding.Revision++ // Session, repo, setup and role are unchanged.
		return drift
	})
	requireNoAttempt(fresh, err, "binding, setup or role changed")

	fresh, err = beginMainPrompt(j, id, rec, claim, current, selectExact)
	if !fresh || err != nil {
		t.Fatalf("exact fences should allow one durable attempt: fresh=%t, err=%v", fresh, err)
	}
	before, err := workRead(j.dir, attemptName)
	if err != nil {
		t.Fatal(err)
	}
	foreign := claim
	foreign.Context.Space, foreign.Context.ImageRole = "panel", "bridge"
	fresh, err = beginMainPrompt(j, id, rec, foreign, nil, nil)
	if fresh || err != nil {
		t.Fatalf("foreign context should permit observation only: fresh=%t, err=%v", fresh, err)
	}
	if after, err := workRead(j.dir, attemptName); err != nil || string(after) != string(before) {
		t.Fatalf("recovery changed the attempt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, previewName), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if fresh, err := beginMainPrompt(j, id, rec, foreign, nil, nil); fresh || err == nil {
		t.Fatalf("corrupt original preview should fail closed: fresh=%t, err=%v", fresh, err)
	}
}
