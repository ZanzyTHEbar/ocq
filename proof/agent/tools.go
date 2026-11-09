package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type proofTool struct {
	ID, Name, Type, Text string
	State                struct {
		Status                   string
		Input, Metadata, Content json.RawMessage
	}
}
type proofMessage struct {
	ID, Type, Text, Outcome string
	Content                 []proofTool
}

func (s *scenario) toolTranscript() []proofMessage {
	var all []proofMessage
	cursor := ""
	for page := 0; page < 100; page++ {
		query := url.Values{"limit": {"100"}}
		if cursor == "" {
			query.Set("order", "asc")
		} else {
			query.Set("cursor", cursor)
		}
		var env struct {
			Data   []proofMessage
			Cursor *struct{ Next *string }
		}
		must(json.Unmarshal(s.api("get", "/api/session/"+s.job.Agent.Session+"/message?"+query.Encode(), fmt.Sprintf("tools-history-%03d", page), nil), &env))
		check(env.Data != nil && env.Cursor != nil, "tool history envelope missing")
		all = append(all, env.Data...)
		if env.Cursor.Next == nil {
			return all
		}
		check(*env.Cursor.Next != "" && *env.Cursor.Next != cursor, "bad tool transcript cursor")
		cursor = *env.Cursor.Next
	}
	panic("tool transcript pagination exceeded bound")
}

type proofShell struct {
	ID, Cwd, Status string
	PID             int
	Metadata        struct{ SessionID string }
}
type toolPermission struct {
	ID, SessionID, Action string
	Resources             []string
	Source                struct{ Type, MessageID, ID string }
}

func (s *scenario) runningShells() []proofShell {
	var env struct {
		Location struct{ Directory string }
		Data     []proofShell
	}
	q := url.Values{"location[directory]": {s.job.Agent.Worktree}}
	must(json.Unmarshal(s.api("get", "/api/shell?"+q.Encode(), "tools-running-shells", nil), &env))
	check(env.Location.Directory == s.job.Agent.Worktree && env.Data != nil, "wrong shell location/envelope")
	for _, shell := range env.Data {
		check(shell.Cwd == s.job.Agent.Worktree && shell.Metadata.SessionID == s.job.Agent.Session, "unowned shell in fixture")
	}
	return env.Data
}

func (s *scenario) toolPermissions() []toolPermission {
	var env struct{ Data []toolPermission }
	must(json.Unmarshal(s.api("get", "/api/session/"+s.job.Agent.Session+"/permission", "tools-pending-permissions", nil), &env))
	return env.Data
}

