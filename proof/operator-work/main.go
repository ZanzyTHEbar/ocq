// Admission-only acceptance against real CLI/SQLite and an explicitly supplied
// existing coordinating session. It NEVER dispatches or creates an OpenCode job.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"ocq/internal/core"
)

func main() {
	bin := flag.String("ocq", "", "built CLI")
	state := flag.String("state-dir", "", "fresh private queue path (must not exist)")
	repo := flag.String("repo", "", "canonical existing coordinating checkout")
	session := flag.String("session", "", "exact existing session at --repo; read-only API discovery")
	profile := flag.String("profile-file", "", "explicit execution profile; queued but NEVER executed")
	flag.Parse()
	if *bin == "" || *state == "" || *repo == "" || *session == "" || *profile == "" {
		panic("all flags required")
	}
	if _, err := os.Lstat(*state); !os.IsNotExist(err) {
		panic("fresh queue path required")
	}
	evidence := *state + "-evidence"
	must(os.Mkdir(evidence, 0700))
	record := func(name string, v any) {
		b, err := json.MarshalIndent(v, "", "  ")
		must(err)
		must(os.WriteFile(filepath.Join(evidence, name), b, 0600))
	}
	counter := 0
	run := func(program string, args ...string) ([]byte, error) {
		counter++
		record(fmt.Sprintf("%03d-intent.json", counter), map[string]any{"program": program, "argv": args, "cwd": *repo, "at": time.Now().UTC(), "ownership": "foreground acceptance child"})
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, program, args...)
		cmd.Dir = *repo
		b, err := cmd.CombinedOutput()
		result := map[string]any{"output": string(b), "launch_failed": cmd.ProcessState == nil}
		if cmd.ProcessState != nil {
			result["pid"] = cmd.ProcessState.Pid()
			result["terminal_reaped"] = true
			result["exit"] = cmd.ProcessState.ExitCode()
		}
		record(fmt.Sprintf("%03d-result.json", counter), result)
		return b, err
	}
	cli := func(args ...string) []byte {
		b, err := run(*bin, append([]string{"--state-dir", *state}, args...)...)
		if err != nil {
			panic(string(b) + err.Error())
		}
		return b
	}
	var existing struct {
		Data struct {
			ID        string `json:"id"`
			ProjectID string `json:"projectID"`
			Location  struct {
				Directory string `json:"directory"`
			} `json:"location"`
		} `json:"data"`
	}
	raw, err := run("opencode", "api", "get", "/api/session/"+*session)
	must(err)
	must(json.Unmarshal(raw, &existing))
	check(existing.Data.ID == *session && existing.Data.Location.Directory == *repo, "exact existing coordinating session location")
	base, err := run("git", "rev-parse", "HEAD")
	must(err)
	cli("list")
	project := "proj-operator-acceptance"
	zero := 0
	handoff, _ := json.Marshal(map[string]any{"version": 1, "kind": "handoff", "project_id": project, "source": "operator_import", "observed_at": nil, "intent": "Admission-only operator workflow acceptance; no execution.", "decisions": []string{}, "risks": []string{}})
	reg := core.ProjectRegistrationRequest{Version: 1, Key: "register", ProjectID: project, Label: "Operator acceptance", ExpectedRevision: &zero, Repo: *repo, CommitAnchor: strings.TrimSpace(string(base)), ElephantSessionID: *session, ExpectedOpenCodeProjectID: existing.Data.ProjectID, ExpectedOpenCodeDirectory: *repo, AuthRef: "explicit-local-acceptance", HandoffManifest: handoff}
	record("registration.json", reg)
	cli("project", "register", "--file", filepath.Join(evidence, "registration.json"))
	brief := filepath.Join(evidence, "brief.txt")
	must(os.WriteFile(brief, []byte("Read README.md and report its purpose without changing files."), 0600))
	before, err := core.ObserveHead(context.Background(), *state)
	must(err)
	list, err := core.ListProjectWork(*state, project)
	must(err)
	waitDone := make(chan core.ObservationWaitOutput, 1)
	waitError := make(chan error, 1)
	go func() {
		out, err := core.ObserveWait(context.Background(), *state, core.ObservationWaitRequest{Protocol: core.ObservationProtocol, QueueIdentity: before.QueueIdentity, StoreContext: before.StoreContext, Revision: before.Revision, WaitSeconds: 5, Work: &list.Journal.WorkObservationCursor})
		if err != nil {
			waitError <- err
		} else {
			waitDone <- out
		}
	}()
	prepare := []string{"project", "work", "prepare", "--request-id", "implementation-one", "--binding-revision", "1", "--profile-file", *profile, "--brief-file", brief, project}
	var prepared core.WorkStatus
	must(json.Unmarshal(cli(prepare...), &prepared))
	check(prepared.State == "prepared", "prepared without admission")
	select {
	case out := <-waitDone:
		record("journal-wake.json", out)
		check(out.WorkChanged && !out.DatabaseChanged && out.Revision == before.Revision, "journal-only wake without SQL revision")
	case err := <-waitError:
		panic(err)
	case <-time.After(7 * time.Second):
		panic("bounded journal lease did not finish")
	}
	var preview core.WorkPreview
	must(json.Unmarshal(cli("project", "work", "preview", project, "implementation-one"), &preview))
	record("preview.json", preview)
	check(strings.Contains(preview.Payload, "Read README.md") && preview.Context.ImageRole == "cli" && preview.Binding.ElephantSessionID == *session, "full frozen request and exact CLI/main-session pins")
	_, err = run(*bin, "--state-dir", *state, "project", "work", "confirm", "--preview-sha256", strings.Repeat("0", 64), project, "implementation-one")
	check(err != nil, "wrong preview must fail")
	foreign := preview.Context
	foreign.ImageRole = "bridge"
	_, err = core.ConfirmProjectWork(*state, project, "implementation-one", preview.PreviewSHA256, foreign)
	check(err != nil, "bridge and CLI image authorization contexts cannot be interchanged")
	var admitted core.WorkStatus
	args := []string{"project", "work", "confirm", "--preview-sha256", preview.PreviewSHA256, project, "implementation-one"}
	must(json.Unmarshal(cli(args...), &admitted))
	check(admitted.State == "recorded_observed" && admitted.Receipt != nil && admitted.Job != nil && admitted.Job.State == "queued" && !admitted.Job.TerminalProven, "one durable admission, not execution")
	var recovered core.WorkStatus
	must(json.Unmarshal(cli(args...), &recovered))
	check(recovered.Receipt.JobID == admitted.Receipt.JobID, "confirmation recovers original receipt")
	must(os.WriteFile(brief, []byte("Different task must conflict with the same request identity."), 0600))
	_, err = run(*bin, append([]string{"--state-dir", *state}, prepare...)...)
	check(err != nil, "same ID different content rejected")
	_, err = run(*bin, "--state-dir", *state, "project", "work", "capture-prepare", "--request-id", "blocked-capture", "--binding-revision", "1", "--implementation", "implementation-one", project)
	check(err != nil, "queued work cannot be captured as terminal")
	cli("cancel", admitted.Job.JobID)
	must(json.Unmarshal(cli("project", "work", "status", project, "implementation-one"), &recovered))
	check(recovered.Receipt.JobID == admitted.Receipt.JobID && recovered.Job.State == "cancelled", "status remains tied to original receipt after cancellation")
	snapshot, err := core.ObserveSnapshot(context.Background(), *state, core.ObservationRequest{Protocol: core.ObservationProtocol, QueueIdentity: before.QueueIdentity, StoreContext: before.StoreContext, Work: &list.Journal.WorkObservationCursor})
	must(err)
	check(snapshot.Work != nil && len(snapshot.Work.Items) == 2, "prepared operations discoverable without ID copying")
	record("snapshot-work.json", snapshot.Work)
	record("outcome.json", map[string]any{"status": "passed", "scope": "real admission and journal observation only; no worker/session/worktree creation or dispatch", "state_dir": *state, "evidence": evidence, "request_id": admitted.RequestID, "job_id": admitted.Job.JobID, "terminal": "all foreground children reaped; queued test job cancelled"})
	fmt.Println("PASS: real operator admission, exact receipt recovery, terminal capture gate, journal-only observation wake; no worker execution. Evidence:", evidence)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func check(ok bool, message string) {
	if !ok {
		panic(message)
	}
}
