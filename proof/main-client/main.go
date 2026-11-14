// Harmless real CLI/Git/SQLite/package/config acceptance. No Main/API mutation,
// HC command, native TUI/GUI, product worker, or installed-file change.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"ocq/internal/core"
)

func main() {
	bin := flag.String("ocq", "", "built CLI")
	asset := flag.String("asset", "", "reviewed tui.js to stage in private release")
	root := flag.String("root", "", "fresh protected durable acceptance directory")
	flag.Parse()
	if *bin == "" || *asset == "" || !filepath.IsAbs(*root) {
		panic("explicit --ocq --asset --root required")
	}
	if _, e := os.Lstat(*root); !os.IsNotExist(e) {
		panic("fresh acceptance root required")
	}
	must(os.Mkdir(*root, 0700))
	counter := 0
	save := func(name string, value any) {
		b, e := json.MarshalIndent(value, "", "  ")
		must(e)
		must(os.WriteFile(filepath.Join(*root, name), b, 0600))
	}
	run := func(cwd, program string, args ...string) ([]byte, error) {
		if program == "git" {
			args = append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "commit.gpgsign=false", "-c", "init.templateDir="}, args...)
		}
		counter++
		save(fmt.Sprintf("%02d-intent.json", counter), map[string]any{"cwd": cwd, "program": program, "argv": args, "scope": "owned foreground harmless acceptance"})
		cmd := exec.Command(program, args...)
		cmd.Dir = cwd
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		err := cmd.Start()
		if err != nil {
			return nil, err
		}
		stat, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", cmd.Process.Pid))
		must(e)
		fields := strings.Fields(string(stat)[strings.LastIndex(string(stat), ") ")+2:])
		save(fmt.Sprintf("%02d-birth.json", counter), map[string]any{"pid": cmd.Process.Pid, "start_ticks": fields[19]})
		err = cmd.Wait()
		save(fmt.Sprintf("%02d-result.json", counter), map[string]any{"pid": cmd.Process.Pid, "reaped": true, "exit": cmd.ProcessState.ExitCode(), "output": out.String()})
		return out.Bytes(), err
	}
	repo := filepath.Join(*root, "primary")
	must(os.Mkdir(repo, 0700))
	_, e := run(repo, "git", "init", "-b", "feature")
	must(e)
	must(os.WriteFile(filepath.Join(repo, "README.md"), []byte("owned harmless fixture\n"), 0600))
	_, e = run(repo, "git", "add", "README.md")
	must(e)
	_, e = run(repo, "git", "-c", "user.name=OCQ fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "fixture")
	must(e)
	dirty := filepath.Join(repo, "untracked.txt")
	original := []byte("preserve this dirty primary checkout\n")
	must(os.WriteFile(dirty, original, 0600))
	before, e := run(repo, "git", "status", "--porcelain=v1")
	must(e)
	state := filepath.Join(*root, "queue")
	_, e = run(repo, *bin, "--state-dir", state, "list")
	must(e)
	profile := filepath.Join(*root, "main-profile.json")
	must(os.WriteFile(profile, []byte(`{"role":"integrator","agent":"build","provider_id":"openai","model_id":"gpt-6.1-sol","variant":"","permissions":[{"action":"*","resource":"*","effect":"ask"}]}`), 0600))
	roleFile := filepath.Join(*root, "role-selection.json")
	fixtureProfile := `{"agent":"fixture-explicit","provider_id":"fixture","model_id":"fixture","variant":"","memory_bytes":"1048576","permissions":[{"action":"*","resource":"*","effect":"ask"}]}`
	roleBytes, e := json.Marshal(core.MainRoleSelection{Protocol: core.MainRoleProtocol, ImplementationProfile: fixtureProfile, ReviewProfile: fixtureProfile})
	must(e)
	must(os.WriteFile(roleFile, roleBytes, 0600))
	prompt := filepath.Join(*root, "prompt.txt")
	must(os.WriteFile(prompt, []byte("Private Main prompt stays in a file, never command argv."), 0600))
	b, e := run(repo, *bin, "--state-dir", state, "project", "main", "prepare", "--operation-id", "wrong-branch", "--mode", "create", "--repo", repo, "--main-branch", "refs/heads/main", "--expected-revision", "0", "--label", "Fixture", "--profile-file", profile, "--role-file", roleFile, "--prompt-file", prompt, "proj-main-fixture")
	check(e != nil && strings.Contains(string(b), "another branch"), "wrong canonical branch must fail before any Main session creation")
	after, e := run(repo, "git", "status", "--porcelain=v1")
	must(e)
	current, e := os.ReadFile(dirty)
	must(e)
	branch, e := run(repo, "git", "symbolic-ref", "HEAD")
	must(e)
	check(bytes.Equal(before, after) && bytes.Equal(current, original) && strings.TrimSpace(string(branch)) == "refs/heads/feature", "dirty branch and files preserved; no checkout/stash/reset")
	jobs, e := run(repo, *bin, "--state-dir", state, "list")
	must(e)
	check(strings.TrimSpace(string(jobs)) == "[]", "Main guard created no queued job/capacity claim")
	release := filepath.Join(*root, "release")
	must(os.MkdirAll(filepath.Join(release, "bin"), 0700))
	assetDir := filepath.Join(release, "share", "opencode", "ocq-handoff")
	must(os.MkdirAll(assetDir, 0700))
	image, e := os.ReadFile(*bin)
	must(e)
	cli := filepath.Join(release, "bin", "ocq")
	must(os.WriteFile(cli, image, 0700))
	plugin, e := os.ReadFile(*asset)
	must(e)
	must(os.WriteFile(filepath.Join(assetDir, "tui.js"), plugin, 0500))
	alias := filepath.Join(*root, "alias")
	must(os.Symlink(cli, alias))
	b, e = run(repo, alias, "project", "client", "check-package")
	must(e)
	var packaging core.NativeClientPackage
	must(json.Unmarshal(b, &packaging))
	check(packaging.Launcher == cli && packaging.Asset == assetDir && len(packaging.AssetSHA256) == 64, "canonical symlink release and packaged asset resolved exactly")
	must(os.Rename(filepath.Join(assetDir, "tui.js"), filepath.Join(*root, "retained-tui.js")))
	_, e = run(repo, alias, "project", "client", "check-package")
	check(e != nil, "missing packaged asset fails; no source/host fallback")
	must(os.Rename(filepath.Join(*root, "retained-tui.js"), filepath.Join(assetDir, "tui.js")))
	config := filepath.Join(*root, "config")
	must(os.Mkdir(config, 0700))
	base := []byte("{// keep comment and literal URL\n\"plugins\":[{\"package\":\"base-plugin\",\"options\":{\"url\":\"https://example.invalid/*literal*/\"}},],\"session\":{\"permissions\":\"autoaccept\",\"fixture_base\":true},}\n")
	configFile := filepath.Join(config, "cli.json")
	must(os.WriteFile(configFile, base, 0600))
	options := core.ClientOptions{Protocol: "ocq-handoff-v1", TargetKind: "integrator", ProjectID: "proj-main-fixture", MainID: "setup", OwnershipMode: "legacy_adoption", QueueIdentity: strings.Repeat("a", 64), SessionID: "ses_ocqmain_fixture", Worktree: repo, OpenCodeProjectID: "fixture", OwnershipSHA256: strings.Repeat("b", 64), ProfileSHA256: strings.Repeat("c", 64)}
	overlay, e := core.BuildNativeClientOverlay(config, `{"plugins":["inline-plugin",],"theme":{"name":"fixture"},"session":{"permissions":"autoaccept","fixture_inline":true},}`, assetDir, options)
	must(e)
	var merged map[string]any
	must(json.Unmarshal([]byte(overlay), &merged))
	plugins := merged["plugins"].([]any)
	check(len(plugins) == 2 && plugins[0] == "inline-plugin" && merged["theme"].(map[string]any)["name"] == "fixture", "effective inline plugin array preserved and handoff appended, not replaced")
	sessionConfig := merged["session"].(map[string]any)
	check(sessionConfig["permissions"] == "prompt" && sessionConfig["fixture_base"] == true && sessionConfig["fixture_inline"] == true, "prompt-only client override defeats inherited autoaccept while preserving other session fields")
	overlay, e = core.BuildNativeClientOverlay(config, "", assetDir, options)
	must(e)
	must(json.Unmarshal([]byte(overlay), &merged))
	plugins = merged["plugins"].([]any)
	check(plugins[0].(map[string]any)["options"].(map[string]any)["url"] == "https://example.invalid/*literal*/", "base JSONC plugin/options preserved")
	current, e = os.ReadFile(configFile)
	must(e)
	check(bytes.Equal(base, current), "no config file rewrite")
	missing := filepath.Join(*root, "never-initialized-wait")
	_, e = run(repo, *bin, "--state-dir", missing, "project", "work", "wait", "proj-main-fixture", "absent", "--timeout-seconds", "1")
	check(e != nil, "wait on unavailable store must fail, not fabricate pending/completion")
	_, e = os.Lstat(missing)
	check(os.IsNotExist(e), "read-only wait must not initialize queue or journal")
	proveRetainedClientReceipt(*root, state, repo, *bin)
	proveSupersededMainRefusal(*root, state, repo, *bin)
	save("outcome.json", map[string]any{"status": "passed", "scope": "real canonical-branch rejection; dirty work preserved; no job; trusted release/symlink packaging; effective JSONC plugin preservation; no Main/API mutation/HC/TUI/GUI launch", "all_children": "terminal and reaped"})
	fmt.Println("PASS harmless Main/client source acceptance; all children reaped. Artifacts:", *root)
}

