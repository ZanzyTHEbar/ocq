// One real, prompt-free private OpenCode 2.2.6 observation scenario. It never
// fabricates a successful producer event or executes an agent. The optional
// fixture mode stops after creating an explicitly owned empty session.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"ocq/internal/core"
)

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func check(ok bool, s string) {
	if !ok {
		panic(s)
	}
}
func hash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func wait(why string, f func() bool) {
	deadline := time.Now().Add(10 * time.Second)
	for !f() {
		check(time.Now().Before(deadline), why)
		time.Sleep(20 * time.Millisecond)
	}
}

func main() {
	if len(os.Args) == 3 && os.Args[1] == "--dispatcher" {
		q, e := core.OpenQueue(os.Args[2])
		must(e)
		defer q.Close()
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer cancel()
		must(q.Dispatch(ctx, 1, 0, true, nil))
		return
	}
	check(len(os.Args) == 5 || len(os.Args) == 6 && (os.Args[5] == "--fixture" || os.Args[5] == "--resume-created"), "usage: herdr-agent-reconcile BRIDGE FRESH_ROOT OWNED_REPO OWNED_WORKTREE [--fixture | --resume-created]")
	resume := len(os.Args) == 6 && os.Args[5] == "--resume-created"
	bridge, e := filepath.Abs(os.Args[1])
	must(e)
	root, e := filepath.Abs(os.Args[2])
	must(e)
	repo, e := filepath.Abs(os.Args[3])
	must(e)
	worktree, e := filepath.Abs(os.Args[4])
	must(e)
	profile := os.Getenv("OCQ_PRIVATE_SERVER_PROFILE")
	endpoint := os.Getenv("OCQ_PRIVATE_SERVER_URL")
	parsed, e := url.Parse(endpoint)
	must(e)
	check(parsed.Scheme == "http" && parsed.Hostname() == "127.0.0.1" && parsed.Port() != "" && parsed.User == nil, "explicit owned loopback service required")
	check(strings.HasPrefix(profile, filepath.Dir(root)+string(os.PathSeparator)), "dedicated private service profile required; never use shared discovery")
	profileInfo, e := os.Stat(profile)
	must(e)
	check(profileInfo.Mode().IsRegular() && profileInfo.Mode().Perm() == 0600, "service profile must be private")
	raw, e := os.ReadFile(profile)
	must(e)
	var auth struct {
		Password string `json:"password"`
	}
	must(json.Unmarshal(raw, &auth))
	check(auth.Password != "", "private service password absent")
	if !resume {
		must(os.Mkdir(root, 0700))
	}
	state := filepath.Join(root, "queue")
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: nil}}
	api := func(method, path string, body []byte) []byte {
		// Setup only: one exact empty session POST, then GETs. Production observation
		// still goes through the real supported opencode CLI, whose fixture wrapper
		// rejects every non-GET invocation and records method/path without bodies.
		check(method == "GET" || method == "POST" && path == "/api/session", "unsafe fixture API method")
		f, e := os.OpenFile(filepath.Join(root, "setup-api.trace"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		must(e)
		_, e = fmt.Fprintln(f, method, path)
		must(e)
		must(f.Close())
		req, e := http.NewRequest(method, endpoint+path, bytes.NewReader(body))
		must(e)
		req.SetBasicAuth("opencode", auth.Password)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-opencode-directory", worktree)
		response, e := client.Do(req)
		must(e)
		defer response.Body.Close()
		b, e := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		must(e)
		check(response.StatusCode >= 200 && response.StatusCode < 300, "owned service request failed; do not retry POST")
		return b
	}
	info := api("GET", "/api/info", nil)
	var server struct {
		Version string `json:"version"`
		PID     int    `json:"pid"`
		Paths   struct {
			Tmp string `json:"tmp"`
		} `json:"paths"`
	}
	must(json.Unmarshal(info, &server))
	check(server.Version == "2.2.6" && strings.HasPrefix(server.Paths.Tmp, filepath.Dir(root)+string(os.PathSeparator)), "wrong server version or nonprivate runtime temp")
	must(os.WriteFile(filepath.Join(root, "server-info.json"), info, 0600))
	q, e := core.OpenQueue(state)
	must(e)
	defer q.Close()
	prompt := []byte("PRIVATE never-sent fixture request. No agent execution is authorized.")
	permissions := json.RawMessage(`[{"action":"*","resource":"*","effect":"deny"}]`)
	job, e := q.SubmitAgent(core.AgentSubmission{Key: "owned-empty-session", Repo: repo, Base: "HEAD", Agent: "build", Model: "openai/gpt-5#default", Prompt: prompt, Permissions: permissions, MemoryBytes: 1})
	must(e)
	check(job.Agent != nil, "ownership receipt absent")
	originalOwnership := *job.Agent
	metadata := map[string]any{"ocq": map[string]any{"job_id": job.ID, "request_digest": job.Digest, "prompt_digest": hash(prompt), "owner_token": job.Agent.Token}}
	body, e := json.Marshal(map[string]any{"id": job.Agent.SessionID, "title": "ocq empty observation fixture", "agent": "build", "model": map[string]string{"providerID": "openai", "id": "gpt-5", "variant": "default"}, "location": map[string]string{"directory": worktree}, "metadata": metadata, "permissions": permissions})
	must(e)
	var created []byte
	if resume {
		retained, e := os.ReadFile(filepath.Join(root, "session-create-intent.json"))
		must(e)
		check(bytes.Equal(retained, body), "exact supplied session creation intent changed; no adoption or POST retry")
		created, e = os.ReadFile(filepath.Join(root, "session-created.json"))
		must(e)
	} else {
		must(os.WriteFile(filepath.Join(root, "session-create-intent.json"), body, 0600))
		created = api("POST", "/api/session", body)
		must(os.WriteFile(filepath.Join(root, "session-created.json"), created, 0600))
	}
	var session struct {
		ID        string `json:"id"`
		ParentID  string `json:"parentID"`
		ProjectID string `json:"projectID"`
		Agent     string `json:"agent"`
		Model     struct {
			ID       string `json:"id"`
			Provider string `json:"providerID"`
			Variant  string `json:"variant"`
		} `json:"model"`
		Location struct {
			Directory string `json:"directory"`
		} `json:"location"`
		Metadata    json.RawMessage `json:"metadata"`
		Permissions json.RawMessage `json:"permissions"`
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	must(json.Unmarshal(created, &envelope))
	check(len(envelope.Data) > 0, "supported session data envelope absent; do not retry POST")
	must(json.Unmarshal(envelope.Data, &session))
	check(session.ID == job.Agent.SessionID, "saved creation response is not the exact supplied session")
	if resume {
		current := api("GET", "/api/session/"+job.Agent.SessionID, nil)
		must(os.WriteFile(filepath.Join(root, "session-recovered-get.json"), current, 0600))
		must(json.Unmarshal(current, &envelope))
		must(json.Unmarshal(envelope.Data, &session))
	}
	check(session.ID == job.Agent.SessionID && session.ParentID == "" && session.ProjectID != "" && session.Agent == "build" && session.Model.ID == "gpt-5" && session.Model.Provider == "openai" && session.Model.Variant == "default" && session.Location.Directory == worktree, "empty session identity/profile/location mismatch; never adopt a replacement")
	var actualMetadata any
	must(json.Unmarshal(session.Metadata, &actualMetadata))
	want, e := json.Marshal(metadata)
	must(e)
	got, e := json.Marshal(actualMetadata)
	must(e)
	check(bytes.Equal(want, got), "session ownership metadata mismatch")
	var actualRules, wantedRules any
	must(json.Unmarshal(session.Permissions, &actualRules))
	must(json.Unmarshal(permissions, &wantedRules))
	a, e := json.Marshal(actualRules)
	must(e)
	b, e := json.Marshal(wantedRules)
	must(e)
	check(bytes.Equal(a, b), "explicit deny permissions changed")
	// Explicit synthetic recovery fixture: these rows describe a known owned
	// checkout and actual empty session, NOT a launch, consumed prompt or result.
	db, e := sql.Open("sqlite", filepath.Join(state, "queue.db"))
	must(e)
	defer db.Close()
	tx, e := db.Begin()
	must(e)
	_, e = tx.Exec("UPDATE agent_ownership SET worktree=?,project_id=?,phase='session_created' WHERE job_id=?", worktree, session.ProjectID, job.ID)
	must(e)
	_, e = tx.Exec("UPDATE jobs SET state='unknown',outcome='uncertain',reason='fixture: owned empty session; no prompt sent; capacity held' WHERE id=?", job.ID)
	must(e)
	must(tx.Commit())
	must(os.MkdirAll(filepath.Join(state, "jobs", job.ID), 0700))
	messages := api("GET", "/api/session/"+job.Agent.SessionID+"/message?limit=100&order=asc", nil)
	must(os.WriteFile(filepath.Join(root, "messages-before.json"), messages, 0600))
	check(!bytes.Contains(messages, []byte(`"type":"user"`)) && !bytes.Contains(messages, []byte(`"type":"assistant"`)) && !bytes.Contains(messages, []byte(`"type":"idle"`)), "empty fixture contains execution history")
	spare, e := q.SubmitCommand("queued-no-execution", worktree, []string{"/bin/true"}, 0)
	must(e)
	r, e := core.OpenReader(state)
	must(e)
	detail, e := r.QueueJob(job.ID)
	must(e)
	must(r.Close())
	t := core.AgentReconcileTarget{CommandResolutionTarget: core.CommandResolutionTarget{Machine: "local", Space: "sp-agent-fixture", Queue: detail.QueueIdentity, Job: job.ID, Sequence: detail.Job.Sequence}, Session: job.Agent.SessionID, Worktree: worktree}
	if len(os.Args) == 6 && os.Args[5] == "--fixture" {
		must(json.NewEncoder(os.Stdout).Encode(map[string]any{"state_dir": state, "target": t, "spare": spare.ID, "server_pid": server.PID, "note": "actual empty owned session; no prompt intent or execution; imported unknown ownership only"}))
		return
	}
	trace := filepath.Join(filepath.Dir(root), "api.trace")
	apiCount := func() int {
		b, e := os.ReadFile(trace)
		if os.IsNotExist(e) {
			return 0
		}
		must(e)
		check(!bytes.Contains(b, []byte("DENIED")), "observer attempted a mutating API method")
		return bytes.Count(b, []byte("\n"))
	}
	// The real commands-only dispatcher holds the same singleton lock; it must
	// never admit/poll this agent or be stopped/adopted by reconciliation.
	self, e := os.Executable()
	must(e)
	dispatch := exec.Command(self, "--dispatcher", state)
	must(dispatch.Start())
	defer func() {
		if dispatch.ProcessState == nil {
			_ = dispatch.Process.Signal(syscall.SIGTERM)
			_ = dispatch.Wait()
		}
	}()
	wait("dispatcher did not hold the slot", func() bool { j, e := q.Job(spare.ID); must(e); return strings.Contains(j.Reason, "capacity blocked") })
	beforeAPI := apiCount()
	_, e = q.Reconcile(job.ID)
	check(e != nil && strings.Contains(e.Error(), "dispatcher lock held") && apiCount() == beforeAPI, "reconcile bypassed live dispatcher exclusion")
	must(dispatch.Process.Signal(syscall.SIGTERM))
	must(dispatch.Wait())
	args := func(action string, t core.AgentReconcileTarget, extra ...string) []string {
		return append([]string{"agent-reconcile-" + action, t.Space, t.Queue, t.Job, t.Sequence, t.Session, t.Worktree}, extra...)
	}
	call := func(a ...string) ([]byte, error) {
		c := exec.Command(bridge, a...)
		c.Env = append(os.Environ(), "OCQ_STATE_DIR="+state)
		return c.Output()
	}
	read := func(t core.AgentReconcileTarget) core.AgentReconcileStatus {
		b, e := call(args("status", t)...)
		must(e)
		check(!bytes.Contains(b, []byte(originalOwnership.Token)) && !bytes.Contains(b, prompt) && !bytes.Contains(b, []byte(`"owner_token"`)) && !bytes.Contains(b, []byte(`"request"`)) && !bytes.Contains(b, []byte(`"terminal_evidence"`)), "safe DTO leaked private ownership/request/evidence")
		var out core.AgentReconcileStatus
		must(json.Unmarshal(b, &out))
		return out
	}
	scalar := func(query string, values ...any) string {
		var s string
		must(db.QueryRow(query, values...).Scan(&s))
		return s
	}
	spareBefore := scalar("SELECT json_array(state,reason,outcome,validation,pid,hex(evidence)) FROM jobs WHERE id=?", spare.ID)
	receiptBefore := scalar("SELECT hex(canonical)||digest FROM receipts WHERE job_id=?", job.ID)
	initial := read(t)
	check(initial.State == "none" && initial.Current != nil && apiCount() == beforeAPI, "view made a live API observation")
	preparedBytes, e := call(args("prepare", t, initial.Current.Digest)...)
	must(e)
	var prepared core.AgentReconcileStatus
	must(json.Unmarshal(preparedBytes, &prepared))
	check(prepared.State == "prepared" && apiCount() == beforeAPI, "prepare observed the API")
	wrong := t
	wrong.Space = "sp-other"
	check(read(wrong).State == "blocked", "another space adopted observation intent")
	_, e = call(args("arm", wrong, prepared.ID, prepared.IntentDigest)...)
	check(e != nil && apiCount() == beforeAPI, "wrong context armed/observed")
	_, e = call(args("arm", t, prepared.ID, prepared.IntentDigest)...)
	must(e)
	check(read(t).State == "armed" && apiCount() == beforeAPI, "claim/reload dispatched observation")
	lostArgs := append([]string{"-c", `"$@" >/dev/null || exit $?; exit 73`, "lost-observation-response", bridge}, args("attempt", t, prepared.ID, prepared.IntentDigest)...)
	lost := exec.Command("/bin/sh", lostArgs...)
	lost.Env = append(os.Environ(), "OCQ_STATE_DIR="+state)
	e = lost.Run()
	check(e != nil && lost.ProcessState.ExitCode() == 73, "response-loss wrapper failed")
	afterAPI := apiCount()
	check(afterAPI > beforeAPI, "core observer did not make a real GET")
	observed := read(t)
	check(observed.State == "recorded" && observed.Result != nil && observed.Result.ID == prepared.ID && observed.Result.IntentDigest == prepared.IntentDigest && observed.Result.BeforeDigest == initial.Current.Digest && observed.Result.Outcome == "observer_returned" && observed.Result.After.Job.State == "unknown" && strings.Contains(observed.Result.After.Job.Reason, "no durable prompt intent") && observed.CurrentMatch == "matches_operation_snapshot", "operation receipt missing or fabricated completion")
	check(observed.Result.After.Job.Validation == "not_run" && observed.Result.After.FirstTerminal == nil, "empty session promoted to validated completion")
	_, e = call(args("attempt", t, prepared.ID, prepared.IntentDigest)...)
	check(e != nil && apiCount() == afterAPI, "duplicate attempt re-observed API")
	check(scalar("SELECT count(*) FROM completion_notifications") == "0" && scalar("SELECT count(*) FROM jobs WHERE state IN ('launching','running','unknown')") == "1", "uncertain agent released capacity or emitted completion")
	check(scalar("SELECT hex(canonical)||digest FROM receipts WHERE job_id=?", job.ID) == receiptBefore && scalar("SELECT json_array(state,reason,outcome,validation,pid,hex(evidence)) FROM jobs WHERE id=?", spare.ID) == spareBefore, "observation changed receipt or another job")
	// Simulate losing the receipt after a completed attempt. Merely seeing the
	// changed job must not establish that our call returned. Restore the same
	// immutable file/inode only as fixture cleanup; the product never does this.
	journal := filepath.Join(state, "herdr-agent-reconcile-"+hash([]byte(t.Queue+"\n"+t.Job+"\n"+t.Sequence)))
	saved := filepath.Join(root, "saved-result.json")
	must(os.Rename(filepath.Join(journal, "result.json"), saved))
	uncertain := read(t)
	check(uncertain.State == "uncertain" && uncertain.Result == nil && apiCount() == afterAPI, "missing operation receipt inferred completion or retried API")
	must(os.Rename(saved, filepath.Join(journal, "result.json")))
	check(read(t).ResultDigest == observed.ResultDigest && apiCount() == afterAPI, "read-only receipt recovery changed operation")
	messagesAfter := api("GET", "/api/session/"+job.Agent.SessionID+"/message?limit=100&order=asc", nil)
	check(bytes.Equal(messages, messagesAfter), "reconciliation changed session transcript")
	missing := filepath.Join(root, "missing")
	_, e = core.InspectAgentReconciliation(missing, t)
	check(e != nil, "status initialized missing queue")
	_, e = os.Stat(missing)
	check(os.IsNotExist(e), "missing queue created")
	must(json.NewEncoder(os.Stdout).Encode(map[string]any{"result": "PASS", "state_dir": state, "operation_id": prepared.ID, "operation_receipt_sha256": observed.ResultDigest, "job_state": "unknown", "terminal_notifications": 0, "checks": "real private 2.2.6 empty owned session; no prompt or agent execution; dispatcher exclusivity; status/prepare/arm are stored-only; exact operation receipt; lost response/reload; missing receipt remains uncertain; duplicate/wrong context refused; GET-only observer; no input/permissions/session mutations; no capacity release or terminal success claim"}))
}
