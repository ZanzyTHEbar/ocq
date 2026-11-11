package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Proof-only precondition: via runs inside the owned origin core's existing
// approved guard. Inspect that ancestry; never install a second listener or
// offer an unguarded fallback. No protocol/payload content is recorded.
func inheritedTransport(root string) {
	guardBytes, e := os.ReadFile(filepath.Join(root, "guard-path"))
	must(e)
	guard := strings.TrimSpace(string(guardBytes))
	var owner struct {
		PID int `json:"pid"`
	}
	load(filepath.Join(root, "cleanup-owner.json"), &owner)
	status, e := os.ReadFile("/proc/self/status")
	must(e)
	flags := map[string]string{}
	for _, line := range strings.Split(string(status), "\n") {
		for _, key := range []string{"NoNewPrivs:", "Seccomp:", "Seccomp_filters:"} {
			if strings.HasPrefix(line, key) {
				flags[strings.TrimSuffix(key, ":")] = strings.TrimSpace(strings.TrimPrefix(line, key))
			}
		}
	}
	check(flags["NoNewPrivs"] == "1" && flags["Seccomp"] == "2", "inherited filter required; no fallback")
	found := false
	pid := os.Getpid()
	chain := []map[string]any{}
	for n := 0; n < 32 && pid > 1 && pid != owner.PID; n++ {
		base := fmt.Sprintf("/proc/%d", pid)
		b, e := os.ReadFile(filepath.Join(base, "stat"))
		must(e)
		text := string(b)
		end := strings.LastIndex(text, ") ")
		check(end > 0, "invalid ancestry stat")
		fields := strings.Fields(text[end+2:])
		check(len(fields) > 19, "invalid ancestry identity")
		exe, e := os.Readlink(filepath.Join(base, "exe"))
		must(e)
		chain = append(chain, map[string]any{"pid": pid, "start": fields[19], "executable": exe})
		if exe == guard {
			found = true
			break
		}
		pid, e = strconv.Atoi(fields[1])
		must(e)
	}
	check(found, "approved origin guard ancestor required; no unguarded transport")
	save(filepath.Join(root, fmt.Sprintf("inherited-transport-%d.json", os.Getpid())), map[string]any{"flags": flags, "guard": guard, "ancestry": chain, "second_listener": false})
}
