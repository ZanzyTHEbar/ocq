// Opt-in one real stored-job -> exact-session handoff acceptance scenario.
// The separate fixture creator precreates an empty session; no agent runs.
package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"ocq/internal/core"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

//go:embed native-driver.js
var nativeDriver []byte

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
func write(p string, v any) { b, e := json.Marshal(v); must(e); must(os.WriteFile(p, b, 0600)) }
func read(p string, v any)  { b, e := os.ReadFile(p); must(e); must(json.Unmarshal(b, v)) }
func run(bin string, a ...string) []byte {
	c := exec.Command(bin, a...)
	c.Stderr = os.Stderr
	b, e := c.Output()
	must(e)
	return b
}
func wait(f func() bool) {
	end := time.Now().Add(25 * time.Second)
	for !f() {
		check(time.Now().Before(end), "bounded wait expired")
		time.Sleep(40 * time.Millisecond)
	}
}

func main() {
	if pipeMode() {
		return
	}
	if len(os.Args) == 3 && os.Args[1] == "--install" {
		root := os.Getenv("PROBE_ROOT")
		check(filepath.IsAbs(root), "private root required")
		mod := filepath.Join(root, "apphome", "mods", "ocq")
		must(os.MkdirAll(mod, 0700))
		files, e := os.ReadDir(os.Args[2])
		must(e)
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".js") && f.Name() != "mod.json" {
				continue
			}
			b, e := os.ReadFile(filepath.Join(os.Args[2], f.Name()))
			must(e)
			s := string(b)
			if f.Name() == "main.js" {
				needle := `if (await nextCommand() === "focus") this.handle.focus();`
				check(strings.Count(s, needle) == 1, "command boundary changed")
				s = `import { nativeAction } from "./native-driver.js";` + "\n" + strings.Replace(s, needle, `const command = await nextCommand(); if (command === "focus") this.handle.focus(); else if (command.startsWith("race-")) await nativeAction(this, command, cx);`, 1)
			}
			if f.Name() == "queue.js" {
				needle := `this.targets.push(fn);`
				check(strings.Count(s, needle) == 1, "render callback boundary changed")
				s = strings.Replace(s, needle, needle+` (this.nativeActions ||= {})[id] = fn;`, 1)
			}
			must(os.WriteFile(filepath.Join(mod, f.Name()), []byte(s), 0600))
		}
		must(os.WriteFile(filepath.Join(mod, "native-driver.js"), nativeDriver, 0600))
		return
	}
	check(len(os.Args) == 3, "usage: handoff-proof BRIDGE APP")
	root := os.Getenv("PROBE_ROOT")
	check(filepath.IsAbs(root), "explicit private root required")
	bridge, app := os.Args[1], os.Args[2]
	var fixture struct {
		Target core.AgentReconcileTarget `json:"target"`
	}
	read(filepath.Join(root, "fixture.json"), &fixture)
	t := fixture.Target
	apiReads := func() int {
		b, e := os.ReadFile(filepath.Join(root, "api.trace"))
		if os.IsNotExist(e) {
			return 0
		}
		must(e)
		check(!strings.Contains(string(b), "DENIED"), "non-read-only API invocation")
		return strings.Count(string(b), "\n")
	}
	args := func(t core.AgentReconcileTarget, action string, service ...string) []string {
		return append([]string{"agent-handoff-" + action, t.Space, t.Queue, t.Job, t.Sequence, t.Session, t.Worktree}, service...)
	}
	call := func(t core.AgentReconcileTarget, action string, service ...string) core.AgentHandoff {
		var s core.AgentHandoff
		b := run(bridge, args(t, action, service...)...)
		for _, private := range []string{os.Getenv("OCQ_HANDOFF_SERVER_URL"), os.Getenv("OPENCODE_SERVER_PASSWORD"), `"owner_token"`, "PRIVATE never-sent fixture request"} {
			check(private != "" && !strings.Contains(string(b), private), "unsafe data in ordinary handoff DTO")
		}
		must(json.Unmarshal(b, &s))
		return s
	}
	s := call(t, "status")
	check(apiReads() == 0, "stored handoff performed a live lookup")
	check(s.Observation == "stored_only" && s.ServiceIdentity != "", "stored-only handoff / explicit service missing")
	check(call(t, "verify", s.ServiceIdentity).Observation == "verified_snapshot", "exact empty session not verified")
	wrong := t
	wrong.Worktree = filepath.Dir(t.Worktree)
	c := exec.Command(bridge, args(wrong, "verify", s.ServiceIdentity)...)
	check(c.Run() != nil, "wrong recorded worktree accepted")
	// The script changes only this fixture's imported ownership to exercise real
	// wrong-location/absent-session reads, then restores the exact known target.
	for _, scenario := range []struct{ Mode, Want string }{{"wrong-location", "session_mismatch"}, {"absent", "session_unavailable"}} {
		run(filepath.Join(root, "mutate"), scenario.Mode)
		var changed struct {
			Target core.AgentReconcileTarget `json:"target"`
		}
		read(filepath.Join(root, "changed.json"), &changed)
		got := call(changed.Target, "verify", s.ServiceIdentity)
		check(got.Observation == scenario.Want, "real API refusal not surfaced: "+scenario.Mode)
		write(filepath.Join(root, scenario.Mode+".json"), got)
	}
	run(filepath.Join(root, "mutate"), "restore")
	check(call(t, "verify", strings.Repeat("0", 64)).Observation == "service_unavailable", "service change not refused")
	fmt.Println("PASS real empty exact session verified; wrong recorded location refused; real wrong-location/absent-session observations refused; changed configured service refused")
	if app == "--backend-pipes" {
		pipeProof(bridge, t, s.ServiceIdentity)
		return
	}
	var appStatus struct {
		Overlays []string `json:"overlays"`
	}
	must(json.Unmarshal(run(app, "status"), &appStatus))
	for _, overlay := range appStatus.Overlays {
		check(overlay == "frame-welcome", "unexpected fixture overlay; do not dismiss")
		run(app, "do", "overlay.close", "--id", overlay)
	}
	beforeNative := apiReads()
	var origin map[string]string
	must(json.Unmarshal(run(app, "do", "space.new", "--name", "owned-human-handoff", "--folder", root, "--argv", `["/bin/sleep","600"]`), &origin))
	write(filepath.Join(root, "native-owned-spaces.json"), []map[string]string{origin})
	run(app, "do", "space.select", "--id", origin["space"])
	run(app, "do", "layout.switch", "--name", "ocq-workspace")
	run(app, "shot", filepath.Join(root, "native-handoff-initial.png"))
	write(filepath.Join(root, "native-target.json"), map[string]string{"job_id": t.Job})
	type snap struct {
		Error                  *string
		Working, Current       bool
		RetainedWorking        bool
		Status, RetainedStatus *core.AgentHandoff
		Clipboard              string
		Panes, Terminals       []json.RawMessage
	}
	command := func(name string) snap {
		p := filepath.Join(root, "native-snapshot.json")
		_ = os.Remove(p)
		run(app, "do", "ocq."+name)
		wait(func() bool { _, e := os.Stat(p); return e == nil })
		var out snap
		read(p, &out)
		check(out.Error == nil, "native action failed")
		return out
	}
	settle := func() snap {
		var out snap
		wait(func() bool { out = command("race-handoff-snapshot"); return !out.Working && out.Status != nil })
		return out
	}
	command("race-handoff-open")
	var stored snap
	wait(func() bool { stored = settle(); return len(stored.Panes) == 1 && len(stored.Terminals) == 1 })
	check(stored.Status.Observation == "stored_only" && stored.Current, "native handoff not stored only")
	run(app, "shot", filepath.Join(root, "native-handoff-stored.png"))
	copy := command("race-handoff-copy")
	write(filepath.Join(root, "native-stored.json"), stored)
	write(filepath.Join(root, "native-copied.json"), copy)
	check(strings.Contains(copy.Clipboard, t.Session) && strings.Contains(copy.Clipboard, t.Worktree) && !strings.Contains(copy.Clipboard, "--session"), "actual clipboard lacks exact location or includes launch command")
	check(apiReads() == beforeNative, "native view/copy implicitly observed live API")
	command("race-handoff-verify")
	verified := settle()
	check(verified.Status.Observation == "verified_snapshot", "native explicit verify failed")
	run(app, "shot", filepath.Join(root, "native-handoff-verified.png"))
	// Capture the latest real copy callback, hold an explicit API response, then
	// leave the space. Neither stale callback nor post-await response may act.
	command("race-handoff-stored")
	check(settle().Status.Observation == "stored_only", "race baseline not stored only")
	copy = command("race-handoff-copy")
	must(os.WriteFile(filepath.Join(root, "verify.hold"), nil, 0600))
	command("race-handoff-verify")
	wait(func() bool { _, e := os.Stat(filepath.Join(root, "verify.entered")); return e == nil })
	var other map[string]string
	must(json.Unmarshal(run(app, "do", "space.new", "--name", "owned-other-handoff-context", "--folder", root, "--argv", `["/bin/sleep","600"]`), &other))
	write(filepath.Join(root, "native-owned-spaces.json"), []map[string]string{origin, other})
	run(app, "do", "space.select", "--id", other["space"])
	run(app, "shot", filepath.Join(root, "native-handoff-other.png"))
	stale := command("race-handoff-stale-copy")
	check(stale.Clipboard == "owned stale-copy sentinel (not a session)", "stale callback wrote clipboard")
	must(os.WriteFile(filepath.Join(root, "verify.release"), nil, 0600))
	wait(func() bool { _, e := os.Stat(filepath.Join(root, "verify.returned")); return e == nil })
	// The retained component must not publish a new result after leaving.
	wait(func() bool { stale = command("race-handoff-snapshot"); return !stale.RetainedWorking })
	a, _ := json.Marshal(stale.RetainedStatus)
	b, _ := json.Marshal(copy.Status)
	check(string(a) == string(b), "stale API response published")
	check(len(stored.Panes) == len(copy.Panes) && len(stored.Terminals) == len(copy.Terminals), "handoff spawned a pane/client")
	write(filepath.Join(root, "native-handoff-final.json"), stale)
	for _, space := range []map[string]string{origin, other} {
		run(app, "do", "space.close", "--id", space["space"], "--confirmed", "true")
	}
	wait(func() bool {
		b := run(filepath.Join(root, "bin", "hc"), "ls", "--all")
		var ts struct{ Terminals []struct{ State string } }
		must(json.Unmarshal(b, &ts))
		for _, t := range ts.Terminals {
			if t.State == "live" {
				return false
			}
		}
		must(os.WriteFile(filepath.Join(root, "native-terminals-closed.json"), b, 0600))
		return true
	})
	fmt.Println("PASS native 0.1.14 stored handoff/copy/explicit read-only verify; no client/pane launch; retained callback and post-await context guards")
}
