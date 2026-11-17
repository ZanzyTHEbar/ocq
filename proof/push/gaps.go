package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/coder/websocket"
	"ocq/internal/core"
)

func storedProjection(state, project, root string) {
	// Explicitly read the original historical authority. Never copy its SQLite,
	// initialize it, install an observation hint, acquire ownership or execute.
	fingerprint := func() map[string]string {
		result := map[string]string{}
		must(filepath.WalkDir(state, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() {
				if p == filepath.Join(state, "worktrees") {
					return filepath.SkipDir
				}
				return nil
			}
			if !e.Type().IsRegular() || strings.HasSuffix(p, "-shm") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			result[strings.TrimPrefix(p, state+"/")] = fmt.Sprintf("%x", sha256.Sum256(b))
			return nil
		}))
		return result
	}
	before := fingerprint()
	ctx := context.Background()
	snapshot, err := core.ObserveSnapshot(ctx, state, core.ObservationRequest{Protocol: core.ObservationProtocol, Selectors: []core.ObservationSelector{{Slot: "board", Kind: "board", ProjectID: project}, {Slot: "results", Kind: "results", ProjectID: project, Cursor: "0"}, {Slot: "jobs", Kind: "queue-jobs", Cursor: "0"}}, Work: &core.WorkObservationCursor{ProjectID: project}})
	must(err)
	check(snapshot.Status == "ready" && snapshot.Work != nil && snapshot.Main != nil, "original authority populated work/Main snapshot")
	raw, err := json.Marshal(snapshot.Work)
	must(err)
	check(strings.Contains(string(raw), `"terminal_proven":true`) && strings.Contains(string(raw), `"goal-result"`), "real terminal-proven work/result chain")
	check(len(snapshot.Main.Items) > 0, "authentic stored Main intents")
	save(filepath.Join(root, "stored-snapshot.json"), snapshot)
	slots := []any{}
	for _, slot := range snapshot.Slots {
		slots = append(slots, map[string]any{"slot": slot.Slot, "kind": slot.Kind, "status": slot.Status, "data": slot.Data})
	}
	save(filepath.Join(root, "stored-projected-slots.json"), slots)
	handoff, err := core.ProjectWorkHandoff(state, project, "goal-result")
	must(err)
	save(filepath.Join(root, "stored-authentic-handoff.json"), handoff)
	review := handoff.Review.Job
	check(review != nil && review.TerminalProven, "exact genuine review target for read-only handoff guard")
	target := core.AgentReconcileTarget{CommandResolutionTarget: core.CommandResolutionTarget{Machine: "local", Space: "sp" + strings.Repeat("7", 32), Queue: snapshot.QueueIdentity, Job: review.JobID, Sequence: review.Sequence}, Session: review.SessionID, Worktree: review.Worktree}
	verified, err := core.InspectAgentHandoff(state, target, strings.Repeat("0", 64))
	must(err)
	check(verified.Observation == "service_unavailable", "unmatched explicit handoff service refuses without API/launch")
	save(filepath.Join(root, "stored-handoff-service-refusal.json"), verified)
	after := fingerprint()
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	check(string(a) == string(b), "historical receipts/database/journal bytes and file inventory unchanged")
	save(filepath.Join(root, "stored-original-files-before-after.json"), map[string]any{"before": before, "after": after})
	save(filepath.Join(root, "RESULT.json"), map[string]any{"status": "pass", "source": state, "project": project, "queue_identity": snapshot.QueueIdentity, "revision": snapshot.Revision, "work_items": len(snapshot.Work.Items), "main_items": len(snapshot.Main.Items), "client_items": len(snapshot.Clients), "original_files_unchanged": true, "read_only": true, "terminal_chain": "original genuine implementation/capture/review/review_result; no execution or mutation", "clients": "report exact stored count; populated client not inferred"})
	fmt.Println("PASS read-only genuine work/Main/terminal result projection; historical bytes unchanged:", root)
}

