package main

// Opt-in native regression harness. Start a new isolated app/core using native.sh;
// this driver never discovers a shared app, queue, server, or last-used pane.
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
var nativeDriver []byte

func exists(p string) bool { _, e := os.Stat(p); return e == nil }
func fixtureState(root string) string {
	if exists(filepath.Join(root, "default-state-ancestor")) {
		return filepath.Join(root, "default-state-real", "ocq")
	}
	if exists(filepath.Join(root, "default-state-xdg")) {
		return filepath.Join(root, "default-state", "ocq")
	}
	return filepath.Join(root, "queue")
}
func waitNative(why string, f func() bool) {
	end := time.Now().Add(25 * time.Second)
	for !f() {
		check(time.Now().Before(end), why)
		time.Sleep(25 * time.Millisecond)
	}
}
func nativeGate(root string, args []string) {
	check(len(args) > 0, "gate action required")
	switch args[0] {
	case "target":
		b, e := os.ReadFile(filepath.Join(root, "native-target.json"))
		must(e)
		_, e = os.Stdout.Write(b)
		must(e)
	case "snapshot":
		check(len(args) == 2, "snapshot required")
		must(os.WriteFile(filepath.Join(root, "native-snapshot.json"), []byte(args[1]), 0600))
	case "settled":
		check(len(args) == 4, "settled identity required")
		must(os.WriteFile(filepath.Join(root, args[1]+".settled"), encode(args[2:]), 0600))
	case "start", "drain", "born":
		phase := args[0]
		if !exists(filepath.Join(root, phase+".hold")) {
			return
		}
		must(os.WriteFile(filepath.Join(root, phase+".entered"), nil, 0600))
		waitNative("native gate release "+phase, func() bool { return exists(filepath.Join(root, phase+".release")) })
	default:
		panic("unsupported native gate")
	}
}
func nativeBridge(root string) {
	bridge := os.Getenv("OCQ_RACE_BRIDGE")
	check(filepath.IsAbs(bridge), "explicit real bridge required")
	args := os.Args[1:]
	check(len(args) > 0, "bridge arguments required")
	f, e := os.OpenFile(filepath.Join(root, "native-bridge.trace"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	must(e)
	_, e = fmt.Fprintln(f, strings.Join(args, " "))
	must(e)
	must(f.Close())
	c := exec.Command(bridge, args...)
	c.Stderr = os.Stderr
	b, e := c.Output()
	if e != nil {
		if exit, ok := e.(*exec.ExitError); ok {
			os.Exit(exit.ExitCode())
		}
		panic(e)
	}
	if args[0] == "dispatcher-status" && exists(filepath.Join(root, "born.hold")) && !exists(filepath.Join(root, "born.entered")) {
		var s core.CommandDispatcherStatus
		must(json.Unmarshal(b, &s))
		if s.State == "born" {
			save(filepath.Join(root, "native-born.json"), s)
			nativeGate(root, []string{"born"})
		}
	}
	_, e = os.Stdout.Write(b)
	must(e)
}
func nativeInstall(root, source string) {
	mod := filepath.Join(root, "apphome", "mods", "ocq")
	must(os.MkdirAll(mod, 0700))
	files, e := os.ReadDir(source)
	must(e)
	for _, file := range files {
		name := file.Name()
		if !strings.HasSuffix(name, ".js") && name != "mod.json" {
			continue
		}
		b, e := os.ReadFile(filepath.Join(source, name))
		must(e)
		if name == "main.js" {
			s := string(b)
			needle := `if (await nextCommand() === "focus") this.handle.focus();`
			check(strings.Count(s, needle) == 1, "native command boundary changed")
			s = `import { nativeAction } from "./native-driver.js";` + "\n" + strings.Replace(s, needle, `const command = await nextCommand(); if (command === "focus") this.handle.focus(); else if (command.startsWith("race-")) await nativeAction(this, command, cx);`, 1)
			b = []byte(s)
		}
		if name == "command-dispatcher.js" {
			s := string(b)
			for _, phase := range []string{"start", "drain"} {
				method := "confirmStart(owner, cx) {"
				if phase == "drain" {
					method = "confirmDrain(owner, cx) {"
				}
				parts := strings.SplitN(s, method, 2)
				check(len(parts) == 2, "native confirm boundary changed")
				header := "cx.spawn(async cx => {\n      try {"
				check(strings.Contains(parts[1], header), "native callback boundary changed")
				parts[1] = strings.Replace(parts[1], header, header+` const gate = await exec("ocq-dispatch-race-gate", ["`+phase+`"], { on: null }); if (gate.code !== 0) throw Error("Native gate failed");`, 1)
				cleanup := "finally { this.working = false; if (this.operationCurrent(owner, operation)) cx.notify(); }"
				if phase == "start" {
					cleanup = "finally { this.working = false; if (this.operationCurrent(owner, operation)) { this.confirm = null; cx.notify(); } }"
				}
				check(strings.Count(parts[1], cleanup) == 1, "native cleanup boundary changed")
				parts[1] = strings.Replace(parts[1], cleanup, strings.TrimSuffix(cleanup, "}")+` await exec("ocq-dispatch-race-gate", ["settled", "`+phase+`", String(this.working), String(this.current(owner))], { on: null }); }`, 1)
				s = strings.Join(parts, method)
			}
			b = []byte(s)
		}
		must(os.WriteFile(filepath.Join(mod, name), b, 0600))
	}
	must(os.WriteFile(filepath.Join(mod, "native-driver.js"), nativeDriver, 0600))
}

func nativeRun(root, app string) {
	state := fixtureState(root)
	check(os.Getenv("HERDR_APP_HOME") == filepath.Join(root, "apphome"), "dedicated exact native app required")
	if exists(filepath.Join(root, "default-state-xdg")) {
		_, explicit := os.LookupEnv("OCQ_STATE_DIR")
		check(!explicit && os.Getenv("XDG_STATE_HOME") == filepath.Join(root, "default-state"), "unset OCQ_STATE_DIR and exact private XDG state required")
	} else {
		check(os.Getenv("OCQ_STATE_DIR") == state, "dedicated exact native queue required")
	}
	check(filepath.IsAbs(app), "explicit native binary required")
	self, e := os.Executable()
	must(e)
	run := func(bin string, args ...string) []byte {
		c := exec.Command(bin, args...)
		b, e := c.Output()
		if e != nil {
			panic(fmt.Sprintf("%s %v: %v", bin, args, e))
		}
		return b
	}
	appCall := func(args ...string) []byte { return run(app, args...) }
	phase := func(name string) { fmt.Print(string(run(self, name))) }
	if exists(filepath.Join(root, "default-state-xdg")) {
		bridge := os.Getenv("OCQ_RACE_BRIDGE")
		var empty struct {
			Queue string `json:"queue_identity"`
		}
		must(json.Unmarshal(run(bridge, "compatibility", "sp-default-missing", root), &empty))
		check(empty.Queue == "" && !exists(state), "default compatibility initialized missing queue")
		for _, a := range [][]string{{"dispatcher-read", "sp-default-missing", strings.Repeat("0", 64)}, {"dispatcher-helper", strings.Repeat("1", 64)}} {
			b, e := exec.Command(bridge, a...).CombinedOutput()
			check(e != nil, "missing default command succeeded: "+string(b))
			check(!exists(state), "default command initialized missing queue")
		}
		save(filepath.Join(root, "default-missing.json"), map[string]any{"ocq_state_dir": "unset", "state": state, "initialized": false})
	}
	phase("setup")
	if exists(filepath.Join(root, "default-state-xdg")) {
		nativeDefaultChecks(root, state)
	}
	origins := []map[string]string{}
	for _, name := range []string{"owned-dispatch-races", "owned-other-context"} {
		var o map[string]string
		must(json.Unmarshal(appCall("do", "space.new", "--name", name, "--folder", root, "--argv", string(encode([]string{self, "anchor"}))), &o))
		origins = append(origins, o)
	}
	save(filepath.Join(root, "native-origins.json"), origins)
	selectSpace := func(n int) {
		appCall("do", "space.select", "--id", origins[n]["space"])
		appCall("shot", filepath.Join(root, fmt.Sprintf("native-space-%d.png", n)))
	}
	selectSpace(0)
	appCall("do", "layout.switch", "--name", "ocq-workspace")
	shot := func(name string) { appCall("shot", filepath.Join(root, name+".png")) }
	type snapshot struct {
		Error            *string
		Working, Current bool
		Status           core.CommandDispatcherStatus
		Selected         core.CommandDispatcherTarget
		Candidates       []core.CommandDispatcherTarget
		Panes            []struct{ Pane, Terminal, Tab string }
	}
	command := func(name string) snapshot {
		_ = os.Remove(filepath.Join(root, "native-snapshot.json"))
		appCall("do", "ocq."+name)
		waitNative("native command "+name, func() bool { return exists(filepath.Join(root, "native-snapshot.json")) })
		var s snapshot
		load(filepath.Join(root, "native-snapshot.json"), &s)
		if s.Error != nil {
			panic("native action " + name + ": " + *s.Error)
		}
		return s
	}
	settle := func() snapshot {
		var s snapshot
		waitNative("UI settling", func() bool { s = command("race-snapshot"); return !s.Working })
		return s
	}
	open := func() { command("race-open"); s := settle(); check(s.Current, "native recovery unavailable") }
	writeTarget := func(o map[string]string) {
		if o["pane"] == "" {
			for _, p := range command("race-snapshot").Panes {
				if p.Terminal == o["terminal"] && p.Tab == o["tab"] {
					check(o["pane"] == "", "ambiguous supplied terminal pane")
					o["pane"] = p.Pane
				}
			}
			check(o["pane"] != "", "supplied terminal pane missing")
		}
		must(os.WriteFile(filepath.Join(root, "native-target.json"), encode(o), 0600))
		waitNative("exact native terminal inventory", func() bool {
			for _, t := range command("race-snapshot").Candidates {
				if t.Pane == o["pane"] && t.Terminal == o["terminal"] && t.Tab == o["tab"] && t.Scope.Space == o["space"] {
					return true
				}
			}
			return false
		})
		command("race-select")
	}
	hold := func(name string) { must(os.WriteFile(filepath.Join(root, name+".hold"), nil, 0600)) }
	release := func(name string) { must(os.WriteFile(filepath.Join(root, name+".release"), nil, 0600)) }
	noMarker := func(names ...string) {
		for _, name := range names {
			check(!exists(filepath.Join(state, "herdr-command-dispatcher", name+".json")), "unexpected "+name)
		}
	}
	open()
	writeTarget(origins[0])
	command("race-prepare")
	check(settle().Status.State == "prepared", "prepare required")
	hold("start")
	command("race-start")
	waitNative("start callback gate", func() bool { return exists(filepath.Join(root, "start.entered")) })
	selectSpace(1)
	command("race-open")
	settle()
	release("start")
	waitNative("stale Start cleanup", func() bool { return exists(filepath.Join(root, "start.settled")) })
	noMarker("claim", "attempt", "born")
	phase("prestart")
	var cleared []string
	load(filepath.Join(root, "start.settled"), &cleared)
	check(cleared[0] == "false" && cleared[1] == "false", "stale Start stuck/current")
	save(filepath.Join(root, "stale-start-cleanup.json"), cleared)
	selectSpace(0)
	open()
	check(settle().Status.State == "prepared", "stale Start consumed claim")
	save(filepath.Join(root, "stale-start-recovery.json"), settle().Status)
	shot("native-stale-start-recovered")
	must(os.Remove(filepath.Join(root, "start.hold")))
	must(os.Remove(filepath.Join(root, "start.settled")))
	hold("born")
	command("race-start")
	waitNative("born status gate", func() bool { return exists(filepath.Join(root, "born.entered")) })
	noMarker("bound", "ready")
	phase("prestart")
	var born core.CommandDispatcherStatus
	load(filepath.Join(root, "native-born.json"), &born)
	check(born.Owner != nil && ticks(born.Owner.PID) == born.Owner.Start, "live exact born owner required before anchor close")
	appCall("do", "pane.close", "--pane", origins[0]["pane"])
	appCall("do", "terminal.open", "--folder", root, "--place", "right", "--name", "explicit-owned-replacement", "--argv", string(encode([]string{self, "anchor"})))
	snap := command("race-snapshot")
	replacement := map[string]string{"space": origins[0]["space"], "tab": origins[0]["tab"]}
	for _, p := range snap.Panes {
		if p.Tab == replacement["tab"] && p.Pane != origins[0]["pane"] && p.Terminal != "" && p.Terminal != born.Owner.Terminal {
			check(replacement["pane"] == "", "ambiguous replacement")
			replacement["pane"] = p.Pane
			replacement["terminal"] = p.Terminal
		}
	}
	check(replacement["pane"] != "", "owned replacement missing")
	save(filepath.Join(root, "native-replacement.json"), replacement)
	check(ticks(born.Owner.PID) == born.Owner.Start, "unbound helper exited before anchor replacement")
	release("born")
	waitNative("unbound Start cleanup", func() bool { return exists(filepath.Join(root, "start.settled")) })
	noMarker("bound", "ready")
	phase("prestart")
	observed, e := core.InspectCommandDispatcher(state, born.Target)
	must(e)
	check(observed.State == "born" && observed.Process == "alive", "helper must remain live/unbound at refusal")
	save(filepath.Join(root, "unbound-refusal.json"), observed)
	shot("native-anchor-lost-unbound")
	command("race-read")
	settle()
	noMarker("bound", "ready")
	waitNative("unbound helper actual exit", func() bool { command("race-read"); return settle().Status.State == "stopped" })
	phase("prestart")
	noMarker("bound", "ready")
	shot("native-unbound-stopped-recovery")
	command("race-retire")
	check(settle().Status.State == "none", "unbound retirement failed")
	writeTarget(replacement)
	command("race-prepare")
	settle()
	command("race-start")
	waitNative("ready real dispatcher", func() bool { return settle().Status.State == "ready" })
	waitNative("real gate started", func() bool { return exists(filepath.Join(root, "command-gate-born.json")) })
	phase("running")
	s := settle()
	check(s.Status.QueueDir == state, "native helper used a different queue path")
	save(filepath.Join(root, "dispatcher-ready.json"), s.Status)
	phase("negative")
	command("race-drain-prepare")
	check(settle().Status.State == "drain_prepared", "drain preparation required")
	hold("drain")
	command("race-drain")
	waitNative("drain callback gate", func() bool { return exists(filepath.Join(root, "drain.entered")) })
	selectSpace(1)
	command("race-open")
	settle()
	release("drain")
	waitNative("stale Drain cleanup", func() bool { return exists(filepath.Join(root, "drain.settled")) })
	noMarker("drain-claim", "drain-attempt")
	phase("running")
	load(filepath.Join(root, "drain.settled"), &cleared)
	check(cleared[0] == "false" && cleared[1] == "false", "stale Drain stuck/current")
	save(filepath.Join(root, "stale-drain-cleanup.json"), cleared)
	selectSpace(0)
	open()
	check(settle().Status.State == "drain_prepared", "stale Drain consumed claim")
	save(filepath.Join(root, "stale-drain-recovery.json"), settle().Status)
	shot("native-stale-drain-recovered")
	must(os.Remove(filepath.Join(root, "drain.hold")))
	command("race-drain")
	check(settle().Status.State == "draining", "drain did not signal")
	time.Sleep(3 * time.Second)
	phase("running")
	shot("native-draining-after-three-seconds")
	phase("release")
	waitNative("drained actual exit", func() bool { command("race-read"); return settle().Status.State == "stopped" })
	phase("verify")
	shot("native-drained-stopped")
	command("race-retire")
	check(settle().Status.State == "none", "retirement failed")
	must(os.WriteFile(filepath.Join(root, "native-terminals-final.json"), run(filepath.Join(root, "bin", "hc"), "ls", "--all"), 0600))
	for _, o := range origins {
		appCall("do", "space.close", "--id", o["space"], "--confirmed", "true")
	}
	waitNative("owned anchor terminal closure", func() bool {
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
	fmt.Println("PASS native delayed Start/Drain callbacks: zero stale claims/signals, local cleanup; lost/replaced anchor after born: zero bind/admission, stopped read-only recovery; real commands drained")
}
