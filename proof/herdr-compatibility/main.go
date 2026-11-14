// Opt-in isolated native/real bridge compatibility acceptance. No agents, API
// sessions or remote execution. Every launched runtime belongs to cleanup-owner.
package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"ocq/internal/core"
)

//go:embed native-driver.js
var driver []byte

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func check(b bool, s string) {
	if !b {
		panic(s)
	}
}
func save(p string, v any) { b, e := json.Marshal(v); must(e); must(os.WriteFile(p, b, 0600)) }
func load(p string, v any) { b, e := os.ReadFile(p); must(e); must(json.Unmarshal(b, v)) }
func exists(p string) bool { _, e := os.Stat(p); return e == nil }
func wait(s string, f func() bool) {
	end := time.Now().Add(30 * time.Second)
	for !f() {
		check(time.Now().Before(end), s)
		time.Sleep(40 * time.Millisecond)
	}
}
func run(bin string, a ...string) []byte {
	c := exec.Command(bin, a...)
	out, stderr := &commandOutput{limit: 8 << 20}, &commandOutput{limit: 8192}
	c.Stdout, c.Stderr = out, stderr
	e := c.Run()
	if e != nil {
		path := retainCommandFailure(c, bin, a, out, stderr, e)
		panic(fmt.Sprintf("command failed (%v); retained diagnostic: %s", e, path))
	}
	check(!out.truncated, "bounded command output exceeded")
	return out.bytes
}

func proxy(root string) {
	a := os.Args[1:]
	check(len(a) > 0, "action required")
	modeBytes, e := os.ReadFile(filepath.Join(root, "mode"))
	must(e)
	mode := strings.TrimSpace(string(modeBytes))
	f, e := os.OpenFile(filepath.Join(root, "bridge.trace"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	must(e)
	must(json.NewEncoder(f).Encode(map[string]string{"mode": mode, "action": a[0]}))
	must(f.Close())
	if (a[0] == "compatibility" && mode == "missing") || (a[0] == "queue-job" && mode == "missing-read") {
		os.Exit(127)
	}
	bin := filepath.Join(root, "bin", "bridge-real")
	if mode == "old" {
		bin = os.Getenv("OCQ_COMPAT_OLD_BRIDGE")
		check(filepath.IsAbs(bin), "exact old bridge required")
	}
	if mode == "drift" && a[0] != "compatibility" {
		bin = filepath.Join(root, "bin", "bridge-drift")
	}
	c := exec.Command(bin, a...)
	c.Stderr = os.Stderr
	b, e := c.Output()
	if e != nil {
		if x, ok := e.(*exec.ExitError); ok {
			os.Exit(x.ExitCode())
		}
		panic(e)
	}
	if a[0] == "dispatcher-attempt" && mode == "native-incompatible" {
		// The durable attempt succeeded under a matched bridge. Its following
		// native-launch check must still refuse a newly unavailable endpoint.
		must(os.WriteFile(filepath.Join(root, "mode"), []byte("missing"), 0600))
	}
	if a[0] == "compatibility" {
		if mode == "malformed" {
			fmt.Print("{")
			return
		}
		if mode == "mismatch" || mode == "action-mismatch" {
			var out map[string]any
			must(json.Unmarshal(b, &out))
			if mode == "mismatch" {
				out["protocol"] = "unknown-release"
			} else {
				out["actions"].(map[string]any)["compose-arm"] = "different-claim-semantics"
			}
			b, e = json.Marshal(out)
			must(e)
		}
		if mode == "timeout" {
			time.Sleep(6 * time.Second)
		}
		if exists(filepath.Join(root, "handshake.hold")) {
			must(os.WriteFile(filepath.Join(root, "handshake.entered"), nil, 0600))
			end := time.Now().Add(4 * time.Second)
			for !exists(filepath.Join(root, "handshake.release")) {
				check(time.Now().Before(end), "bounded handshake gate")
				time.Sleep(20 * time.Millisecond)
			}
		}
	}
	_, e = os.Stdout.Write(b)
	must(e)
	if a[0] == "compatibility" && exists(filepath.Join(root, "handshake.hold")) {
		must(os.WriteFile(filepath.Join(root, "handshake.returned"), nil, 0600))
	}
}

func install(root, source string) {
	dir := filepath.Join(root, "apphome", "mods", "ocq")
	must(os.MkdirAll(dir, 0700))
	files, e := os.ReadDir(source)
	must(e)
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".js") && f.Name() != "mod.json" {
			continue
		}
		b, e := os.ReadFile(filepath.Join(source, f.Name()))
		must(e)
		s := string(b)
		if (exists(filepath.Join(root, "corrections-only")) || exists(filepath.Join(root, "peer-transfer"))) && strings.Contains(s, `if (!current()) throw new Error("Transfer authorization changed");`) {
			s = strings.ReplaceAll(s, "let socket = null;", "let socket = null; let transferStep = 0;")
			s = strings.ReplaceAll(s, `if (!current()) throw new Error("Transfer authorization changed");`, `await transferCheckpoint("`+f.Name()+`", ++transferStep); if (!current()) throw new Error("Transfer authorization changed");`)
			s = `import { transferCheckpoint } from "./native-driver.js";` + "\n" + s
		}
		if f.Name() == "command-dispatcher.js" && exists(filepath.Join(root, "peer-transfer")) {
			parts := strings.SplitN(s, "confirmStart(owner, cx) {", 2)
			check(len(parts) == 2, "peer native start boundary changed")
			header := "cx.spawn(async cx => {\n      try {"
			check(strings.Count(parts[1], header) >= 1, "peer start callback boundary changed")
			parts[1] = strings.Replace(parts[1], header, header+" await peerStartCheckpoint();", 1)
			cleanup := "finally { this.working = false; if (this.operationCurrent(owner, operation)) { this.confirm = null; cx.notify(); } }"
			check(strings.Count(parts[1], cleanup) == 1, "peer start cleanup changed")
			parts[1] = strings.Replace(parts[1], cleanup, strings.TrimSuffix(cleanup, "}")+" await peerStartSettled(); }", 1)
			s = `import { peerStartCheckpoint, peerStartSettled } from "./native-driver.js";` + "\n" + strings.Join(parts, "confirmStart(owner, cx) {")
		}
		if f.Name() == "main.js" {
			needle := `if (await nextCommand() === "focus") this.handle.focus();`
			check(strings.Count(s, needle) == 1, "native command boundary changed")
			s = `import { nativeAction } from "./native-driver.js";` + "\n" + strings.Replace(s, needle, `const command = await nextCommand(); if (command === "focus") this.handle.focus(); else if (command.startsWith("compat-")) await nativeAction(this, command, cx);`, 1)
		}
		must(os.WriteFile(filepath.Join(dir, f.Name()), []byte(s), 0600))
	}
	must(os.WriteFile(filepath.Join(dir, "native-driver.js"), driver, 0600))
}