// An explicit fixture operator, not product autoapproval. Validate the actual
// runtime tool source, complete input, action and resources before replying once.
func (s *scenario) approveFixture(p toolPermission) {
	check(p.SessionID == s.job.Agent.Session && p.Source.Type == "tool" && p.Source.ID != "" && p.Source.MessageID != "", "permission is not an owned actual tool request")
	var env struct {
		Data struct {
			ID, Type string
			Content  []proofTool
		}
	}
	must(json.Unmarshal(s.api("get", "/api/session/"+s.job.Agent.Session+"/message/"+p.Source.MessageID, "tools-permission-source-"+p.ID, nil), &env))
	check(env.Data.ID == p.Source.MessageID && env.Data.Type == "assistant", "permission source message mismatch")
	var tool *proofTool
	for i := range env.Data.Content {
		if env.Data.Content[i].Type == "tool" && env.Data.Content[i].ID == p.Source.ID {
			tool = &env.Data.Content[i]
		}
	}
	check(tool != nil && tool.State.Status == "running", "permission source is not a pending running tool")
	var input struct {
		Path, OldString, NewString, Command, Workdir, PatchText string
		Background                                              bool
	}
	must(json.Unmarshal(tool.State.Input, &input))
	allowed := map[string]bool{}
	switch tool.Name {
	case "patch":
		check(p.Action == "edit" && strings.TrimSpace(input.PatchText) == "*** Begin Patch\n*** Update File: result.txt\n@@\n-pending\n+OCQ-TOOLS-OK\n*** End Patch", "patch is not the exact harmless fixture edit")
		allowed["result.txt"] = true
		allowed[filepath.Join(s.job.Agent.Worktree, "result.txt")] = true
	case "read", "edit":
		path := input.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(s.job.Agent.Worktree, path)
		}
		check(filepath.Clean(path) == filepath.Join(s.job.Agent.Worktree, "result.txt"), "tool targets a nonfixture file")
		check(p.Action == tool.Name, "unexpected file permission action")
		if tool.Name == "edit" {
			check((input.OldString == "pending" || input.OldString == "pending\n") && input.NewString == strings.Replace(input.OldString, "pending", "OCQ-TOOLS-OK", 1), "unexpected edit content")
		}
		allowed["result.txt"] = true
		allowed[path] = true
	case "shell":
		check(p.Action == "shell" && (input.Workdir == "" || input.Workdir == s.job.Agent.Worktree), "unexpected shell action/cwd")
		check((input.Command == "./verify.sh" && !input.Background) || (input.Command == "./background.sh" && input.Background), "shell is not an exact harmless fixture command")
		allowed[input.Command] = true
		allowed[filepath.Join(s.job.Agent.Worktree, strings.TrimPrefix(input.Command, "./"))] = true
	default:
		panic("unapproved fixture tool: " + tool.Name)
	}
	check(len(p.Resources) > 0, "empty permission resources")
	for _, resource := range p.Resources {
		check(allowed[resource], "unexpected permission resource: "+resource)
	}
	intent := filepath.Join(s.root, "tools-approve-"+p.ID+".intent.json")
	_, err := os.Stat(intent)
	check(os.IsNotExist(err), "permission reply already attempted; never retry ambiguous POST")
	request, err := json.Marshal(p)
	must(err)
	save(filepath.Join(s.root, "tools-permission-request-"+p.ID+".json"), request)
	s.api("post", "/api/session/"+p.SessionID+"/permission/"+p.ID+"/reply", "tools-approve-"+p.ID, map[string]string{"decision": "once"})
	s.note("operator approved once exact fixture " + tool.Name + " request " + p.ID)
}