func byteAdapters(c *client, d map[string]any, pin []string, state, root, project string) {
	action := func(name string, payload *string, positional ...string) map[string]any {
		args := append([]string{name}, positional...)
		args = append(args, pin...)
		family, operation, _ := strings.Cut(name, "-")
		args = append(args, "--mod-action", family+"/"+operation+"/journal-v1")
		reply := c.callPayload(args, payload)
		check(reply["code"] == float64(0), "real private byte adapter "+name)
		var value map[string]any
		must(json.Unmarshal([]byte(reply["stdout"].(string)), &value))
		return value
	}
	reader, err := core.OpenReader(state)
	must(err)
	lanes, err := reader.Lanes(project)
	must(err)
	must(reader.Close())
	launch := strings.Repeat("5", 64)
	if len(lanes.Lanes) != 0 {
		check(len(lanes.Lanes) == 1, "exact admitted/cancelled acceptance lane")
		lane := lanes.Lanes[0]
		raw, err := json.Marshal(core.MailRequest{ProjectID: project, From: "lane:" + lane.LaneID, To: "integrator:" + project, LaneID: lane.LaneID, JobID: lane.OwnerJobID, Kind: "lane.blocked", Subject: "Socket byte acceptance — no agent execution", Body: "Exact retained UTF-8 bytes\nQuotes: \"review before arm\"; no background resend."})
		must(err)
		// Key is generated by the retained journal; never supplied in compose bytes.
		var mail map[string]any
		must(json.Unmarshal(raw, &mail))
		delete(mail, "Key")
		raw, err = json.Marshal(mail)
		must(err)
		text := string(raw)
		prepared := action("compose-receive", &text, launch)
		composeID := strings.TrimPrefix(prepared["response"].(string), "PREPARED ")
		retained := action("compose-preview", nil, composeID, launch)
		check(retained["response"] == text, "compose exact byte preview before arm")
		armed := action("compose-arm", nil, composeID)
		check(armed["state"] == "armed", "durable compose arm")
		completed := action("compose-dispatch", nil, composeID)
		check(completed["state"] == "completed", "compose exact single-attempt receipt")
		save(filepath.Join(root, "compose-byte-result.json"), map[string]any{"status": "pass", "preview_exact_bytes": true, "armed": armed, "completed": completed})
	} else {
		save(filepath.Join(root, "compose-byte-result.json"), map[string]any{"status": "not_run", "reason": "No owned lane in this no-agent-admission fixture. Positive compose requires a separately authorized real lane; never manufacture one or a terminal job."})
	}

	registeredBytes, err := os.ReadFile(filepath.Join(root, "project-registration.json"))
	must(err)
	var registered map[string]any
	must(json.Unmarshal(registeredBytes, &registered))
	zero := 0
	draft := core.ProjectRegistrationDraft{Mode: "new", Key: "socket-byte-registration", ProjectID: "proj-socket-byte-intake", Label: "Socket byte intake", ExpectedRevision: &zero, Repo: registered["repo_root"].(string), CommitAnchor: registered["commit_anchor"].(string), ElephantSessionID: registered["elephant_session_id"].(string), ExpectedOpenCodeProjectID: registered["opencode_project_id"].(string), ExpectedOpenCodeDirectory: registered["opencode_directory"].(string), AuthRef: "explicit-existing-session-read-only-verification", Intent: "Real socket registration intake; no session create, prompt, execution or result claim.", Decisions: []string{}, Risks: []string{}}
	raw, err := json.Marshal(draft)
	must(err)
	text := string(raw)
	built := action("intake-registration-draft", &text, launch)
	review := built["exact"].(map[string]any)
	exact := review["raw"].(string)
	received := action("intake-receive-text", &exact, "registration", launch)
	id := strings.TrimPrefix(received["response"].(string), "PREPARED ")
	preview := action("intake-review-preview", nil, id, launch)
	check(preview["exact"].(map[string]any)["raw"] == exact, "registration retained exact bytes before arm")
	intakeArmed := action("intake-arm", nil, id)
	check(intakeArmed["state"] == "armed", "durable registration intake arm")
	intakeCompleted := action("intake-dispatch", nil, id)
	check(intakeCompleted["state"] == "completed", "exact registration intake receipt")
	save(filepath.Join(root, "intake-byte-result.json"), map[string]any{"status": "pass", "draft_review_exact": true, "retained_review_exact": true, "armed": intakeArmed, "completed": intakeCompleted, "session_effects": "existing session read-only verification only; no prompt or execution"})
}

