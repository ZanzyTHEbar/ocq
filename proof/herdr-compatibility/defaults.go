package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"ocq/internal/core"
)

// Real command boundary, no initialized store on missing-path observations and
// no dispatcher/agent execution. The helper must reach its native-owner gate,
// not fail because the adapter passed an empty state path.
func defaultPaths(root, bridge string) {
	for _, xdg := range []bool{true, false} {
		label := "home"
		if xdg {
			label = "xdg"
		}
		base := filepath.Join(root, label)
		must(os.MkdirAll(base, 0700))
		home := filepath.Join(base, "home")
		must(os.Mkdir(home, 0700))
		env := []string{}
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "OCQ_STATE_DIR=") && !strings.HasPrefix(v, "XDG_STATE_HOME=") && !strings.HasPrefix(v, "HOME=") && !strings.HasPrefix(v, "HC_TERMINAL_ID=") {
				env = append(env, v)
			}
		}
		env = append(env, "HOME="+home)
		state := filepath.Join(home, ".local", "state", "ocq")
		if xdg {
			env = append(env, "XDG_STATE_HOME="+base)
			state = filepath.Join(base, "ocq")
		}
		invoke := func(args ...string) ([]byte, error) {
			c := exec.Command(bridge, args...)
			c.Env = env
			return c.CombinedOutput()
		}
		space := "sp-default-path"
		handshake := func() map[string]any {
			b, e := invoke("compatibility", space, base)
			must(e)
			var v map[string]any
			must(json.Unmarshal(b, &v))
			return v
		}
		check(handshake()["queue_identity"] == "", "missing default queue appears ready")
		_, err := invoke("dispatcher-read", space, strings.Repeat("0", 64))
		check(err != nil, "missing dispatcher read succeeded")
		_, err = invoke("dispatcher-helper", strings.Repeat("1", 64))
		check(err != nil, "missing helper succeeded")
		check(!exists(state), "missing default path initialized")
		q, e := core.OpenQueue(state)
		must(e)
		must(q.Close())
		v := handshake()
		queue := v["queue_identity"].(string)
		check(len(queue) == 64, "default queue missing")
		args := []string{space, queue, "p" + strings.Repeat("1", 32), "t1", "tb" + strings.Repeat("2", 32), base}
		call := func(action, id string) core.CommandDispatcherStatus {
			a := append([]string{"dispatcher-" + action}, args...)
			if id != "" {
				a = append(a, id)
			}
			if action != "status" {
				a = append(a, "--mod-contract", v["protocol"].(string), "--mod-action", v["actions"].(map[string]any)["dispatcher-"+action].(string), "--mod-image", v["image_sha256"].(string), "--mod-context", v["bridge_context"].(string), "--mod-space", space, "--mod-folder", base, "--mod-queue", queue)
			}
			b, e := invoke(a...)
			check(e == nil, "default dispatcher "+action+": "+string(b))
			var s core.CommandDispatcherStatus
			must(json.Unmarshal(b, &s))
			return s
		}
		b, e := invoke("dispatcher-context", space)
		must(e)
		var scope core.CommandSubmissionScope
		must(json.Unmarshal(b, &scope))
		check(scope.Queue == queue, "default context queue changed")
		b, e = invoke("dispatcher-read", space, queue)
		must(e)
		check(call("status", "").State == "none", "unexpected default intent")
		p := call("prepare", "")
		check(p.State == "prepared", "default prepare failed")
		check(call("arm", p.ID).State == "armed", "default arm failed")
		check(call("attempt", p.ID).State == "unknown", "default attempt failed")
		b, e = invoke("dispatcher-helper", p.ID)
		check(e != nil && strings.Contains(string(b), "native terminal identity required"), "helper did not reach exact native ownership gate: "+string(b))
		save(filepath.Join(base, "result.json"), map[string]any{"queue": queue, "missing": "not_initialized", "control": "prepared_armed_attempted_once", "helper": "reached_native_identity_gate_no_launch"})
	}
}