func main() {
	root := os.Getenv("PROBE_ROOT")
	if len(os.Args) == 3 && os.Args[1] == "diagnostic-check" {
		verifyParserFailureCapture(root, os.Args[2])
		return
	}
	check(filepath.IsAbs(root), "explicit private root required")
	if len(os.Args) > 1 && os.Args[1] == "transport-observe" {
		check(len(os.Args) == 5, "transport-observe HC GUARD DISPATCHER_PROOF")
		transportObserve(root, os.Args[2], os.Args[3], os.Args[4])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "transport-relay" {
		check(len(os.Args) == 4, "transport-relay HC GUARD")
		transportRelay(root, os.Args[2], os.Args[3])
		return
	}
	if filepath.Base(os.Args[0]) == "ocq-herdr" {
		proxy(root)
		return
	}
	if filepath.Base(os.Args[0]) == "ocq-compat-gate" {
		switch os.Args[1] {
		case "target":
			b, e := os.ReadFile(filepath.Join(root, "target.json"))
			must(e)
			_, e = os.Stdout.Write(b)
			must(e)
		case "peer-target":
			b, e := os.ReadFile(filepath.Join(root, "peer-target.json"))
			must(e)
			_, e = os.Stdout.Write(b)
			must(e)
		case "peer-descriptor":
			must(os.WriteFile(filepath.Join(root, "peer-descriptor.json"), []byte(os.Args[2]), 0600))
		case "inherited-transport":
			inheritedTransport(root)
		case "peer-start":
			if !exists(filepath.Join(root, "peer-start.hold")) {
				return
			}
			must(os.WriteFile(filepath.Join(root, "peer-start.entered"), nil, 0600))
			wait("peer start not released", func() bool { return exists(filepath.Join(root, "peer-start.release")) })
		case "peer-start-settled":
			if !exists(filepath.Join(root, "peer-start.hold")) {
				return
			}
			must(os.WriteFile(filepath.Join(root, "peer-start.settled"), nil, 0600))
		case "snapshot":
			must(os.WriteFile(filepath.Join(root, "snapshot.json"), []byte(os.Args[2]), 0600))
		case "delay-settled":
			must(os.WriteFile(filepath.Join(root, "handshake.settled"), nil, 0600))
		case "transfer-config":
			b, e := os.ReadFile(filepath.Join(root, "transfer-config.json"))
			must(e)
			_, e = os.Stdout.Write(b)
			must(e)
		case "transfer-checkpoint":
			var v struct{ File, Step string }
			load(filepath.Join(root, "transfer-config.json"), &v)
			if v.File == os.Args[2] && v.Step == os.Args[3] {
				must(os.WriteFile(filepath.Join(root, "transfer.waiting"), nil, 0600))
				wait("transfer gate not released", func() bool { return exists(filepath.Join(root, "transfer.release")) })
			}
		case "transfer-done":
			must(os.WriteFile(filepath.Join(root, "transfer-done.json"), []byte(os.Args[2]), 0600))
		default:
			panic("unknown gate")
		}
		return
	}
	check(len(os.Args) == 3, "usage: compatibility-proof install SOURCE | run APP")
	if os.Args[1] == "install" {
		install(root, os.Args[2])
		return
	}
	if os.Args[1] == "defaults" {
		defaultPaths(root, os.Args[2])
		return
	}
	check(os.Args[1] == "run", "unknown proof mode")
	app := os.Args[2]
	bridge := filepath.Join(root, "bin", "bridge-real")
	// An absent queue stays absent after a real endpoint request.
	missing := filepath.Join(root, "never-initialized")
	c := exec.Command(bridge, "compatibility", "sp-missing", root)
	c.Env = append(os.Environ(), "OCQ_STATE_DIR="+missing)
	b, e := c.Output()
	must(e)
	check(!exists(missing), "compatibility initialized a queue")
	var blank map[string]any
	must(json.Unmarshal(b, &blank))
	check(blank["queue_identity"] == "", "absent queue presented as ready")
	q, e := core.OpenQueue(filepath.Join(root, "queue"))
	must(e)
	defer q.Close()
	job, e := q.SubmitCommand("compatibility-harmless-never-executed", root, []string{"/bin/true"}, 1)
	must(e)
	var origin map[string]string
	anchor, _ := json.Marshal([]string{filepath.Join(root, "bin", "dispatcher-proof"), "anchor"})
	originArgs := []string{"do", "space.new", "--name", "owned-compatibility", "--folder", root}
	if !exists(filepath.Join(root, "peer-transfer")) {
		originArgs = append(originArgs, "--argv", string(anchor))
	}
	must(json.Unmarshal(run(app, originArgs...), &origin))
	save(filepath.Join(root, "owned-spaces.json"), []map[string]string{origin})
	run(app, "do", "space.select", "--id", origin["space"])
	run(app, "do", "layout.switch", "--name", "ocq-workspace")
	run(app, "shot", filepath.Join(root, "initial.png"))
	save(filepath.Join(root, "target.json"), map[string]string{"job": job.ID})
	type snapshot struct {
		Error                                          *string
		Ready, Pending, CancelWorking, DispatchWorking bool
		Denied                                         []string
		Compatibility                                  map[string]any
		Cancel                                         *core.QueuedCancelStatus
		Dispatcher                                     *core.CommandDispatcherStatus
		Submission                                     *core.CommandSubmissionStatus
		Detail                                         map[string]any
		DetailError                                    string
		DetailLoading                                  bool
		Spaces                                         []struct {
			ID           string  `json:"id"`
			Machine      *string `json:"machine"`
			Active       bool    `json:"active"`
			Name, Folder string
		}
		Machines []struct {
			Name   *string
			Layout string
		}
		Terminals, Panes []json.RawMessage
	}
	command := func(name string) snapshot {
		p := filepath.Join(root, "snapshot.json")
		_ = os.Remove(p)
		run(app, "do", "ocq."+name)
		wait("native snapshot absent", func() bool { return exists(p) })
		var out snapshot
		load(p, &out)
		return out
	}
	checked := func(name string) snapshot {
		s := command(name)
		check(s.Error == nil, "native callback: "+name)
		return s
	}
	settle := func(f func(snapshot) bool) snapshot {
		var out snapshot
		wait("native component did not settle", func() bool { out = checked("compat-snapshot"); return f(out) })
		return out
	}
	check(checked("compat-check").Ready, "matched bridge unavailable")
	if exists(filepath.Join(root, "peer-transfer")) {
		settle(func(s snapshot) bool { return modelAnchorReady(root, "origin", origin["terminal"], s.Terminals) })
		verifyNativeFailureCapture(root, app)
		afterDiagnostic := checked("compat-snapshot")
		sameSelection := false
		for _, s := range afterDiagnostic.Spaces {
			if s.Active && s.ID == origin["space"] && s.Machine == nil && s.Folder == root {
				sameSelection = true
			}
		}
		check(sameSelection, "rejected diagnostic selection changed active context")
	}
	if exists(filepath.Join(root, "corrections-only")) {
		nativeCorrections(root, app, origin, func(n string) { checked(n) }, func() bool { return command("compat-check").Ready }, q)
		return
	}
	checked("compat-open")
	settle(func(s snapshot) bool { return s.Cancel != nil && !s.CancelWorking })
	checked("compat-prepare")
	settle(func(s snapshot) bool { return s.Cancel != nil && s.Cancel.State == "prepared" && !s.CancelWorking })
	checked("compat-cancel")
	settle(func(s snapshot) bool {
		return s.Cancel != nil && s.Cancel.State == "cancelled_observed" && !s.CancelWorking
	})
	j, e := q.Job(job.ID)
	must(e)
	check(j.State == "cancelled" && j.Outcome == "cancelled_before_launch", "matched harmless mutation not applied")
	// Keep a genuine prepared dispatcher intent, never arm/start it under a bad
	// handshake. This exercises its actual native-start callback, not a boolean.
	checked("compat-dispatch-open")
	settle(func(s snapshot) bool { return s.Dispatcher != nil && !s.DispatchWorking })
	checked("compat-dispatch-prepare")
	prepared := settle(func(s snapshot) bool {
		return s.Dispatcher != nil && s.Dispatcher.State == "prepared" && !s.DispatchWorking
	})
	beforeTrace := func() map[string]int {
		b, e := os.ReadFile(filepath.Join(root, "bridge.trace"))
		must(e)
		m := map[string]int{}
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var v map[string]string
			must(json.Unmarshal([]byte(line), &v))
			m[v["action"]]++
		}
		return m
	}
	var accepted map[string]any
	must(json.Unmarshal(run(bridge, "compatibility", origin["space"], root), &accepted))
	actions := accepted["actions"].(map[string]any)
	save(filepath.Join(root, "compatibility.json"), accepted)
	var results []map[string]any
	for _, mode := range []string{"old", "missing", "mismatch", "malformed", "action-mismatch", "timeout"} {
		must(os.WriteFile(filepath.Join(root, "mode"), []byte(mode), 0600))
		s := command("compat-check")
		check(!s.Ready && s.Error != nil, "incompatible handshake accepted: "+mode)
		before := beforeTrace()
		audit := checked("compat-audit")
		check(len(audit.Denied) == len(actions), "mutation inventory not fully denied")
		checked("compat-dispatch-start")
		after := settle(func(s snapshot) bool { return !s.DispatchWorking })
		check(len(after.Terminals) == len(prepared.Terminals), "incompatible native start launched a terminal")
		check(!exists(filepath.Join(root, "queue", "herdr-command-dispatcher", "claim.json")), "incompatible dispatcher claimed")
		afterTrace := beforeTrace()
		for action := range actions {
			check(before[action] == afterTrace[action], "mutation crossed real process boundary: "+mode+"/"+action)
		}
		save(filepath.Join(root, "audit-"+mode+".json"), map[string]any{"denied": audit.Denied, "before_boundary": before, "after_boundary": afterTrace})
		// A real stored view remains usable on the same incompatible boundary.
		b := run(filepath.Join(root, "bin", "ocq-herdr"), "queue-job", job.ID)
		var view core.QueueJobOutput
		must(json.Unmarshal(b, &view))
		check(view.Job.ID == job.ID, "stored inspection unavailable")
		checked("compat-open")
		settle(func(s snapshot) bool { return s.Detail != nil && s.Detail["job"] != nil })
		results = append(results, map[string]any{"mode": mode, "denied_actions": len(audit.Denied), "mutation_commands": 0, "new_native_terminals": 0, "stored_read": "available"})
	}
	// Bridge replacement after a matched handshake is refused by the actual
	// command image check before its RunE, independently from the JS gate.
	must(os.WriteFile(filepath.Join(root, "mode"), []byte("matched"), 0600))
	check(checked("compat-check").Ready, "recheck unavailable")
	flags := []string{"--mod-contract", accepted["protocol"].(string), "--mod-action", actions["queued-cancel-prepare"].(string), "--mod-image", accepted["image_sha256"].(string), "--mod-context", accepted["bridge_context"].(string), "--mod-space", origin["space"], "--mod-folder", root, "--mod-queue", accepted["queue_identity"].(string)}
	args := []string{"queued-cancel-prepare", origin["space"], accepted["queue_identity"].(string), job.ID, "1", "queued"}
	check(exec.Command(bridge, args...).Run() != nil, "unversioned caller admitted")
	wrongAction := append([]string(nil), flags...)
	wrongAction[3] = "different-per-action-semantics"
	check(exec.Command(bridge, append(append([]string(nil), args...), wrongAction...)...).Run() != nil, "wrong per-action contract admitted")
	check(exec.Command(filepath.Join(root, "bin", "bridge-drift"), append(args, flags...)...).Run() != nil, "changed executing image admitted")
	var proofMode []byte
	proofMode, e = os.ReadFile(filepath.Join(root, "proof-mode"))
	must(e)
	if strings.TrimSpace(string(proofMode)) == "--local-audits" {
		must(os.WriteFile(filepath.Join(root, "mode"), []byte("native-incompatible"), 0600))
		checked("compat-dispatch-start")
		noLaunch := settle(func(s snapshot) bool { return !s.DispatchWorking })
		check(noLaunch.Dispatcher != nil && noLaunch.Dispatcher.State == "unknown" && noLaunch.Dispatcher.Owner == nil, "native boundary fixture did not reach its unlaunched attempt")
		check(len(noLaunch.Terminals) == len(prepared.Terminals), "native open bypassed failed fresh compatibility check")
		check(!noLaunch.Ready, "failed native compatibility check left mutation controls enabled")
		must(os.WriteFile(filepath.Join(root, "mode"), []byte("matched"), 0600))
		check(checked("compat-check").Ready, "matched read-only recheck unavailable")
		save(filepath.Join(root, "results.json"), map[string]any{"matched": "cancelled_before_launch", "faults": results, "action_count": len(actions), "native_after_attempt": "fresh check refused, no terminal launched", "bridge_replacement": "refused", "noninitializing": "queue remains absent", "selected_machine": "NOT_RUN_local_only"})
		run(app, "do", "space.close", "--id", origin["space"], "--confirmed", "true")
		wait("local audit terminal did not finish", func() bool {
			var ts struct{ Terminals []struct{ State string } }
			must(json.Unmarshal(run(filepath.Join(root, "bin", "hc"), "ls", "--all"), &ts))
			for _, t := range ts.Terminals {
				if t.State == "live" {
					return false
				}
			}
			return true
		})
		fmt.Printf("PASS LOCAL ONLY: %d actions denied across six faults, stored reads retained, post-attempt native launch refused; selected-machine NOT RUN\n", len(actions))
		return
	}
	check(strings.TrimSpace(string(proofMode)) == "--peer", "explicit peer mode required")
	// Two private local queues and HC profiles, no network host or execution of
	// queued work. Peer setup is direct core fixture work, never a mod mutation.
	peerState := filepath.Join(root, "machine-queue")
	peerQueue, e := core.OpenQueue(peerState)
	must(e)
	defer peerQueue.Close()
	peerJob, e := peerQueue.SubmitCommand("peer-stored-never-dispatched", root, []string{"/bin/true"}, 0)
	must(e)
	peerReader, e := core.OpenReader(peerState)
	must(e)
	peerScope, e := peerReader.CommandSubmissionContext("sp-peer-fixture")
	must(e)
	must(peerReader.Close())
	check(peerScope.Queue != accepted["queue_identity"], "peer queue identity not distinct")
	save(filepath.Join(root, "peer-target.json"), map[string]string{"job": peerJob.ID, "queue": peerScope.Queue})
	beforeRegistry := run(filepath.Join(root, "bin", "hc"), "machine", "ls", "--json")
	var initialRegistry struct{ Machines []peerRegistration }
	must(json.Unmarshal(beforeRegistry, &initialRegistry))
	check(len(initialRegistry.Machines) == 0, "fresh private machine registry required")
	must(os.WriteFile(filepath.Join(root, "machines-before.json"), beforeRegistry, 0600))
	registrationBytes := run(filepath.Join(root, "bin", "hc"), "machine", "add", "--name", "compatibility-local-fixture", "--session", "p", "--via", filepath.Join(root, "local-machine"))
	must(os.WriteFile(filepath.Join(root, "machine-added.json"), registrationBytes, 0600))
	var registered peerRegistration
	must(json.Unmarshal(registrationBytes, &registered))
	check(registered.Name != "" && registered.ID != "" && !registered.Temporary && registered.Via && registered.Session == "p" && registered.Target == filepath.Join(root, "local-machine"), "exact ordinary private peer registration required")
	var privateCore struct{ ID string }
	load(filepath.Join(root, "machine-state", "herdr-core", "p", "core.json"), &privateCore)
	check(privateCore.ID == registered.ID, "registration reached a different private core")
	afterRegistry := run(filepath.Join(root, "bin", "hc"), "machine", "ls", "--json")
	var finalRegistry struct{ Machines []peerRegistration }
	must(json.Unmarshal(afterRegistry, &finalRegistry))
	check(len(finalRegistry.Machines) == 1 && finalRegistry.Machines[0].ID == registered.ID && finalRegistry.Machines[0].Name == registered.Name && !finalRegistry.Machines[0].Temporary, "private registry identity changed")
	must(os.WriteFile(filepath.Join(root, "machines-after.json"), afterRegistry, 0600))
	requestedName := "owned-peer-" + filepath.Base(root)
	readyPeer := settle(func(s snapshot) bool {
		for _, m := range s.Machines {
			if m.Name != nil && *m.Name == registered.Name && m.Layout == "ready" {
				return true
			}
		}
		return false
	})
	for _, s := range readyPeer.Spaces {
		check(!(s.Machine != nil && *s.Machine == registered.Name && s.Name == requestedName), "requested native peer target already exists; no resend")
	}
	preTerminals := peerInventory(root, registered)
	var noTerminals struct{ Terminals []json.RawMessage }
	must(json.Unmarshal(preTerminals, &noTerminals))
	check(len(noTerminals.Terminals) == 0, "fresh private peer already owns a terminal")
	must(os.WriteFile(filepath.Join(root, "peer-terminals-before.json"), preTerminals, 0600))
	save(filepath.Join(root, "peer-space-precondition.json"), map[string]any{"registered": registered, "native_machine_identifier": registered.Name, "core_id": privateCore.ID, "requested_name": requestedName, "spaces": readyPeer.Spaces, "machines": readyPeer.Machines, "existing_peer_terminals": 0, "resend": false})
	var other map[string]string
	must(json.Unmarshal(run(app, "do", "space.new", "--name", requestedName, "--machine", registered.Name, "--folder", root), &other))
	check(strings.HasPrefix(other["space"], registered.Name+"/sp") && strings.HasPrefix(other["terminal"], registered.Name+"/"), "returned native peer identifiers not machine-qualified")
	save(filepath.Join(root, "owned-spaces.json"), []map[string]string{origin, other})
	run(app, "shot", filepath.Join(root, "peer-model-anchor.png"))
	peerModel := settle(func(s snapshot) bool { return modelAnchorReady(root, "peer", other["terminal"], s.Terminals) })
	peerModelFound := false
	for _, s := range peerModel.Spaces {
		if s.ID == other["space"] && s.Machine != nil && *s.Machine == registered.Name && s.Folder == root {
			peerModelFound = true
		}
	}
	check(peerModelFound, "actual peer model space/folder identity unavailable")
	save(filepath.Join(root, "peer-model-snapshot.json"), peerModel)
	// Prepare the foreign space before holding a five-second product handshake;
	// only the actual selection change occurs while its old response is delayed.
	run(app, "do", "space.select", "--id", origin["space"])
	run(app, "shot", filepath.Join(root, "original-before-delay.png"))
	check(checked("compat-check").Ready, "local check before peer race failed")
	checked("compat-dispatch-open")
	settle(func(s snapshot) bool {
		return s.Dispatcher != nil && s.Dispatcher.State == "prepared" && !s.DispatchWorking
	})
	checked("compat-command-open")
	settle(func(s snapshot) bool { return s.Submission != nil && s.Submission.State == "idle" })
	save(filepath.Join(root, "transfer-config.json"), map[string]string{"Family": "command", "File": "command-submission.js", "Step": "2"})
	checked("compat-transfer")
	wait("local private receiver not paused", func() bool { return exists(filepath.Join(root, "transfer.waiting")) })
	must(os.WriteFile(filepath.Join(root, "peer-start.hold"), nil, 0600))
	checked("compat-dispatch-start")
	wait("local start callback not paused", func() bool { return exists(filepath.Join(root, "peer-start.entered")) })
	check(!exists(filepath.Join(root, "queue", "herdr-command-dispatcher", "claim.json")), "paused native start already claimed")
	beforeSwitch := beforeTrace()
	must(os.WriteFile(filepath.Join(root, "handshake.hold"), nil, 0600))
	checked("compat-delay")
	wait("handshake hold not entered", func() bool { return exists(filepath.Join(root, "handshake.entered")) })
	run(app, "do", "space.select", "--id", other["space"])
	run(app, "shot", filepath.Join(root, "other-context.png"))
	must(os.WriteFile(filepath.Join(root, "handshake.release"), nil, 0600))
	wait("delayed handshake did not finish", func() bool { return exists(filepath.Join(root, "handshake.settled")) })
	check(exists(filepath.Join(root, "handshake.returned")), "old handshake did not actually return")
	last := settle(func(s snapshot) bool { return !s.Pending })
	check(!last.Ready, "delayed handshake unlocked a different machine")
	check(last.Compatibility == nil, "old handshake published into new selected-machine context")
	remote := false
	for _, s := range last.Spaces {
		if s.Active && s.Machine != nil {
			check(s.ID == other["space"] && *s.Machine == registered.Name && s.Folder == root, "actual selected peer machine/space/folder differs")
			remote = true
		}
	}
	check(remote, "native selected-machine switch not established")
	must(os.WriteFile(filepath.Join(root, "transfer.release"), nil, 0600))
	must(os.WriteFile(filepath.Join(root, "peer-start.release"), nil, 0600))
	wait("stale private exchange did not settle", func() bool { return exists(filepath.Join(root, "transfer-done.json")) })
	wait("stale native callback did not settle", func() bool { return exists(filepath.Join(root, "peer-start.settled")) })
	var transfer struct{ Refused bool }
	load(filepath.Join(root, "transfer-done.json"), &transfer)
	check(transfer.Refused, "stale private exchange succeeded")
	localScope := core.CommandSubmissionScope{Machine: "local", Space: origin["space"], Queue: accepted["queue_identity"].(string)}
	localSubmission, e := core.InspectCommandSubmission(filepath.Join(root, "queue"), localScope)
	must(e)
	check(localSubmission.State == "idle", "stale private exchange prepared a local intent")
	check(!exists(filepath.Join(root, "queue", "herdr-command-dispatcher", "claim.json")) && !exists(filepath.Join(root, "queue", "herdr-command-dispatcher", "attempt.json")), "stale native callback claimed or attempted")
	checked("compat-peer-descriptor")
	var peerDescriptor map[string]any
	load(filepath.Join(root, "peer-descriptor.json"), &peerDescriptor)
	check(peerDescriptor["queue_identity"] == peerScope.Queue && peerDescriptor["bridge_context"] != accepted["bridge_context"] && peerDescriptor["space_id"] == strings.TrimPrefix(other["space"], registered.Name+"/") && peerDescriptor["folder"] == root, "actual selected-machine bridge context incorrect")
	checked("compat-foreign-open")
	foreign := settle(func(s snapshot) bool {
		if s.Detail == nil {
			return false
		}
		j, _ := s.Detail["job"].(map[string]any)
		return j != nil && j["id"] == peerJob.ID
	})
	check(foreign.Detail["queue_identity"] == peerScope.Queue && !foreign.Ready && foreign.Compatibility == nil, "foreign stored view used local queue or authorized mutations")
	save(filepath.Join(root, "foreign-context-snapshot.json"), foreign)
	run(app, "shot", filepath.Join(root, "foreign-queued-job.png"))
	status := run(app, "status")
	var finalStatus struct {
		Layout   string
		Errors   []json.RawMessage
		Overlays []string
	}
	must(json.Unmarshal(status, &finalStatus))
	must(os.WriteFile(filepath.Join(root, "foreign-layout-status.json"), status, 0600))
	check(finalStatus.Layout == "ocq-workspace" && len(finalStatus.Errors) == 0 && len(finalStatus.Overlays) == 0, "native layout has errors or obstructing overlays")
	// A failed selected-peer read must clear the view, not retry on the local queue.
	beforeMissing := beforeTrace()["queue-job"]
	must(os.WriteFile(filepath.Join(root, "mode"), []byte("missing-read"), 0600))
	checked("compat-foreign-open")
	unavailable := settle(func(s snapshot) bool { return !s.DetailLoading && s.Detail == nil && s.DetailError != "" })
	check(beforeTrace()["queue-job"] == beforeMissing+1, "unavailable peer read retried or fell back")
	check(!unavailable.Ready && unavailable.Compatibility == nil, "unavailable peer read enabled mutations")
	save(filepath.Join(root, "foreign-unavailable-snapshot.json"), unavailable)
	must(os.WriteFile(filepath.Join(root, "mode"), []byte("matched"), 0600))
	audit := checked("compat-audit")
	check(len(audit.Denied) == len(actions), "foreign selection did not deny every mutation")
	afterSwitch := beforeTrace()
	for action := range actions {
		check(beforeSwitch[action] == afterSwitch[action], "stale/foreign action crossed mutation boundary: "+action)
	}
	pj, e := peerQueue.Job(peerJob.ID)
	must(e)
	check(pj.State == "queued" && pj.Digest == peerJob.Digest, "peer stored job changed")
	for _, location := range []string{"local", registered.Name} {
		var b []byte
		if location == "local" {
			b = run(filepath.Join(root, "bin", "hc"), "ls", "--all", "--json")
		} else {
			b = peerInventory(root, registered)
		}
		var ts struct {
			Terminals []struct {
				Address string `json:"address"`
			}
		}
		must(json.Unmarshal(b, &ts))
		expected := map[string]bool{other["terminal"]: true}
		if location == "local" {
			expected[origin["terminal"]] = true
		}
		check(len(ts.Terminals) == len(expected), "stale callback changed native terminal inventory")
		for _, t := range ts.Terminals {
			// HC ls emits compact terminal summaries without argv. Require the
			// exact two known anchors, including finished terminals, instead of
			// accidentally accepting absent argv as a no-helper observation.
			check(expected[t.Address], "stale callback launched an unexpected native terminal")
			delete(expected, t.Address)
		}
		must(os.WriteFile(filepath.Join(root, "peer-race-terminals-"+location+".json"), b, 0600))
	}
	localJobs, e := q.Jobs()
	must(e)
	peerJobs, e := peerQueue.Jobs()
	must(e)
	check(len(localJobs) == 1 && len(peerJobs) == 1, "stale/foreign callback admitted a job")
	check(localJobs[0].ID == job.ID && localJobs[0].Digest == job.Digest && localJobs[0].State == "cancelled" && peerJobs[0].ID == peerJob.ID && peerJobs[0].Digest == peerJob.Digest && peerJobs[0].State == "queued", "private queue receipt/state changed unexpectedly")
	save(filepath.Join(root, "selected-machine-audit.json"), map[string]any{"denied": audit.Denied, "before": beforeSwitch, "after": afterSwitch, "local_queue": accepted["queue_identity"], "peer_queue": peerScope.Queue, "stale_transfer": "idle_no_intent", "stale_start": "no_claim_no_attempt"})
	save(filepath.Join(root, "results.json"), map[string]any{"matched": "cancelled_before_launch", "faults": results, "action_count": len(actions), "delayed_machine_switch": "real_selected_machine_old_response_refused", "bridge_replacement": "refused", "native_after_attempt": "separate_final_local_audit", "stale_native_callback": "no_claim_attempt_or_launch", "stale_private_transfer": "no_prepared_intent", "stored_foreign_read": "exact_distinct_peer_queue", "noninitializing": "absent_probe_queue_remains_absent"})
	for _, s := range []map[string]string{other, origin} {
		run(app, "do", "space.close", "--id", s["space"], "--confirmed", "true")
	}
	wait("local stdio peer terminals did not finish", func() bool {
		b := peerInventory(root, registered)
		var ts struct{ Terminals []struct{ State string } }
		must(json.Unmarshal(b, &ts))
		for _, t := range ts.Terminals {
			if t.State == "live" {
				return false
			}
		}
		must(os.WriteFile(filepath.Join(root, "peer-terminals-closed.json"), b, 0600))
		return true
	})
	check(peerDescriptor["queue_identity"] != accepted["queue_identity"], "two private queue identities collapsed")
	wait("owned terminals did not finish", func() bool {
		b := run(filepath.Join(root, "bin", "hc"), "ls", "--all")
		var ts struct{ Terminals []struct{ State string } }
		must(json.Unmarshal(b, &ts))
		for _, t := range ts.Terminals {
			if t.State == "live" {
				return false
			}
		}
		must(os.WriteFile(filepath.Join(root, "terminals-closed.json"), b, 0600))
		return true
	})
	fmt.Printf("PASS matched native harmless cancellation; %d exact action contracts denied across old/missing/malformed/version/action/timeout boundaries; no incompatible native launch; delayed selected-machine handshake refused; read-only inspection retained\n", len(actions))
}
