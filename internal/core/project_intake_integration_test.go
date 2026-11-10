package core_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZanzyTHEbar/faults-go"
	_ "modernc.org/sqlite"

	"ocq/internal/core"
)

// Exercise existing-store intake and exact receipt recovery against real SQLite
// without a dispatcher, agent, OpenCode service or mocked live verification.
func TestExistingProjectIntakeReceipts(t *testing.T) {
	if os.Getenv("OCQ_INTAKE_CRASH_CHILD") == "1" {
		_, _ = core.DispatchLocalProjectIntake(os.Getenv("OCQ_INTAKE_STATE"), os.Getenv("OCQ_INTAKE_ID"))
		os.Exit(0)
	}
	if os.Getenv("OCQ_INTAKE_DELAYED_CHILD") == "1" {
		_, _ = fmt.Fprintln(os.Stdout, "ready")
		var release [1]byte
		if _, err := io.ReadFull(os.Stdin, release[:]); err != nil {
			os.Exit(2)
		}
		if _, err := core.DispatchLocalProjectIntake(os.Getenv("OCQ_INTAKE_STATE"), os.Getenv("OCQ_INTAKE_ID")); err == nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(filepath.Join(home, ".local", "state"), "ocq-intake-acceptance-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	missing := filepath.Join(root, "missing")
	if intake, err := core.OpenExistingProjectIntake(missing); err == nil {
		_ = intake.Close()
		t.Fatal("missing queue accepted")
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing queue created: %v", err)
	}
	state := filepath.Join(root, "queue")
	queue, err := core.OpenQueue(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(state, "queue.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	hash := func(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
	marshal := func(v any) []byte {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	base := strings.Repeat("a", 40)
	repo := filepath.Join(root, "absent-repo")
	handoff := []byte(`{"version":1,"kind":"handoff","project_id":"proj-fixture","source":"operator_import","observed_at":null,"intent":"fixture","decisions":[],"risks":[]}`)
	revision := 0
	registration := core.ProjectRegistrationRequest{Version: 1, Key: "reg-fixture", ProjectID: "proj-fixture", Label: "Fixture", ExpectedRevision: &revision, Repo: repo, CommitAnchor: base, ElephantSessionID: "ses_fixture", ExpectedOpenCodeProjectID: "project-fixture", ExpectedOpenCodeDirectory: repo, AuthRef: "fixture-approval", HandoffManifestDigest: hash(handoff), HandoffManifest: handoff}
	regBytes := marshal(registration)
	request := []byte(fmt.Sprintf(`{"version":2,"kind":"agent","repo":%q,"base":%q,"prompt":"fixture","agent":"build","model":{"id":"fixture","providerID":"fixture"},"permissions":[{"action":"*","resource":"*","effect":"deny"}],"provision_policy":"workmux-headless-no-hooks-no-file-ops-v1","memory_bytes":0}`, repo, base))
	brief := []byte(fmt.Sprintf(`{"version":1,"kind":"brief","project_id":"proj-fixture","source":"operator_import","observed_at":null,"goal":"fixture","scope":["file"],"exclusions":[],"acceptance_criteria":["fixture"],"base":%q,"agent":"build","model":{"id":"fixture","providerID":"fixture"},"permissions":[{"action":"*","resource":"*","effect":"deny"}],"allowed_actions":["read"],"context_references":[]}`, base))
	envelope := marshal(map[string]any{"version": 1, "key": "sub-fixture", "project_id": "proj-fixture", "binding_revision": 1, "role": "implementation", "request": string(request), "brief_digest": hash(brief), "brief_manifest": json.RawMessage(brief), "target_job_id": nil, "target_result_digest": nil})

	// Seed retained, immutable receipts representing an earlier accepted request.
	// The missing repository proves recovery does not rerun live checks.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO projects(id,label,created_at) VALUES('proj-fixture','Fixture','time')`, nil},
		{`INSERT INTO project_manifests(digest,project_id,version,kind,canonical,recorded_at) VALUES(?,'proj-fixture',1,'handoff',?,'time')`, []any{hash(handoff), handoff}},
		{`INSERT INTO project_manifests(digest,project_id,version,kind,canonical,recorded_at) VALUES(?,'proj-fixture',1,'brief',?,'time')`, []any{hash(brief), brief}},
		{`INSERT INTO project_bindings(project_id,revision,repo_root,git_common_dir,commit_anchor,elephant_session_id,opencode_project_id,opencode_directory,handoff_manifest_digest,auth_ref,verified_at,recorded_at) VALUES('proj-fixture',1,?,? ,?,'ses_fixture','project-fixture',?,?,'fixture-approval','time','time')`, []any{repo, repo, base, repo, hash(handoff)}},
		{`INSERT INTO project_registrations(key,digest,canonical,project_id,revision,recorded_at) VALUES('reg-fixture',?,?,'proj-fixture',1,'time')`, []any{hash(regBytes), regBytes}},
		{`INSERT INTO receipts(key,job_id,canonical,digest,accepted_at) VALUES('sub-fixture','job-fixture',?,?,'time')`, []any{request, hash(request)}},
		{`INSERT INTO jobs(id,state,reason) VALUES('job-fixture','unknown','ownership uncertain')`, nil},
		{`INSERT INTO agent_ownership(job_id,session_id,prompt_id,token) VALUES('job-fixture','ses_worker','msg_worker','owner_fixture')`, nil},
		{`INSERT INTO project_submissions(key,envelope_digest,envelope_canonical,job_id,recorded_at) VALUES('sub-fixture',?,?,'job-fixture','time')`, []any{hash(envelope), envelope}},
		{`INSERT INTO project_jobs(job_id,project_id,binding_revision,role,brief_digest,recorded_at) VALUES('job-fixture','proj-fixture',1,'implementation',?,'time')`, []any{hash(brief)}},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	intake, err := core.OpenExistingProjectIntake(state)
	if err != nil {
		t.Fatal(err)
	}
	defer intake.Close()
	reader, err := core.OpenReader(state)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	regReceipt, err := reader.RegistrationReceipt(regBytes)
	if err != nil || regReceipt.ProjectID != "proj-fixture" || regReceipt.Revision != 1 {
		t.Fatalf("registration receipt: %+v %v", regReceipt, err)
	}
	subReceipt, err := reader.SubmissionReceipt(envelope)
	if err != nil || subReceipt.JobID != "job-fixture" || subReceipt.EnvelopeDigest != hash(envelope) {
		t.Fatalf("submission receipt: %+v %v", subReceipt, err)
	}
	regAgain, err := intake.RegisterProject(regBytes)
	if err != nil || regAgain.Digest != regReceipt.Digest || regAgain.Revision != 1 {
		t.Fatalf("receipt-first registration: %+v %v", regAgain, err)
	}
	subAgain, err := intake.SubmitProject(envelope)
	if err != nil || subAgain.JobID != subReceipt.JobID || subAgain.State != "unknown" {
		t.Fatalf("receipt-first submission: %+v %v", subAgain, err)
	}
	if _, err := reader.RegistrationReceipt(append(append([]byte(nil), regBytes...), ' ')); !faults.IsCode(err, faults.CodeValidationFailed) {
		t.Fatalf("registration key conflict not rejected: %v", err)
	}
	if _, err := reader.SubmissionReceipt(append(append([]byte(nil), envelope...), ' ')); !faults.IsCode(err, faults.CodeValidationFailed) {
		t.Fatalf("submission key conflict not rejected: %v", err)
	}
	registration.Key = "reg-new"
	newRegistration := marshal(registration)
	if _, err := reader.RegistrationReceipt(newRegistration); !faults.IsCode(err, faults.CodeNotFound) {
		t.Fatalf("missing registration receipt: %v", err)
	}
	if _, err := intake.RegisterProject(newRegistration); err == nil || !strings.Contains(err.Error(), "repository path is not an existing directory") {
		t.Fatalf("new registration did not reach live repository verification: %v", err)
	}
	newEnvelope := []byte(strings.Replace(string(envelope), `"sub-fixture"`, `"sub-new"`, 1))
	if _, err := reader.SubmissionReceipt(newEnvelope); !faults.IsCode(err, faults.CodeNotFound) {
		t.Fatalf("missing submission receipt: %v", err)
	}
	if _, err := intake.SubmitProject(newEnvelope); err == nil || !strings.Contains(err.Error(), "repository path is not an existing directory") {
		t.Fatalf("new submission did not reach live repository verification: %v", err)
	}

	// A separate process is delayed immediately before dispatch. A fresh caller
	// revokes and retires its claim, then prepares a replacement before release.
	prepared, err := core.PrepareLocalProjectIntake(state, "registration", regBytes)
	if err != nil || prepared.State != "prepared" {
		t.Fatalf("prepare: %+v %v", prepared, err)
	}
	preview, err := core.ReadLocalProjectIntake(state, prepared.ID)
	if err != nil || preview.Kind != "registration" || preview.Key != "reg-fixture" || !bytes.Equal(preview.Request, regBytes) {
		t.Fatalf("preview: %v", err)
	}
	if _, err := core.DispatchLocalProjectIntake(state, prepared.ID); err == nil {
		t.Fatal("unconfirmed request dispatched")
	}
	if armed, err := core.ArmLocalProjectIntake(state, prepared.ID); err != nil || armed.State != "armed" {
		t.Fatalf("arm: %+v %v", armed, err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestExistingProjectIntakeReceipts$")
	child.Env = append(os.Environ(), "OCQ_INTAKE_DELAYED_CHILD=1", "OCQ_INTAKE_STATE="+state, "OCQ_INTAKE_ID="+prepared.ID)
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	}()
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("delayed child: %q %v", line, err)
	}
	if status, err := core.LocalProjectIntakeStatus(state); err != nil || status.State != "armed" || status.ID != prepared.ID {
		t.Fatalf("recover claim: %+v %v", status, err)
	}
	if cancelled, err := core.CancelLocalProjectIntake(state, prepared.ID); err != nil || cancelled.State != "cancelled" {
		t.Fatalf("revoke: %+v %v", cancelled, err)
	}
	if err := core.RetireLocalProjectIntake(state, prepared.ID); err != nil {
		t.Fatal(err)
	}
	replacement, err := core.PrepareLocalProjectIntake(state, "submission", envelope)
	if err != nil || replacement.ID == prepared.ID {
		t.Fatalf("replacement: %+v %v", replacement, err)
	}
	if _, err := stdin.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	err = child.Wait()
	waited = true
	if err != nil {
		t.Fatalf("delayed dispatch was not refused: %v", err)
	}
	if status, err := core.LocalProjectIntakeStatus(state); err != nil || status.State != "prepared" || status.ID != replacement.ID {
		t.Fatalf("replacement affected: %+v %v", status, err)
	}
	if _, err := core.ArmLocalProjectIntake(state, replacement.ID); err != nil {
		t.Fatal(err)
	}
	if done, err := core.DispatchLocalProjectIntake(state, replacement.ID); err != nil || done.State != "completed" || done.Observation != "matched" {
		t.Fatalf("existing receipt dispatch: %+v %v", done, err)
	}
	if err := core.RetireLocalProjectIntake(state, replacement.ID); err != nil {
		t.Fatal(err)
	}
	uncertain, err := core.PrepareLocalProjectIntake(state, "registration", newRegistration)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.ArmLocalProjectIntake(state, uncertain.ID); err != nil {
		t.Fatal(err)
	}
	// Hold the real SQLite writer lock while a separate process creates its
	// durable attempt. Kill that process before it can obtain a write transaction.
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	crash := exec.Command(os.Args[0], "-test.run=^TestExistingProjectIntakeReceipts$")
	crash.Env = append(os.Environ(), "OCQ_INTAKE_CRASH_CHILD=1", "OCQ_INTAKE_STATE="+state, "OCQ_INTAKE_ID="+uncertain.ID)
	if err := crash.Start(); err != nil {
		t.Fatal(err)
	}
	crashWaited := false
	defer func() {
		if !crashWaited {
			_ = crash.Process.Kill()
			_ = crash.Wait()
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, readErr := os.ReadFile(filepath.Join(state, "herdr-project-intake", "attempt.json"))
		if readErr == nil && json.Valid(b) {
			break
		}
		if (readErr != nil && !os.IsNotExist(readErr)) || time.Now().After(deadline) {
			t.Fatalf("attempt marker not observed: %v", readErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if err := crash.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = crash.Wait()
	crashWaited = true
	if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if _, err := core.CancelLocalProjectIntake(state, uncertain.ID); err == nil {
		t.Fatal("attempted intake cancelled")
	}
	if err := core.RetireLocalProjectIntake(state, uncertain.ID); err == nil {
		t.Fatal("attempted intake retired")
	}
	if _, err := core.DispatchLocalProjectIntake(state, uncertain.ID); err == nil {
		t.Fatal("attempted intake retried")
	}
	if observed, err := core.ReconcileLocalProjectIntake(state, uncertain.ID); err != nil || observed.State != "attempted" || observed.Observation != "not_observed" {
		t.Fatalf("missing receipt uncertainty: %+v %v", observed, err)
	}
	if _, err := db.Exec("PRAGMA user_version=4"); err != nil {
		t.Fatal(err)
	}
	if _, err := intake.RegisterProject(regBytes); !faults.IsCode(err, faults.CodeConfigInvalid) {
		t.Fatalf("registration accepted changed schema: %v", err)
	}
	if _, err := intake.SubmitProject(envelope); !faults.IsCode(err, faults.CodeConfigInvalid) {
		t.Fatalf("submission accepted changed schema: %v", err)
	}
	if _, err := reader.RegistrationReceipt(regBytes); !faults.IsCode(err, faults.CodeConfigInvalid) {
		t.Fatalf("registration read accepted changed schema: %v", err)
	}
	if _, err := reader.SubmissionReceipt(envelope); !faults.IsCode(err, faults.CodeConfigInvalid) {
		t.Fatalf("submission read accepted changed schema: %v", err)
	}
	if other, err := core.OpenExistingProjectIntake(state); err == nil {
		_ = other.Close()
		t.Fatal("old schema opened")
	}
	var version, registrations, submissions int
	var jobState, reason string
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 4 {
		t.Fatalf("schema was migrated: %d %v", version, err)
	}
	if err := db.QueryRow("SELECT count(*) FROM project_registrations").Scan(&registrations); err != nil || registrations != 1 {
		t.Fatalf("registration mutated: %d %v", registrations, err)
	}
	if err := db.QueryRow("SELECT count(*) FROM project_submissions").Scan(&submissions); err != nil || submissions != 1 {
		t.Fatalf("submission mutated: %d %v", submissions, err)
	}
	if err := db.QueryRow("SELECT state,reason FROM jobs WHERE id='job-fixture'").Scan(&jobState, &reason); err != nil || jobState != "unknown" || reason != "ownership uncertain" {
		t.Fatalf("ownership changed: %s %s %v", jobState, reason, err)
	}
}