func mainJournal(ctx context.Context, c *client, d map[string]any, bridge, state, root, repo string) {
	project := "proj-socket-main-journal"
	id := c.write("subscribe", map[string]any{"protocol": "ocq-observe-v1", "queue_identity": d["queue_identity"], "store_context": d["observation_store_context"], "bridge_context": d["observation_context"], "selectors": []any{}, "work": map[string]any{"project_id": project, "fingerprint": ""}})
	before := c.read()
	for before["type"] == "snapshot" && before["id"] != id {
		before = c.read()
	}
	check(before["type"] == "snapshot", "Main journal-only subscription before creation")
	profile := filepath.Join(root, "main-profile.json")
	roleFile := filepath.Join(root, "main-role-selection.json")
	deny := `[{"action":"*","resource":"*","effect":"deny"}]`
	worker := `{"agent":"build","provider_id":"openai","model_id":"gpt-6.1-sol","variant":"default","memory_bytes":"0","permissions":` + deny + `}`
	raw, err := json.Marshal(core.MainProfile{Role: "integrator", Agent: "build", ProviderID: "openai", ModelID: "gpt-6.1-sol", Variant: "default", Permissions: json.RawMessage(deny)})
	must(err)
	must(os.WriteFile(profile, raw, 0600))
	raw, err = json.Marshal(core.MainRoleSelection{Protocol: core.MainRoleProtocol, ImplementationProfile: worker, ReviewProfile: worker})
	must(err)
	must(os.WriteFile(roleFile, raw, 0600))
	cli := strings.TrimSuffix(bridge, "ocq-herdr") + "ocq"
	writer := exec.CommandContext(ctx, cli, "--state-dir", state, "project", "main", "prepare", "--operation-id", "socket-main-prepared-only", "--mode", "create", "--repo", repo, "--main-branch", "refs/heads/main", "--expected-revision", "0", "--label", "Prepared only socket proof", "--profile-file", profile, "--role-file", roleFile, project)
	b, err := writer.CombinedOutput()
	must(os.WriteFile(filepath.Join(root, "main-journal-writer.json"), b, 0600))
	must(err)
	var after map[string]any
	for {
		after = c.read()
		check(after["type"] == "snapshot" && after["id"] == id, "Main update pushed without subscription renewal")
		b, _ := json.Marshal(after["snapshot"].(map[string]any)["main"])
		if strings.Contains(string(b), `"socket-main-prepared-only"`) {
			break
		}
	}
	a, z := before["snapshot"].(map[string]any), after["snapshot"].(map[string]any)
	check(a["revision"] == z["revision"], "Main filesystem projection separate from committed SQL revision")
	save(filepath.Join(root, "main-journal-push.json"), after)
	save(filepath.Join(root, "main-journal-result.json"), map[string]any{"status": "pass", "revision_before": a["revision"], "revision_after": z["revision"], "explicit_subscriptions": 1, "new_session_or_prompt": false, "prepared_main_operation": "socket-main-prepared-only", "clients": z["clients"], "client_preparation": "not attempted: no real selected owned session for this prepared-only Main; no fabricated client pointers"})
}

