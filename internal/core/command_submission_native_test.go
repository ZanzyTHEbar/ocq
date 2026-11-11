package core

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// One opt-in acceptance scenario spanning actual native admission, a separately
// bounded real command dispatcher, and immutable receipt recovery after paths
// change. No mocked collaborators, project/agent setup or unit matrix.
func TestCommandSubmissionNativeFixture(t *testing.T) {
	root := os.Getenv("OCQ_COMMAND_FIXTURE_ROOT")
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
	state := filepath.Join(root, "queue")
	phase := os.Getenv("OCQ_COMMAND_FIXTURE_PHASE")
	if phase == "prepare" {
		if _, e := os.Lstat(state); !os.IsNotExist(e) {
			t.Fatal("fresh fixture queue required; never retry uncertain preparation")
		}
		q, e := OpenQueue(state)
		must(e)
		must(q.Close())
		for _, n := range []string{"cwd-original", "cwd-other"} {
			must(os.Mkdir(filepath.Join(root, n), 0700))
		}
		printf, e := os.ReadFile("/usr/bin/printf")
		must(e)
		// Keep the basename: some hosts supply a multicall printf binary.
		must(os.WriteFile(filepath.Join(root, "bin/printf"), printf, 0700))
		must(os.Symlink(filepath.Join(root, "bin/printf"), filepath.Join(root, "chosen-executable")))
		must(os.Symlink(filepath.Join(root, "cwd-original"), filepath.Join(root, "chosen-cwd")))
		d := CommandSubmissionDraft{Key: "native-general-command", Executable: filepath.Join(root, "chosen-executable"), Arguments: []string{"%s\n", "PRIVATE_COMMAND café\u202e\u200b\n\x01", "literal shell syntax: $(touch NEVER_CREATED)", strings.Repeat("long-argument-", 22)}, Cwd: filepath.Join(root, "chosen-cwd"), Memory: "0"}
		write("form.json", encode(d))
		// Explicit path intake does not prove executable suitability, just as CLI.
		must(os.WriteFile(filepath.Join(root, "not-executable"), []byte("metadata only"), 0600))
		r, e := OpenReader(state)
		must(e)
		s, e := r.CommandSubmissionContext("sp-contract-fixture")
		must(e)
		nonexec := d
		nonexec.Executable = filepath.Join(root, "not-executable")
		_, e = BuildCommandSubmissionDraft(s, nonexec)
		must(e)
		jobs, e := r.QueueJobs(0, 20)
		must(e)
		if len(jobs.Items) != 0 {
			t.Fatal("draft created work")
		}
		must(r.Close())
		t.Log("PASS fresh schema5/no jobs; explicit path builder preserves CLI non-executable-file semantics; no project/agent")
		return
	}
	if phase != "queued" && phase != "mutate" && phase != "verify" {
		t.Fatal("explicit prepare, queued, mutate or verify phase required")
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
	j, e := openCommandSubmission(state, s)
	must(e)
	status, record := commandSubmissionStatus(j, s)
	j.close()
	if status.State != "completed" && status.State != "attempted" {
		t.Fatal("actual native admission required")
	}
	p, e := parsePreparedCommandSubmission(record.Request)
	must(e)
	receipt, e := r.CommandSubmissionReceipt(p.Key, []byte(p.Canonical))
	must(e)
	job, e := r.store.get(receipt.Job.ID)
	must(e)
	if p.Scope != s || p.Key != "native-general-command" || job.Digest != sha256Hex([]byte(p.Canonical)) || !bytes.Equal(job.Request, []byte(p.Canonical)) || job.Validation != "not_run" {
		t.Fatal("immutable receipt/context mismatch")
	}
	write("accepted-intent.json", encode(record))
	write(phase+"-receipt.json", encode(receipt))
	var d CommandSubmissionDraft
	must(json.Unmarshal(load("form.json"), &d))
	if phase == "queued" {
		expected, e := BuildCommandSubmissionDraft(s, d)
		must(e)
		if !bytes.Equal(expected, record.Request) || job.State != "queued" || job.Stdout != "" || job.Stderr != "" {
			t.Fatal("native exact draft mismatch or premature execution")
		}
		cli, e := newCommandRequest(append([]string{d.Executable}, d.Arguments...), d.Cwd, 0)
		must(e)
		canonical, _, e := encodeCommandRequest(cli)
		must(e)
		if !bytes.Equal(canonical, job.Request) {
			t.Fatal("v1 CLI byte format changed")
		}
		projection, e := commandSubmissionReview(record.Request)
		must(e)
		write("expected-review.json", projection)
		wrong := s
		wrong.Space = "sp-wrong-context"
		out, e := InspectCommandSubmission(state, wrong)
		must(e)
		if out.State != "other_context" {
			t.Fatal("context not distinguished")
		}
		if _, e = ArmCommandSubmission(state, wrong, record.ID); e == nil {
			t.Fatal("wrong space allowed mutation")
		}
		wrong = s
		wrong.Queue = strings.Repeat("0", 64)
		if _, e = InspectCommandSubmission(state, wrong); e == nil {
			t.Fatal("wrong queue allowed")
		}
		write("accepted-job-id", []byte(job.ID))
		t.Log("PASS one actual native admission QUEUED/not_run; exact pre-resolved v1 CLI canonical bytes/key/digest/context; no execution/project; wrong context/queue refused")
	} else {
		if job.State != "finished" || job.Outcome != "exited" || job.ExitCode == nil || *job.ExitCode != 0 {
			t.Fatalf("real harmless dispatch required: state=%s outcome=%s", job.State, job.Outcome)
		}
		log, e := r.store.openLog(job.ID, false)
		must(e)
		output, e := io.ReadAll(log)
		must(e)
		must(log.Close())
		want := []byte(strings.Join(d.Arguments[1:], "\n") + "\n")
		if !bytes.Equal(output, want) {
			t.Fatal("real argv/output not exact")
		}
		if _, e = os.Stat(filepath.Join(root, "cwd-original", "NEVER_CREATED")); !os.IsNotExist(e) {
			t.Fatal("shell syntax was executed")
		}
		write("exact-output.bin", output)
		if phase == "mutate" {
			// Change BOTH input symlinks AND the paths already resolved into v1.
			must(os.Remove(filepath.Join(root, "chosen-executable")))
			must(os.Symlink("/usr/bin/false", filepath.Join(root, "chosen-executable")))
			must(os.Remove(filepath.Join(root, "bin/printf")))
			must(os.Symlink("/usr/bin/false", filepath.Join(root, "bin/printf")))
			must(os.Rename(filepath.Join(root, "cwd-original"), filepath.Join(root, "cwd-moved")))
			must(os.Symlink(filepath.Join(root, "cwd-other"), filepath.Join(root, "cwd-original")))
			must(os.Remove(filepath.Join(root, "chosen-cwd")))
			must(os.Symlink(filepath.Join(root, "cwd-other"), filepath.Join(root, "chosen-cwd")))
			now, e := newCommandRequest(append([]string{d.Executable}, d.Arguments...), d.Cwd, 0)
			must(e)
			changed, _, e := encodeCommandRequest(now)
			must(e)
			if bytes.Equal(changed, job.Request) {
				t.Fatal("fixture did not change dynamic resolution")
			}
			w, e := OpenExistingCommandSubmitter(state)
			must(e)
			again, e := w.SubmitPreparedCommand(p.Key, []byte(p.Canonical))
			must(e)
			if !reflect.DeepEqual(job, again) {
				t.Fatal("exact prepared retry mutated immutable receipt/job after path changes")
			}
			if _, e = w.SubmitPreparedCommand(p.Key, changed); e == nil {
				t.Fatal("conflicting same key admitted")
			}
			must(w.Close())
			q, e := OpenQueue(state)
			must(e)
			if _, e = q.SubmitCommand(p.Key, d.Cwd, append([]string{d.Executable}, d.Arguments...), 0); e == nil {
				t.Fatal("existing dynamic CLI behavior unexpectedly changed")
			}
			must(q.Close())
			if _, e = AttemptCommandSubmission(state, s, record.ID); e == nil {
				t.Fatal("second admission attempt permitted")
			}
		}
		out, e := ReconcileCommandSubmission(state, s, record.ID)
		must(e)
		if out.State != "completed" || out.Observation != "matched" || out.Receipt.Job.ID != job.ID {
			t.Fatal("exact recovery re-resolved paths or lost receipt")
		}
		t.Log("PASS real command output/argv without shell; changed input AND resolved symlinks/cwd; exact receipt recovery/retry without re-resolution; conflicts and second attempt refused; dynamic CLI unchanged")
	}
	for table, want := range map[string]int{"receipts": 1, "jobs": 1, "projects": 0, "project_jobs": 0, "agent_operations": 0, "agent_ownership": 0} {
		var n int
		must(r.store.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n))
		if n != want {
			t.Fatalf("unexpected %s rows %d", table, n)
		}
	}
}