func (s *scenario) runTools() {
	_, err := os.Stat(filepath.Join(s.state, "queue.db"))
	check(os.IsNotExist(err), "tools state already exists; no prompt/mutation replay")
	must(os.Mkdir(s.repo, 0700))
	save(filepath.Join(s.repo, "result.txt"), []byte("pending\n"))
	save(filepath.Join(s.repo, ".gitignore"), []byte(".background-*\n.release-background\n"))
	save(filepath.Join(s.repo, "verify.sh"), []byte("#!/bin/sh\nset -eu\ntest \"$(cat result.txt)\" = OCQ-TOOLS-OK\nprintf 'OCQ-VERIFY-OK\\n'\n"))
	save(filepath.Join(s.repo, "background.sh"), []byte("#!/bin/sh\nset -eu\nprintf '%s\\n' \"$$\" > .background-pid\ni=0\nwhile test ! -e .release-background; do\n i=$((i+1))\n if test \"$i\" -ge 1200; then exit 7; fi\n sleep 0.1\ndone\nprintf 'OCQ-BACKGROUND-EXIT\\n'\n"))
	must(os.Chmod(filepath.Join(s.repo, "verify.sh"), 0700))
	must(os.Chmod(filepath.Join(s.repo, "background.sh"), 0700))
	_, err = command(s.repo, "git", "init", "--quiet")
	must(err)
	_, err = command(s.repo, "git", "add", "result.txt", "verify.sh", "background.sh", ".gitignore")
	must(err)
	_, err = command(s.repo, "git", "-c", "core.hooksPath=/dev/null", "-c", "user.name=ocq fixture", "-c", "user.email=ocq-fixture@example.invalid", "commit", "--quiet", "-m", "fixture")
	must(err)
	prompt := `Work only in this disposable fixture. Use actual tools, no subagents or unrelated actions.
1. Use read on result.txt.
2. Use patch with exactly this patchText (preserve the newline, no other edits):
*** Begin Patch
*** Update File: result.txt
@@
-pending
+OCQ-TOOLS-OK
*** End Patch
3. Use shell with command exactly ./verify.sh in the current worktree, foreground (background false). It must print OCQ-VERIFY-OK.
4. Use shell with command exactly ./background.sh in the current worktree, background true, timeout 120000. This is a bounded cooperative fixture; the operator releases it after observing your idle session. Do NOT wait, poll, kill it, or create the release file.
5. End your response after the background tool returns. Report the edit/verification. If later notified of background exit, acknowledge and finish. Do not invoke any other tools. Permission requests will be handled by an explicit fixture operator.`
	save(filepath.Join(s.root, "prompt.txt"), []byte(prompt))
	save(filepath.Join(s.root, "permissions.json"), []byte(`[{"action":"*","resource":"*","effect":"ask"}]`))
	b := s.cli("submit-agent", "--key", "real-tools-once", "--repo", s.repo, "--base", "HEAD", "--prompt-file", filepath.Join(s.root, "prompt.txt"), "--agent", "build", "--model", "openai/gpt-6.1-sol#low", "--permissions-file", filepath.Join(s.root, "permissions.json"), "--memory-bytes", "67108864")
	must(json.Unmarshal(b, &s.job))
	save(filepath.Join(s.root, "queued-job.json"), b)
	// Cleanup is fixture-only, including on assertion failure; never infer agent
	// exit from dispatcher exit. Cooperative release precedes any exact stop.
	defer s.cleanupTools()
	s.start()
	s.wait("single prompt acknowledgment", func() bool { j := s.status(); return j.Agent.Phase == "prompt_accepted" || j.Agent.Phase == "terminal" })
	s.stop(syscall.SIGTERM)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(s.state, "queue.db")+"?mode=ro&_pragma=busy_timeout(5000)")
	must(err)
	defer db.Close()
	held := func() int {
		var n int
		must(db.QueryRow("SELECT count(*) FROM jobs WHERE id=? AND state IN ('launching','running','unknown')", s.job.ID).Scan(&n))
		return n
	}
	approved := map[string]bool{}
	blocked, backgroundHeld, released := false, false, false
	var background identity
	end := time.Now().Add(180 * time.Second)
	for time.Now().Before(end) {
		for _, p := range s.toolPermissions() {
			check(!approved[p.ID], "already-answered request remains pending; no replay")
			b = s.cli("reconcile", s.job.ID)
			must(json.Unmarshal(b, &s.job))
			check(s.job.State == "unknown" && s.job.Outcome == "blocked" && held() == 1, "queue released slot while an actual tool permission was pending")
			save(filepath.Join(s.root, "tools-permission-held-"+p.ID+".json"), b)
			still := false
			for _, current := range s.toolPermissions() {
				if current.ID == p.ID {
					still = true
				}
			}
			check(still, "product answered tool permission")
			blocked = true
			s.approveFixture(p)
			approved[p.ID] = true
		}
		b = s.cli("reconcile", s.job.ID)
		must(json.Unmarshal(b, &s.job))
		for _, shell := range s.runningShells() {
			if shell.Status == "running" && !s.active() {
				check(s.job.State != "finished" && held() == 1, "outer tool/session idle incorrectly released running background shell")
				var messageEnv struct {
					Data []struct {
						Type    string
						Content []proofTool
					}
				}
				must(json.Unmarshal(s.api("get", "/api/session/"+s.job.Agent.Session+"/message?limit=100&order=asc", "tools-background-history", nil), &messageEnv))
				completeBackground := false
				for _, m := range messageEnv.Data {
					for _, tool := range m.Content {
						var meta struct{ ShellID string }
						_ = json.Unmarshal(tool.State.Metadata, &meta)
						if tool.Name == "shell" && tool.State.Status == "completed" && meta.ShellID == shell.ID {
							completeBackground = true
						}
					}
				}
				check(completeBackground, "background tool has not actually returned completed with exact shell ID")
				background = who(shell.PID)
				identityBytes, err := json.Marshal(background)
				must(err)
				save(filepath.Join(s.root, "background.identity"), identityBytes)
				save(filepath.Join(s.root, "tools-background-held-job.json"), b)
				s.note("PASS actual outer tool completed/session inactive while owned shell " + shell.ID + " still running; slot held")
				backgroundHeld = true
				save(filepath.Join(s.job.Agent.Worktree, ".release-background"), []byte("explicit fixture operator release\n"))
				released = true
			}
		}
		if s.job.State == "finished" {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	check(blocked && backgroundHeld && released, "actual permission/background scenario not exercised")
	check(!live(background), "owned background process identity remains after runtime terminal observation")
	s.verifyTools()
	s.note(fmt.Sprintf("PASS %d operator-once actual tool permissions and observed background hold/release", len(approved)))
}

func (s *scenario) verifyTools() {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(s.state, "queue.db")+"?mode=ro&_pragma=busy_timeout(5000)")
	must(err)
	defer db.Close()
	var held int
	must(db.QueryRow("SELECT count(*) FROM jobs WHERE id=? AND state IN ('launching','running','unknown')", s.job.ID).Scan(&held))
	check(s.job.State == "finished" && s.job.Outcome == "agent_succeeded" && s.job.Exit == nil && s.job.Validation == "not_run" && held == 0, "tool scenario did not finish with slot released and validation separate")
	var raw []byte
	var digest string
	must(db.QueryRow("SELECT canonical,digest FROM receipts WHERE job_id=?", s.job.ID).Scan(&raw, &digest))
	sum := sha256.Sum256(raw)
	check(digest == hex.EncodeToString(sum[:]) && digest == s.job.Digest, "tool receipt digest mismatch")
	result, err := os.ReadFile(filepath.Join(s.job.Agent.Worktree, "result.txt"))
	must(err)
	check(string(result) == "OCQ-TOOLS-OK\n", "real fixture edit is wrong")
	diff, err := command(s.job.Agent.Worktree, "git", "diff", "--no-ext-diff", "--", "result.txt")
	must(err)
	check(strings.Contains(string(diff), "-pending") && strings.Contains(string(diff), "+OCQ-TOOLS-OK"), "actual Git diff lacks expected edit")
	save(filepath.Join(s.root, "fixture.diff"), diff)
	changed, err := command(s.job.Agent.Worktree, "git", "diff", "--name-only")
	must(err)
	check(strings.TrimSpace(string(changed)) == "result.txt", "fixture scripts or unrelated files changed")
	proof, err := os.ReadFile(s.job.Agent.TerminalEvidence)
	must(err)
	var evidence struct {
		ToolCount      int `json:"tool_count"`
		Messages       []proofMessage
		ResourcesFinal struct {
			Shells []struct {
				ID, Status string
				Exit       *int
			}
		} `json:"resources_final"`
	}
	must(json.Unmarshal(proof, &evidence))
	messages := s.toolTranscript()
	check(len(messages) == len(evidence.Messages) && len(messages) > 0, "real API/evidence history mismatch")
	users, reads, edits, shells := 0, 0, 0, 0
	foregroundVerified, backgroundReturned := false, false
	backgroundID := ""
	var output strings.Builder
	for i, m := range messages {
		check(m.ID == evidence.Messages[i].ID, "API/evidence message order mismatch")
		if m.Type == "user" {
			users++
			check(m.ID == s.job.Agent.Prompt && m.Text == s.job.Request.Prompt, "foreign/changed prompt")
		}
		for _, tool := range m.Content {
			if m.Type == "assistant" && tool.Type == "text" {
				output.WriteString(tool.Text)
				output.WriteByte('\n')
			}
			if tool.Type != "tool" {
				continue
			}
			check(tool.State.Status == "completed" || tool.State.Status == "error", "nonterminal tool accepted")
			switch tool.Name {
			case "read":
				reads++
			case "edit", "patch":
				edits++
			case "shell":
				shells++
				var input struct {
					Command    string
					Background bool
				}
				var meta struct {
					ShellID, Status string
					Exit            *int
				}
				must(json.Unmarshal(tool.State.Input, &input))
				must(json.Unmarshal(tool.State.Metadata, &meta))
				if input.Command == "./verify.sh" && !input.Background {
					foregroundVerified = tool.State.Status == "completed" && meta.Exit != nil && *meta.Exit == 0 && strings.Contains(string(tool.State.Content), "OCQ-VERIFY-OK")
				}
				if input.Command == "./background.sh" && input.Background {
					backgroundReturned = tool.State.Status == "completed" && meta.Status == "running" && meta.ShellID != ""
					backgroundID = meta.ShellID
				}
			}
		}
	}
	check(users == 1 && reads >= 1 && edits == 1 && shells == 2 && evidence.ToolCount == reads+edits+shells, "real read/edit/foreground/background tools missing")
	last := messages[len(messages)-1]
	check(last.Type == "idle" && last.Outcome == "succeeded" && foregroundVerified && backgroundReturned, "actual foreground verification/background tool output or terminal outcome absent")
	backgroundExited := false
	for _, shell := range evidence.ResourcesFinal.Shells {
		if shell.ID == backgroundID && shell.Status == "exited" && shell.Exit != nil && *shell.Exit == 0 {
			backgroundExited = true
		}
	}
	check(backgroundExited, "exact background shell exit 0 missing from durable resource evidence")
	q := url.Values{"location[directory]": {s.job.Agent.Worktree}}
	var exact struct {
		Location struct{ Directory string }
		Data     struct {
			ID, Status string
			Exit       *int
			Metadata   struct{ SessionID string }
		}
	}
	must(json.Unmarshal(s.api("get", "/api/shell/"+backgroundID+"?"+q.Encode(), "tools-background-exit", nil), &exact))
	check(exact.Location.Directory == s.job.Agent.Worktree && exact.Data.ID == backgroundID && exact.Data.Metadata.SessionID == s.job.Agent.Session && exact.Data.Status == "exited" && exact.Data.Exit != nil && *exact.Data.Exit == 0, "independent exact runtime shell GET disagrees with terminal evidence")
	storedOutput, err := os.ReadFile(s.job.Stdout)
	must(err)
	check(output.Len() > 0 && string(storedOutput) == output.String(), "durable stdout is not actual assistant text only")
	check(len(s.runningShells()) == 0 && !s.active() && len(s.toolPermissions()) == 0, "owned runtime/permission still pending")
	for _, suffix := range []string{"inbox", "form"} {
		var pending struct{ Data []json.RawMessage }
		must(json.Unmarshal(s.api("get", "/api/session/"+s.job.Agent.Session+"/"+suffix, "tools-final-"+suffix, nil), &pending))
		check(pending.Data != nil && len(pending.Data) == 0, "pending inbox/form at completion")
	}
	var order string
	must(db.QueryRow("SELECT group_concat(kind,',') FROM (SELECT kind FROM agent_operations WHERE job_id=? ORDER BY rowid)", s.job.ID).Scan(&order))
	check(order == "worktree,session,prompt", "duplicate provisioning/prompt attempt")
	save(filepath.Join(s.root, "tools-final-job.json"), s.cli("status", s.job.ID))
	s.note("PASS independent API/DB/fixture assertions: exact file/diff, actual foreground exit/output, exact background runtime exit, one prompt, durable assistant-only stdout, empty runtime/queues, slot released, validation not_run")
}

func (s *scenario) cleanupTools() {
	if s.dispatcher != nil {
		s.stop(syscall.SIGTERM)
	}
	if s.job.Agent == nil || s.job.Agent.Worktree == "" {
		return
	}
	save(filepath.Join(s.job.Agent.Worktree, ".release-background"), []byte("fixture cleanup release\n"))
	for _, p := range s.toolPermissions() {
		check(p.SessionID == s.job.Agent.Session, "cleanup permission ownership mismatch")
		s.api("post", "/api/session/"+p.SessionID+"/permission/"+p.ID+"/reply", "tools-cleanup-reject-"+p.ID, map[string]string{"decision": "reject"})
	}
	if s.active() {
		s.api("post", "/api/session/"+s.job.Agent.Session+"/interrupt?resume=false", "tools-cleanup-interrupt", nil)
	}
	s.wait("owned runtime/session cleanup", func() bool { return len(s.runningShells()) == 0 && !s.active() })
	check(len(s.toolPermissions()) == 0, "owned permission remains pending")
	s.note("verified owned runtime shells absent/session inactive/permissions empty; all Git resources and evidence retained")
}