// Simulate the retained preparation/crash boundary with private journal fixtures.
// Wrong-branch refusal bounds all prepare calls before ANY native discovery.
func proveSupersededMainRefusal(root, state, repo, bin string) {
	hash := func(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
	project, oldID, newID := "proj-main-fixture", "old-prepared", "explicit-successor"
	profile, e := os.ReadFile(filepath.Join(root, "main-profile.json"))
	must(e)
	selection, e := os.ReadFile(filepath.Join(root, "role-selection.json"))
	must(e)
	old := core.MainPrepareRequest{Protocol: core.MainProtocol, ProjectID: project, OperationID: oldID, Mode: "create", Repo: repo, MainBranch: "refs/heads/main", ExpectedRevision: "0", Label: "fixture", Profile: string(profile), Role: string(selection)}
	raw, e := json.Marshal(old)
	must(e)
	digest := hash(raw)
	role := core.MainRoleContract{Protocol: core.MainRoleProtocol, Key: "ocq-main-role-v1", ProjectID: project, SetupID: oldID, SessionID: "ses_fixture_never_created", StateDir: state, Repo: repo, GitCommonDir: filepath.Join(repo, ".git"), MainBranch: old.MainBranch, CLI: bin, Instructions: "Private retained fixture, not an installed instruction entry."}
	must(json.Unmarshal(selection, &role.Selection))
	b, e := json.Marshal(role)
	must(e)
	role.SHA256 = hash(b)
	info, e := os.Stat(filepath.Join(state, "queue.db"))
	must(e)
	st := info.Sys().(*syscall.Stat_t)
	record := map[string]any{"protocol": core.MainProtocol, "input_json": string(raw), "input_sha256": digest, "queue_path": state, "queue_device": fmt.Sprint(st.Dev), "queue_inode": fmt.Sprint(st.Ino), "setup_id": oldID, "repo": repo, "git_common_dir": role.GitCommonDir, "main_branch": old.MainBranch, "head": strings.Repeat("a", 40), "session_id": role.SessionID, "metadata": map[string]any{}, "profile_sha256": strings.Repeat("b", 64), "role": role}
	journal := filepath.Join(state, "project-work-"+hash([]byte(project)))
	put := func(name string, value any) {
		b, e := json.Marshal(value)
		must(e)
		must(os.WriteFile(filepath.Join(journal, ".main-"+oldID+"."+name+".json"), b, 0600))
	}
	put("intent", record)
	next := old
	next.OperationID = newID
	newRaw, e := json.Marshal(next)
	must(e)
	_, e = core.PrepareProjectMain(state, newRaw)
	check(e != nil && strings.Contains(e.Error(), "another branch"), "distinct create passes the unattempted predecessor gate, then safely refuses wrong branch before native discovery")
	put("session.armed", map[string]any{"fixture": "never executed"})
	_, e = core.PrepareProjectMain(state, newRaw)
	check(e != nil && strings.Contains(e.Error(), "stage evidence"), "even armed setup forbids replacement")
	must(os.Remove(filepath.Join(journal, ".main-"+oldID+".session.armed.json")))
	put("superseded", map[string]any{"version": 1, "intent_sha256": digest, "successor_operation_id": newID, "successor_input_sha256": hash(newRaw)})
	status, e := core.InspectProjectMain(state, project, oldID)
	must(e)
	check(status.State == "superseded", "superseded intent remains discoverable with permanent refusal state")
	_, e = core.StartProjectMain(state, project, oldID, strings.Repeat("0", 64), core.MainActionContext{})
	check(e != nil && strings.Contains(e.Error(), "superseded"), "old intent cannot start, even before any preview/context/API checks")
	_, e = core.PrepareProjectMain(state, newRaw)
	check(e != nil && strings.Contains(e.Error(), "another branch"), "interrupted supersession accepts only exact successor recovery up to safe branch guard")
	other := next
	other.OperationID = "unrelated-operation"
	otherRaw, e := json.Marshal(other)
	must(e)
	_, e = core.PrepareProjectMain(state, otherRaw)
	check(e != nil && strings.Contains(e.Error(), "exact successor"), "interrupted supersession cannot be adopted by another operation")
	b, e = json.Marshal(map[string]any{"status": "passed", "fixture": "retained preparation/crash boundary only; no target/API/HC", "old_start_refused": true, "armed_replacement_refused": true, "exact_successor_recovery_only": true})
	must(e)
	must(os.WriteFile(filepath.Join(root, "supersession-refusal.json"), b, 0600))
}

// One retained-journal acceptance fixture, NOT an actual target/HC receipt or
// execution proof. Exercise the real core/SQLite/file recovery API with a stale
// original binding; no session discovery, target API or HC process is involved.
func proveRetainedClientReceipt(root, state, repo, bin string) {
	hash := func(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
	r, e := core.OpenReader(state)
	must(e)
	queue, store, e := r.WorkQueueContext()
	must(e)
	must(r.Close())
	project := "proj-main-fixture"
	p := core.ClientPointer{Protocol: core.ClientProtocol, ProjectID: project, NavigationID: "retained-fixture", TargetKind: "integrator", Space: "fixture", QueueIdentity: queue, StoreContext: store, SessionID: "ses_fixture_not_created", Cwd: repo, BindingRevision: "1", BindingSHA256: strings.Repeat("d", 64), LauncherArgv: []string{bin, "--state-dir", state, "project", "client", "run", project, ""}}
	b, e := json.Marshal(p)
	must(e)
	p.PointerSHA256 = hash(b)
	p.LauncherArgv[7] = p.PointerSHA256
	c := core.ClientActionContext{Protocol: core.ClientProtocol, Action: "client-open", Peer: "local", Space: p.Space, Folder: repo, QueueIdentity: queue, StoreContext: store, ImageRole: "cli", ImageSHA256: strings.Repeat("e", 64)}
	journal := filepath.Join(state, "project-work-"+hash([]byte(project)))
	put := func(suffix string, value any) {
		raw, e := json.Marshal(value)
		must(e)
		must(os.WriteFile(filepath.Join(journal, ".client-"+p.PointerSHA256+"."+suffix+".json"), raw, 0600))
	}
	put("pointer", p)
	claim := map[string]any{"intent_sha256": p.PointerSHA256, "payload_sha256": p.PointerSHA256, "preview_sha256": p.PointerSHA256, "context": c}
	put("armed", claim)
	put("attempt", claim)
	receipt := core.ClientTerminalReceipt{ReturnedTerminalID: "t1", ObservedTerminalID: "t1", Peer: "local", Cwd: repo, Argv: p.LauncherArgv, Labels: map[string]string{"ocq.handoff": p.PointerSHA256}}
	recorded, e := core.RecordProjectClient(state, project, p.PointerSHA256, c, receipt)
	check(e != nil && strings.Contains(e.Error(), "HC receipt retained") && recorded.State == "terminal_recorded", "stale continuation refused only AFTER exact receipt retained")
	recovered, e := core.InspectProjectClient(state, project, p.PointerSHA256)
	must(e)
	check(recovered.Terminal != nil && recovered.Terminal.ReturnedTerminalID == "t1" && !recovered.AttemptPermitted, "stored receipt recovers despite stale/unavailable selected binding, without another launch")
	_, e = core.AttemptProjectClient(state, project, p.PointerSHA256, c)
	check(e != nil, "stale consumed receipt cannot authorize another attempt")
	_, e = core.VerifyProjectClient(state, project, p.PointerSHA256, c)
	check(e != nil, "stale receipt cannot authorize focus")
	different := receipt
	different.ReturnedTerminalID = "t2"
	different.ObservedTerminalID = "t2"
	_, e = core.RecordProjectClient(state, project, p.PointerSHA256, c, different)
	check(e != nil && strings.Contains(e.Error(), "immutable"), "different terminal receipt cannot overwrite original")
	b, e = json.Marshal(map[string]any{"status": "passed", "fixture": "retained journal only; no actual HC/target", "receipt_persisted_before_stale_continuation_error": true, "no_launch_or_focus_authorization": true})
	must(e)
	must(os.WriteFile(filepath.Join(root, "receipt-recovery.json"), b, 0600))
}
func must(e error) {
	if e != nil {
		panic(e)
	}
}
func check(ok bool, message string) {
	if !ok {
		panic(message)
	}
}
