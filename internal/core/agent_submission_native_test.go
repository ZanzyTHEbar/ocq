package core

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// One opt-in actual native/SQLite/Git acceptance scenario. No OpenCode fixture:
// standalone intake has no live API gate. Never start dispatch or an agent.
func TestAgentSubmissionNativeFixture(t *testing.T) {
	root := os.Getenv("OCQ_AGENT_SUBMIT_FIXTURE_ROOT")
	if root == "" {
		t.Skip("requires explicitly owned native fixture")
	}
	info, e := os.Stat(root)
	if e != nil || !filepath.IsAbs(root) || info.Mode() != os.ModeDir|0700 {
		t.Fatal("private absolute fixture required")
	}
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	encode := func(v any) []byte { t.Helper(); b, e := json.Marshal(v); must(e); return b }
	write := func(n string, b []byte) { t.Helper(); must(os.WriteFile(filepath.Join(root, n), b, 0600)) }
	load := func(n string) []byte { t.Helper(); b, e := os.ReadFile(filepath.Join(root, n)); must(e); return b }
	state, repo := filepath.Join(root, "queue"), filepath.Join(root, "repo")
	phase := os.Getenv("OCQ_AGENT_SUBMIT_FIXTURE_PHASE")
	if phase == "prepare" {
		if _, e := os.Lstat(state); !os.IsNotExist(e) {
			t.Fatal("fresh queue required; never retry uncertain fixture setup")
		}
		q, e := OpenQueue(state)
		must(e)
		must(q.Close())
		must(os.Mkdir(repo, 0700))
		for _, a := range [][]string{{"init", "-q", repo}, {"-C", repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "standalone fixture"}} {
			c := exec.Command("git", a...)
			if out, e := c.CombinedOutput(); e != nil {
				t.Fatalf("fixture git: %v %s", e, out)
			}
		}
		base, e := gitRead(repo, "rev-parse", "HEAD")
		must(e)
		must(os.Symlink(repo, filepath.Join(root, "supplied-repo")))
		d := AgentSubmissionDraft{Key: "native-standalone-agent", Repo: filepath.Join(root, "supplied-repo"), Base: base, Prompt: "PRIVATE_AGENT_PROMPT café\u202e\u200b\nExact prompt\x01\nDo not execute in this fixture.", Agent: "fixture-not-executed", ProviderID: "fixture", ModelID: "never-run", Variant: "native-explicit", Memory: "9007199254740993", Permissions: `[{"action":"*","resource":"*","effect":"deny"},{"action":"read","resource":"src/**","effect":"allow"},{"action":"shell","resource":"*","effect":"ask"}]`}
		write("form.json", encode(d))
		r, e := OpenReader(state)
		must(e)
		s, e := r.CommandSubmissionContext("sp-fixture")
		must(e)
		built, e := BuildAgentSubmissionDraft(s, d)
		must(e)
		p, e := parsePreparedAgentSubmission(built)
		must(e)
		var raw agentRequest
		must(json.Unmarshal([]byte(p.Canonical), &raw))
		raw.Model.Variant = ""
		raw.Prompt = "PRIVATE_EXACT_IMPORT café\u200b\nRetain exact canonical bytes; never execute."
		canonical := encode(raw)
		importJSON := []byte("\n{\n  \"request\": " + string(encode(string(canonical))) + ",\n  \"key\": \"native-import-cancelled\"\n}\n")
		write("advanced.json", importJSON)
		write("advanced-canonical.json", canonical)
		_, e = ImportAgentSubmissionDraft(s, importJSON)
		must(e)
		bad := d
		bad.Permissions = `[{"action":"read","action":"shell","resource":"*","effect":"allow"}]`
		if _, e = BuildAgentSubmissionDraft(s, bad); e == nil {
			t.Fatal("duplicate permission fields erased/accepted")
		}
		jobs, e := r.QueueJobs(0, 20)
		must(e)
		if len(jobs.Items) != 0 {
			t.Fatal("draft admitted work")
		}
		must(r.Close())
		t.Log("PASS actual Git/schema5; exact ordered explicit profile, >2^53 memory; duplicate permission fields refused; zero jobs/API/provisioning")
		return
	}
	if phase != "raw" && phase != "admitted" && phase != "verify" {
		t.Fatal("explicit prepare, raw, admitted or verify phase required")
	}
	var origin struct {
		Space string `json:"space"`
	}
	must(json.Unmarshal(load("origin.json"), &origin))
	r, e := OpenReader(state)
	must(e)
	defer r.Close()
	s, e := r.CommandSubmissionContext(origin.Space)
	must(e)
	j, e := agentAdmission.open(state, s)
	must(e)
	status, record := agentAdmission.status(j, s)
	j.close()
	p, e := parsePreparedAgentSubmission(record.Request)
	must(e)
	if phase == "raw" {
		if status.State != "prepared" || p.Source != "exact_json_import" || p.Import != string(load("advanced.json")) || p.Canonical != string(load("advanced-canonical.json")) {
			t.Fatal("native import did not preserve exact input/canonical bytes")
		}
		jobs, e := r.QueueJobs(0, 20)
		must(e)
		if len(jobs.Items) != 0 {
			t.Fatal("import/prepare admitted work")
		}
		if _, e = PrepareCommandSubmission(state, s, record.Request); e == nil {
			t.Fatal("command route accepted agent payload")
		}
		write("native-raw-intent.json", encode(record))
		t.Log("PASS native advanced original JSON and exact canonical v2 retained; no job; command route cannot accept agent payload")
		return
	}
	if status.State != "completed" && status.State != "attempted" {
		t.Fatal("actual native admission required")
	}
	receipt, e := r.AgentSubmissionReceipt(p.Key, []byte(p.Canonical))
	must(e)
	job, e := r.store.get(receipt.Job.ID)
	must(e)
	var d AgentSubmissionDraft
	must(json.Unmarshal(load("form.json"), &d))
	req, e := parseCanonicalAgentRequest(job.Request)
	must(e)
	if job.State != "queued" || job.Validation != "not_run" || job.Outcome != "pending" || job.Stdout != "" || job.Stderr != "" || p.Source != "guided_core_resolved" || !bytes.Equal(job.Request, []byte(p.Canonical)) || job.Digest != sha256Hex([]byte(p.Canonical)) || req.Model != (agentModel{ID: d.ModelID, ProviderID: d.ProviderID, Variant: d.Variant}) || req.Prompt != d.Prompt || req.Base != d.Base || req.MemoryBytes != 9007199254740993 {
		t.Fatal("exact native v2 profile/bytes/prompt/base/memory/state mismatch")
	}
	var rules []permissionRule
	must(json.Unmarshal([]byte(d.Permissions), &rules))
	if !reflect.DeepEqual(rules, req.Permissions) {
		t.Fatal("ordered permissions changed")
	}
	owner, e := r.store.agentOwnership(job.ID)
	must(e)
	if owner.Phase != "queued" || owner.Worktree != "" || owner.ProjectID != "" || !strings.HasPrefix(owner.SessionID, "ses_ocq_") || !strings.HasPrefix(owner.PromptID, "msg_ocq_") || !strings.HasPrefix(owner.Token, "owner_") {
		t.Fatal("receipt/ownership linkage invalid or provisioning occurred")
	}
	write("accepted-intent.json", encode(record))
	write(phase+"-receipt.json", encode(receipt))
	write("accepted-job-id", []byte(job.ID))
	if phase == "admitted" {
		built, e := BuildAgentSubmissionDraft(s, d)
		must(e)
		if !bytes.Equal(built, record.Request) {
			t.Fatal("native draft bytes differ from real Go builder")
		}
		cli, e := canonicalAgentSubmission(AgentSubmission{Key: d.Key, Repo: d.Repo, Base: d.Base, Prompt: []byte(d.Prompt), Agent: d.Agent, Model: agentModelSelector(req.Model), Permissions: []byte(d.Permissions), MemoryBytes: req.MemoryBytes})
		must(e)
		if !bytes.Equal(cli, job.Request) {
			t.Fatal("existing CLI v2 bytes changed")
		}
		wrong := s
		wrong.Space = "sp-wrong-context"
		out, e := InspectAgentSubmission(state, wrong)
		must(e)
		if out.State != "other_context" {
			t.Fatal("wrong space not distinguished")
		}
		if _, e = ArmAgentSubmission(state, wrong, record.ID); e == nil {
			t.Fatal("wrong scope allowed claim")
		}
		// Make the ORIGINAL canonical repository unavailable. Receipt-first exact
		// recovery/retry must work without Git/location resolution or live API.
		must(os.Rename(repo, filepath.Join(root, "repo-moved")))
		must(os.Remove(filepath.Join(root, "supplied-repo")))
		must(os.Symlink(filepath.Join(root, "missing-repo"), filepath.Join(root, "supplied-repo")))
		w, e := OpenExistingAgentSubmitter(state)
		must(e)
		again, e := w.SubmitPreparedAgent(p.Key, []byte(p.Canonical))
		must(e)
		if !reflect.DeepEqual(job, again) {
			t.Fatal("exact prepared retry mutated receipt/job or re-resolved unavailable repository")
		}
		conflict := req
		conflict.Prompt = "DIFFERENT valid canonical prompt"
		if _, e = w.SubmitPreparedAgent(p.Key, encode(conflict)); e == nil {
			t.Fatal("same key different v2 accepted")
		}
		if _, e = w.SubmitPreparedAgent("fresh-missing-location", []byte(p.Canonical)); e == nil {
			t.Fatal("fresh admission bypassed live repository checks")
		}
		must(w.Close())
		q, e := OpenQueue(state)
		must(e)
		if _, e = q.SubmitAgent(AgentSubmission{Key: d.Key, Repo: d.Repo, Base: d.Base, Prompt: []byte(d.Prompt), Agent: d.Agent, Model: agentModelSelector(req.Model), Permissions: []byte(d.Permissions), MemoryBytes: req.MemoryBytes}); e == nil {
			t.Fatal("CLI dynamic location behavior unexpectedly changed")
		}
		must(q.Close())
		if _, e = AttemptAgentSubmission(state, s, record.ID); e == nil {
			t.Fatal("second journal attempt allowed")
		}
	}
	out, e := ReconcileAgentSubmission(state, s, record.ID)
	must(e)
	if out.State != "completed" || out.Observation != "matched" || out.Receipt.Job.ID != job.ID {
		t.Fatal("exact offline receipt recovery failed")
	}
	for table, want := range map[string]int{"receipts": 1, "jobs": 1, "agent_ownership": 1, "agent_operations": 0, "completion_notifications": 0, "projects": 0, "project_jobs": 0, "project_lanes": 0} {
		var n int
		must(r.store.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n))
		if n != want {
			t.Fatalf("unexpected %s rows %d", table, n)
		}
	}
	trace := load("bridge.trace")
	for _, secret := range []string{d.Prompt, p.Canonical, owner.Token} {
		if bytes.Contains(trace, []byte(secret)) {
			t.Fatal("private payload/token leaked to ordinary command trace")
		}
	}
	if _, e = os.Stat(filepath.Join(root, "unexpected-opencode.trace")); !os.IsNotExist(e) {
		t.Fatal("unexpected live OpenCode call")
	}
	t.Log("PASS actual native queued/not_run exact v2/ordered profile/ownership linkage; missing-repository receipt-first retry/recovery; conflict/fresh-location/second-attempt refusals; zero API/prompt/provision/operations/project, no private trace payload")
}
