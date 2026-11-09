// One real Workmux/shared-OpenCode acceptance flow. Retains all owned resources.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

type job struct {
	ID         string `json:"id"`
	Sequence   int64  `json:"sequence"`
	Digest     string `json:"digest"`
	State      string `json:"state"`
	Reason     string `json:"reason"`
	Outcome    string `json:"outcome"`
	Exit       *int   `json:"exit_code"`
	Validation string `json:"validation"`
	Stdout     string `json:"stdout"`
	Request    struct {
		Version     int             `json:"version"`
		Repo        string          `json:"repo"`
		Base        string          `json:"base"`
		Prompt      string          `json:"prompt"`
		Agent       string          `json:"agent"`
		Model       json.RawMessage `json:"model"`
		Permissions json.RawMessage `json:"permissions"`
	} `json:"request"`
	Agent *struct {
		Session          string `json:"session_id"`
		Prompt           string `json:"prompt_id"`
		Worktree         string `json:"worktree"`
		Phase            string `json:"phase"`
		ServerOutcome    string `json:"server_outcome"`
		Token            string `json:"owner_token"`
		TerminalEvidence string `json:"terminal_evidence"`
	} `json:"agent"`
}
type identity struct {
	PID   int    `json:"pid"`
	Start uint64 `json:"start_ticks"`
}
type scenario struct {
	root, repo, state, bin, legacy, self string
	log                                  *os.File
	dispatcher                           *exec.Cmd
	done                                 chan error
	owner                                identity
	job                                  job
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func check(ok bool, text string) {
	if !ok {
		panic(text)
	}
}
func save(path string, b []byte) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	must(err)
	_, err = f.Write(b)
	must(errors.Join(err, f.Sync(), f.Close()))
}
func who(pid int) identity {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	must(err)
	f := strings.Fields(string(b)[strings.LastIndex(string(b), ") ")+2:])
	n, err := strconv.ParseUint(f[19], 10, 64)
	must(err)
	return identity{pid, n}
}
func live(p identity) bool {
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", p.PID)); errors.Is(err, os.ErrNotExist) {
		return false
	}
	return who(p.PID) == p
}
func (s *scenario) note(text string) {
	fmt.Fprintln(s.log, time.Now().UTC().Format(time.RFC3339Nano), text)
	must(s.log.Sync())
}
func command(dir, bin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, bin, args...)
	c.Dir = dir
	b, err := c.Output()
	if err != nil {
		return nil, fmt.Errorf("CLI %s failed (stderr not persisted): %w", filepath.Base(bin), err)
	}
	return b, nil
}
func (s *scenario) cli(args ...string) []byte {
	b, err := command(s.repo, s.bin, append([]string{"--state-dir", s.state}, args...)...)
	must(err)
	return b
}
func (s *scenario) status() job {
	var j job
	must(json.Unmarshal(s.cli("status", s.job.ID), &j))
	s.job = j
	return j
}
func (s *scenario) api(method, path, name string, body any) []byte {
	args := []string{"api", method, path}
	if method != "get" {
		b, err := json.Marshal(map[string]any{"method": method, "path": path, "body": body})
		must(err)
		save(filepath.Join(s.root, name+".intent.json"), b)
	}
	if body != nil {
		b, err := json.Marshal(body)
		must(err)
		args = append(args, "--data", string(b))
	}
	b, err := command(s.job.Agent.Worktree, "opencode", args...)
	must(err)
	if len(b) > 0 && name != "" {
		save(filepath.Join(s.root, name+".response.json"), b)
	}
	return b
}
func (s *scenario) wait(text string, fn func() bool) {
	end := time.Now().Add(120 * time.Second)
	for time.Now().Before(end) {
		if fn() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	panic("timeout: " + text)
}
func (s *scenario) start() {
	file, err := os.OpenFile(filepath.Join(s.root, "dispatcher.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	must(err)
	c := exec.Command(s.self, "owned-exec", filepath.Join(s.root, "dispatcher.identity"), s.bin, "--state-dir", s.state, "dispatch")
	c.Stdout, c.Stderr = file, file
	must(c.Start())
	s.dispatcher = c
	s.owner = who(c.Process.Pid)
	s.done = make(chan error, 1)
	go func() { s.done <- c.Wait(); file.Close() }()
	s.note(fmt.Sprintf("owned dispatcher pid=%d start=%d", s.owner.PID, s.owner.Start))
}
func (s *scenario) stop(sig syscall.Signal) {
	if s.dispatcher == nil {
		return
	}
	if live(s.owner) {
		must(syscall.Kill(s.owner.PID, sig))
	}
	select {
	case err := <-s.done:
		if sig != syscall.SIGKILL {
			must(err)
		}
	case <-time.After(45 * time.Second):
		panic("owned dispatcher did not exit")
	}
	check(!live(s.owner), "owned dispatcher remains")
	s.note(fmt.Sprintf("verified dispatcher exit pid=%d start=%d", s.owner.PID, s.owner.Start))
	s.dispatcher = nil
}
func (s *scenario) active() bool {
	b := s.api("get", "/api/session/active", "", nil)
	var response struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	must(json.Unmarshal(b, &response))
	_, active := response.Data[s.job.Agent.Session]
	return active
}
func (s *scenario) run() {
	// Run only once per evidence root. No external-mutation replay on a rerun.
	_, err := os.Stat(filepath.Join(s.state, "queue.db"))
	check(errors.Is(err, os.ErrNotExist), "acceptance state already exists; inspect exact receipts, do not rerun this flow")
	oldArgs := []string{"--state-dir", s.state, "submit", "--key", "legacy-receipt", "--cwd", s.repo, "--memory-bytes", "1", "--", "/usr/bin/printf", "%s\n", "legacy"}
	b, err := command(s.repo, s.legacy, oldArgs...)
	must(err)
	var old job
	must(json.Unmarshal(b, &old))
	_, err = command(s.repo, s.legacy, "--state-dir", s.state, "cancel", old.ID)
	must(err)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(s.state, "queue.db")+"?_pragma=busy_timeout(5000)")
	must(err)
	defer db.Close()
	var before []byte
	var digest string
	var version int
	must(db.QueryRow("SELECT canonical,digest FROM receipts WHERE job_id=?", old.ID).Scan(&before, &digest))
	must(db.QueryRow("PRAGMA user_version").Scan(&version))
	check(version == 1, "not a real legacy schema1 fixture")
	save(filepath.Join(s.root, "legacy-canonical.json"), before)
	prompt := "Reply only OCQ-INTEGRATION-OK. Do not call tools, inspect files, delegate, or change anything."
	save(filepath.Join(s.root, "prompt.txt"), []byte(prompt))
	save(filepath.Join(s.root, "permissions.json"), []byte(`[{"action":"*","resource":"*","effect":"ask"}]`))
	b = s.cli("submit-agent", "--key", "agent-integration-once", "--repo", s.repo, "--base", "HEAD", "--prompt-file", filepath.Join(s.root, "prompt.txt"), "--agent", "plan", "--model", "openai/gpt-6.1-sol#low", "--permissions-file", filepath.Join(s.root, "permissions.json"), "--memory-bytes", "67108864")
	must(json.Unmarshal(b, &s.job))
	save(filepath.Join(s.root, "queued-job.json"), b)
	check(s.job.Request.Version == 2 && s.job.Agent != nil && s.job.Agent.Session != "" && s.job.Agent.Prompt != "" && s.job.Agent.Phase == "queued", "agent identity not preallocated while queued")
	base, err := command(s.repo, "git", "rev-parse", "HEAD")
	must(err)
	check(s.job.Request.Base == strings.TrimSpace(string(base)), "base not pinned")
	var after []byte
	var afterDigest string
	must(db.QueryRow("SELECT canonical,digest FROM receipts WHERE job_id=?", old.ID).Scan(&after, &afterDigest))
	must(db.QueryRow("PRAGMA user_version").Scan(&version))
	check(version == 5 && string(before) == string(after) && digest == afterDigest, "migration rewrote v1 receipt")
	b = s.cli(oldArgs[2:]...)
	var retry job
	must(json.Unmarshal(b, &retry))
	check(retry.ID == old.ID && retry.State == "cancelled", "v1 matching retry behavior changed")
	s.note("PASS schema1->3 migration preserved real legacy bytes/digest/receipt; v1 retry unchanged; v2 pinned base and preallocated session/prompt IDs")
	s.start()
	s.wait("prompt acknowledgment", func() bool {
		j := s.status()
		if j.State == "unknown" && j.Agent.Phase != "prompt_intent" && j.Agent.Phase != "prompt_accepted" {
			panic(j.Reason)
		}
		return j.Agent.Phase == "prompt_accepted" || j.Agent.Phase == "terminal"
	})
	j := s.status()
	check(j.Agent.Worktree != "" && j.Agent.Worktree != s.repo, "same writable worktree reused")
	wasActive := s.active()
	s.note(fmt.Sprintf("prompt accepted before dispatcher kill; exact session active at inspection=%t (not inferred from CLI exit)", wasActive))
	s.stop(syscall.SIGKILL)
	b = s.cli("reconcile", s.job.ID)
	must(json.Unmarshal(b, &s.job))
	s.wait("exact-session terminal correlation", func() bool {
		b := s.cli("reconcile", s.job.ID)
		must(json.Unmarshal(b, &s.job))
		return s.job.State == "finished"
	})
	check(s.job.Outcome == "agent_succeeded" && s.job.Agent.ServerOutcome == "succeeded" && s.job.Exit == nil && s.job.Validation == "not_run", "agent outcome conflated with CLI exit/validation")
	b = s.cli("logs", s.job.ID)
	check(strings.Contains(string(b), "OCQ-INTEGRATION-OK"), "separate user output missing")
	var order string
	must(db.QueryRow("SELECT group_concat(kind,',') FROM (SELECT kind FROM agent_operations WHERE job_id=? ORDER BY rowid)", s.job.ID).Scan(&order))
	check(order == "worktree,session,prompt", "external intents not serialized")
	// Follow every API page even though this session is deliberately tiny.
	cursor := ""
	users := 0
	pages := 0
	for {
		path := "/api/session/" + s.job.Agent.Session + "/message?limit=1"
		if cursor == "" {
			path += "&order=asc"
		} else {
			path += "&cursor=" + cursor
		}
		b := s.api("get", path, fmt.Sprintf("history-page-%03d", pages), nil)
		pages++
		var p struct {
			Data []struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"data"`
			Cursor struct {
				Next *string `json:"next"`
			} `json:"cursor"`
		}
		must(json.Unmarshal(b, &p))
		for _, m := range p.Data {
			if m.Type == "user" {
				users++
				check(m.ID == s.job.Agent.Prompt, "second/foreign prompt before completion")
			}
		}
		if p.Cursor.Next == nil {
			break
		}
		cursor = *p.Cursor.Next
		check(pages < 100, "pagination did not terminate")
	}
	check(users == 1, "not exactly one durable prompt anchor")
	s.note(fmt.Sprintf("PASS one real tiny plan-agent prompt; exact correlated outcome and output after dispatcher-only SIGKILL; %d transcript pages, one user prompt", pages))
	// API visibility only, NOT a tool-triggered permission test. Never approve.
	permissionID := "per_ocq_" + strings.TrimPrefix(s.job.ID, "job-")
	formID := "frm_ocq_" + strings.TrimPrefix(s.job.ID, "job-")
	permission := map[string]any{"id": permissionID, "action": "ocq-visibility", "resources": []string{"disposable"}, "metadata": map[string]any{"ocq_acceptance": true}}
	b = s.api("post", "/api/session/"+s.job.Agent.Session+"/permission", "permission-visibility-create", permission)
	var permissionReply struct {
		Data struct {
			ID     string `json:"id"`
			Effect string `json:"effect"`
		} `json:"data"`
	}
	must(json.Unmarshal(b, &permissionReply))
	check(permissionReply.Data.ID == permissionID && permissionReply.Data.Effect == "ask", "permission visibility request not pending ask")
	form := map[string]any{"id": formID, "title": "Disposable API visibility probe", "fields": []any{map[string]any{"key": "note", "type": "string"}}}
	s.api("post", "/api/session/"+s.job.Agent.Session+"/form", "form-visibility-create", form)
	b = s.cli("reconcile", s.job.ID)
	must(json.Unmarshal(b, &s.job))
	check(s.job.State == "unknown" && s.job.Outcome == "blocked" && strings.Contains(s.job.Reason, "permission/form"), "pending API requests not visible as blocked")
	s.api("post", "/api/session/"+s.job.Agent.Session+"/permission/"+permissionID+"/reply", "permission-visibility-reject", map[string]string{"decision": "reject"})
	s.api("delete", "/api/session/"+s.job.Agent.Session+"/form/"+formID, "form-visibility-cancel", nil)
	b = s.cli("reconcile", s.job.ID)
	must(json.Unmarshal(b, &s.job))
	check(s.job.State == "finished", "settled visibility requests did not permit exact reinspection")
	s.note("PASS API-created permission/form visibility blocks completion/capacity; explicitly owned requests rejected/cancelled, never approved; not an agent-tool permission test")
	// Future foreign input must be detected without executing it.
	s.api("post", "/api/session/"+s.job.Agent.Session+"/prompt", "foreign-deferred-input", map[string]any{"id": "msg_ocq_foreign_" + strings.TrimPrefix(s.job.ID, "job-"), "text": "Deferred interference probe; do not execute.", "resume": false})
	b = s.cli("reconcile", s.job.ID)
	must(json.Unmarshal(b, &s.job))
	check(s.job.State == "unknown" && s.job.Agent.Phase == "session_interference", "future foreign input was silently adopted")
	check(!s.active(), "owned session is still active")
	save(filepath.Join(s.root, "final-job.json"), b)
	s.note("PASS foreign deferred input -> latched unknown, no resume/second execution; owned worktree/branch/session/results retained, exact session inactive")
}

func main() {
	syscall.Umask(0077)
	if len(os.Args) > 1 && os.Args[1] == "owned-exec" {
		b, err := json.Marshal(who(os.Getpid()))
		must(err)
		save(os.Args[2], b)
		must(syscall.Exec(os.Args[3], os.Args[3:], os.Environ()))
		return
	}
	check(len(os.Args) == 4, "usage: ocq-agent-acceptance OCQ LEGACY_OCQ PROOF_ROOT | --reconcile-existing OCQ PROOF_ROOT | --tools OCQ PROOF_ROOT | --verify-tools OCQ PROOF_ROOT")
	// The old spelling is now an alias for the non-replaying session check,
	// never the historical mutating permission/form visibility continuation.
	reconcile := os.Args[1] == "--reconcile-existing" || os.Args[1] == "--inspect-existing"
	tools := os.Args[1] == "--tools"
	verifyTools := os.Args[1] == "--verify-tools"
	binArg := os.Args[1]
	if reconcile || tools || verifyTools {
		binArg = os.Args[2]
	}
	bin, err := filepath.Abs(binArg)
	must(err)
	legacy, err := filepath.Abs(os.Args[2])
	must(err)
	root, err := filepath.Abs(os.Args[3])
	must(err)
	self, err := os.Executable()
	must(err)
	f, err := os.OpenFile(filepath.Join(root, "integration-result.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	must(err)
	s := &scenario{root: root, repo: filepath.Join(root, "repo"), state: filepath.Join(root, "integration-state"), bin: bin, legacy: legacy, self: self, log: f}
	code := 0
	func() {
		defer func() {
			if p := recover(); p != nil {
				code = 1
				s.note(fmt.Sprintf("FAIL: %v", p))
			}
		}()
		if verifyTools {
			b, err := os.ReadFile(filepath.Join(s.root, "queued-job.json"))
			must(err)
			must(json.Unmarshal(b, &s.job))
			b = s.cli("reconcile", s.job.ID)
			must(json.Unmarshal(b, &s.job))
			s.verifyTools()
		} else if tools {
			s.runTools()
		} else if reconcile {
			s.reconcileExisting()
		} else {
			s.run()
		}
	}()
	if s.dispatcher != nil {
		s.stop(syscall.SIGTERM)
	}
	s.note("owned dispatcher retired; all created worktrees/branches/sessions/results deliberately retained; inspect exact session state before any further action")
	if code == 0 {
		label := "PASS agent integration acceptance"
		if reconcile {
			label = "PASS retained session-scoped completion; no external mutation replay; validation not_run"
		}
		if tools {
			label = "PASS real tool edit/permission/background acceptance; validation not_run"
		}
		if verifyTools {
			label = "PASS recorded real tool API/DB/fixture verification; no execution replay"
		}
		s.note(label)
		fmt.Println(label)
	}
	fmt.Println("Artifacts:", root)
	must(f.Close())
	os.Exit(code)
}
