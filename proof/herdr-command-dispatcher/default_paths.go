package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func nativeDefaultChecks(root, state string) {
	bridge := os.Getenv("OCQ_RACE_BRIDGE")
	call := func(base string, args ...string) ([]byte, error) {
		c := exec.Command(bridge, args...)
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "OCQ_STATE_DIR=") && !strings.HasPrefix(v, "XDG_STATE_HOME=") {
				c.Env = append(c.Env, v)
			}
		}
		c.Env = append(c.Env, "XDG_STATE_HOME="+base)
		return c.CombinedOutput()
	}
	space := "sp-default-canonical"
	var alias, canonical map[string]any
	b, e := call(filepath.Join(root, "default-state"), "compatibility", space, root)
	must(e)
	must(json.Unmarshal(b, &alias))
	b, e = call(filepath.Dir(state), "compatibility", space, root)
	must(e)
	must(json.Unmarshal(b, &canonical))
	check(alias["queue_identity"] != "" && alias["queue_identity"] == canonical["queue_identity"] && alias["bridge_context"] == canonical["bridge_context"], "ancestor alias changed validated compatibility identity")
	refusal := filepath.Join(root, "refuse-final-link")
	must(os.Mkdir(refusal, 0700))
	must(os.Symlink(state, filepath.Join(refusal, "ocq")))
	for _, base := range []string{refusal, filepath.Join(root, "missing-default")} {
		b, e := call(base, "compatibility", space, root)
		must(e)
		var absent struct {
			Queue string `json:"queue_identity"`
		}
		must(json.Unmarshal(b, &absent))
		check(absent.Queue == "", "unsupported store presented ready")
		for _, args := range [][]string{{"dispatcher-read", space, alias["queue_identity"].(string)}, {"dispatcher-helper", strings.Repeat("1", 64)}} {
			b, e := call(base, args...)
			check(e != nil, "unsupported dispatcher default accepted: "+string(b))
		}
	}
	check(!exists(filepath.Join(root, "missing-default")), "missing default store initialized")
	s, e := os.Lstat(filepath.Join(refusal, "ocq"))
	must(e)
	check(s.Mode()&os.ModeSymlink != 0, "final symlink altered")
	check(!exists(filepath.Join(state, "herdr-command-dispatcher")), "refusal created dispatcher intent")
	save(filepath.Join(root, "default-context.json"), map[string]any{"alias": alias, "canonical": canonical, "state": state, "ocq_state_dir": "unset", "final_symlink": "refused_untouched", "missing": "not_initialized"})
}