// One actual command lifecycle drives append notifications and the selected
// log-window boundary. Only the FIFO producer gate is coordinated by this proof;
// there is no recurring projection read, sleep, mocked writer or fabricated log.
func continuousLog(ctx context.Context, c *client, d map[string]any, bridge, state, root, gate, job string) {
	cursor := map[string]any{"job_id": job, "stream": "stdout", "offset": "0", "file_identity": "", "observed_size": "0", "modified_ns": "0"}
	request := func(cursor any) map[string]any {
		return map[string]any{"protocol": "ocq-observe-v1", "queue_identity": d["queue_identity"], "store_context": d["observation_store_context"], "bridge_context": d["observation_context"], "selectors": []any{map[string]any{"slot": "log-job", "kind": "queue-job", "job_id": job}}, "log": cursor}
	}
	id := c.write("subscribe", request(cursor))
	initial := c.read()
	for initial["type"] == "snapshot" && initial["id"] != id {
		initial = c.read()
	}
	check(initial["type"] == "snapshot" && initial["id"] == id, "one selected log subscription")
	save(filepath.Join(root, "log-before-dispatch.json"), initial)
	cli := strings.TrimSuffix(bridge, "ocq-herdr") + "ocq"
	log, err := os.OpenFile(filepath.Join(root, "command-dispatcher.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	must(err)
	defer log.Close()
	dispatcher := exec.CommandContext(ctx, cli, "--state-dir", state, "dispatch", "--commands-only", "--slots", "1", "--memory-floor-bytes", "0")
	dispatcher.Stdout, dispatcher.Stderr = log, log
	save(filepath.Join(root, "dispatcher-intent.json"), map[string]any{"argv": dispatcher.Args, "scope": "fresh command-only fixture; no agent jobs or old reservations"})
	must(dispatcher.Start())
	identity, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", dispatcher.Process.Pid))
	must(err) // Only the exact new child owned by this proof, never a system scan.
	fields := strings.Fields(string(identity)[strings.LastIndex(string(identity), ") ")+2:])
	save(filepath.Join(root, "dispatcher-owned.json"), map[string]any{"pid": dispatcher.Process.Pid, "start_ticks": fields[19], "owner_pid": os.Getpid()})
	stopped := false
	// On failure unblock only our finite command so graceful drain can settle.
	defer func() {
		if !stopped {
			f, _ := os.OpenFile(gate, os.O_WRONLY|syscall.O_NONBLOCK, 0)
			if f != nil {
				_, _ = f.WriteString("stop\n")
				_ = f.Close()
			}
			_ = dispatcher.Process.Signal(syscall.SIGTERM)
			_ = dispatcher.Wait()
		}
	}()
	frames := []any{initial}
	content := []byte{}
	gateReleased := false
	var last map[string]any
	for len(content) < 65536 {
		f := c.read()
		check(f["type"] == "snapshot" && f["id"] == id, "unsolicited log push on retained subscription")
		frames = append(frames, f)
		snapshot := f["snapshot"].(map[string]any)
		rawLog, ok := snapshot["log"].(map[string]any)
		if !ok || rawLog["status"] != "ready" {
			continue
		}
		last = rawLog
		check(rawLog["non_atomic_with_database"] == true, "log remains explicitly non-atomic")
		if encoded, ok := rawLog["bytes_base64"].(string); ok {
			b, err := base64.StdEncoding.DecodeString(encoded)
			must(err)
			content = append(content, b...)
		}
		if !gateReleased && strings.Contains(string(content), "first-log-chunk\n") {
			f, err := os.OpenFile(gate, os.O_WRONLY, 0)
			must(err)
			_, err = f.WriteString("continue\n")
			must(err)
			must(f.Close())
			gateReleased = true
		}
	}
	check(gateReleased && len(content) == 65536 && last["next_offset"] == "65536", "automatic log pushes stop at exact 64 KiB selected window")
	save(filepath.Join(root, "continuous-log-window.json"), frames)
	firstControlCount := c.n
	// Explicit user-selected next window, not a timer/automatic read renewal.
	id = c.write("subscribe", request(last["cursor"]))
	remainder := []byte{}
	finished := false
	for !finished || !strings.Contains(string(remainder), "\nlast-log-chunk\n") {
		f := c.read()
		if f["type"] == "snapshot" && f["id"] != id {
			continue // In-flight prior window, never apply it to the new selection.
		}
		check(f["type"] == "snapshot" && f["id"] == id, "explicit next log window")
		snapshot := f["snapshot"].(map[string]any)
		raw, _ := json.Marshal(snapshot["slots"])
		finished = strings.Contains(string(raw), `"state":"finished"`)
		if l, ok := snapshot["log"].(map[string]any); ok && l["status"] == "ready" {
			if b, ok := l["bytes_base64"].(string); ok {
				data, err := base64.StdEncoding.DecodeString(b)
				must(err)
				remainder = append(remainder, data...)
			}
		}
	}
	full := append(content, remainder...)
	check(string(full) == "first-log-chunk\n"+strings.Repeat("x", 70000)+"\nlast-log-chunk\n", "actual stdout reconstructed exactly across two explicit windows")
	check(c.n == firstControlCount+1, "no implicit projection reads during continuous output")
	must(dispatcher.Process.Signal(syscall.SIGTERM))
	must(dispatcher.Wait())
	stopped = true
	_, err = os.Stat(fmt.Sprintf("/proc/%d", dispatcher.Process.Pid))
	check(os.IsNotExist(err), "owned command dispatcher reaped")
	save(filepath.Join(root, "continuous-log-result.json"), map[string]any{"status": "pass", "job": job, "automatic_window_bytes": len(content), "total_exact_bytes": len(full), "explicit_subscriptions": 2, "additional_reads_or_renewals": 0, "job_finished": finished, "dispatcher_pid": dispatcher.Process.Pid, "dispatcher_reaped": true})
}

func staleControls(ctx context.Context, open func() *client, d map[string]any, folder, root string) {
	c := open()
	defer c.c.CloseNow()
	c.write("ping", nil)
	check(c.read()["type"] == "pong", "initial control admitted")
	frame, _ := json.Marshal(map[string]any{"type": "ping", "id": "1"})
	must(c.c.Write(ctx, websocket.MessageText, frame))
	_, _, err := c.c.Read(ctx)
	check(websocket.CloseStatus(err) == websocket.StatusPolicyViolation, "replayed/decreasing control ID fails closed")
	stale := open()
	defer stale.c.CloseNow()
	stale.write("subscribe", map[string]any{"protocol": "ocq-observe-v1", "queue_identity": strings.Repeat("0", 64), "store_context": d["observation_store_context"], "bridge_context": d["observation_context"], "selectors": []any{map[string]any{"slot": "queue", "kind": "queue-jobs", "cursor": "0"}}})
	_, _, err = stale.c.Read(ctx)
	check(websocket.CloseStatus(err) == websocket.StatusPolicyViolation, "stale queue subscription fails closed")
	save(filepath.Join(root, "stale-controls.json"), map[string]any{"status": "pass", "replayed_control": "policy_violation", "stale_subscription": "policy_violation", "scope": folder, "decreasing_SQL_revision_client_application": "separate actual native controller acceptance; not inferred from control IDs"})
}

func replaceQueue(ctx context.Context, open func() *client, d map[string]any, state, root string) {
	c := open()
	defer c.c.CloseNow()
	c.write("subscribe", map[string]any{"protocol": "ocq-observe-v1", "queue_identity": d["queue_identity"], "store_context": d["observation_store_context"], "bridge_context": d["observation_context"], "selectors": []any{map[string]any{"slot": "queue", "kind": "queue-jobs", "cursor": "0"}}})
	before := c.read()
	check(before["type"] == "snapshot", "identity fixture initial snapshot")
	oldRevision := before["snapshot"].(map[string]any)["revision"].(string)
	replacement := filepath.Join(root, "replacement-queue")
	q, err := core.OpenQueue(replacement)
	must(err)
	must(q.Close())
	oldInfo, err := os.Stat(filepath.Join(state, "queue.db"))
	must(err)
	// Only this freshly created fixture: preserve the old entire queue instead
	// of mutating a live SQLite inode/WAL pair. Rename atomically changes the
	// selected path's database identity and triggers the old watch's MOVE_SELF.
	retired := filepath.Join(root, "retired-original-queue")
	must(os.Rename(state, retired))
	must(os.Rename(replacement, state))
	newInfo, err := os.Stat(filepath.Join(state, "queue.db"))
	must(err)
	check(!os.SameFile(oldInfo, newInfo), "actual database inode replaced")
	_, _, err = c.c.Read(ctx)
	check(err != nil && websocket.CloseStatus(err) == websocket.StatusPolicyViolation, "replaced queue closes retained subscription, never adopts new store")
	fresh := open()
	defer fresh.c.CloseNow()
	next := fresh.welcome["descriptor"].(map[string]any)
	check(next["queue_identity"] != d["queue_identity"], "explicit reconnect obtains new queue pin")
	replacementSnapshot, err := core.ObserveSnapshot(ctx, state, core.ObservationRequest{Protocol: core.ObservationProtocol, Selectors: []core.ObservationSelector{{Slot: "replacement", Kind: "queue-jobs", Cursor: "0"}}})
	must(err)
	check(replacementSnapshot.Status == "ready", "replacement queue observed without initialization")
	oldVersion, err := strconv.ParseInt(oldRevision, 10, 64)
	must(err)
	newVersion, err := strconv.ParseInt(replacementSnapshot.Revision, 10, 64)
	must(err)
	check(newVersion < oldVersion, "actual replacement carries a lower committed revision")
	fresh.write("subscribe", map[string]any{"protocol": "ocq-observe-v1", "queue_identity": d["queue_identity"], "store_context": d["observation_store_context"], "bridge_context": d["observation_context"], "selectors": []any{map[string]any{"slot": "queue", "kind": "queue-jobs", "cursor": "0"}}})
	_, _, err = fresh.c.Read(ctx)
	check(websocket.CloseStatus(err) == websocket.StatusPolicyViolation, "old pins cannot regain authority after reconnect")
	save(filepath.Join(root, "queue-replacement.json"), map[string]any{"status": "pass", "old_identity": d["queue_identity"], "new_identity": next["queue_identity"], "old_revision": oldRevision, "replacement_revision": replacementSnapshot.Revision, "old_queue_preserved_at": retired, "same_database_inode": false, "old_subscription": "closed", "old_pins_after_explicit_reconnect": "refused", "scope": "fresh fixture only; no original historical queue touched"})
}
