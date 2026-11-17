package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func readOnlyControls(ctx context.Context, bridge, state, root, folder, project, space string) {
	script := os.Getenv("OCQ_PROOF_READ_CONTROLS")
	if script == "" || project == "" {
		return
	}
	args := []string{"--mod-space", space, "--mod-folder", folder, "push-serve", "--queue-dir", state, "--token-file", filepath.Join(root, "token")}
	save(filepath.Join(root, "readonly-endpoint-intent.json"), map[string]any{"argv": append([]string{bridge}, args...), "allow_actions": false})
	cmd := exec.CommandContext(ctx, bridge, args...)
	output, err := cmd.StdoutPipe()
	must(err)
	log, err := os.OpenFile(filepath.Join(root, "readonly-endpoint.stderr"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	must(err)
	defer log.Close()
	cmd.Stderr = log
	must(cmd.Start())
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", cmd.Process.Pid))
	must(err)
	fields := strings.Fields(string(stat)[strings.LastIndex(string(stat), ") ")+2:])
	save(filepath.Join(root, "readonly-endpoint-owned.json"), map[string]any{"pid": cmd.Process.Pid, "start_ticks": fields[19]})
	stopped := false
	defer func() {
		if !stopped {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			_ = cmd.Wait()
		}
	}()
	line, err := bufio.NewReader(output).ReadBytes('\n')
	must(err)
	var endpoint map[string]any
	must(json.Unmarshal(line, &endpoint))
	config := filepath.Join(root, "readonly-controls-config.json")
	save(config, map[string]any{"endpoint": endpoint["endpoint"], "token_file": filepath.Join(root, "token"), "space": space, "folder": folder, "project": project, "prepared_main": os.Getenv("OCQ_PROOF_MAIN_REPO") != ""})
	source := os.Getenv("OCQ_PROOF_COMPATIBILITY_SOURCE")
	check(source != "", "explicit production compatibility module required")
	node := exec.CommandContext(ctx, "node", "--experimental-vm-modules", script, config, source, filepath.Join(root, "readonly-controls-result.json"))
	result, err := node.CombinedOutput()
	must(os.WriteFile(filepath.Join(root, "readonly-controls.log"), result, 0600))
	must(err)
	must(cmd.Process.Signal(syscall.SIGTERM))
	must(cmd.Wait())
	stopped = true
	save(filepath.Join(root, "readonly-endpoint-stopped.json"), map[string]any{"pid": cmd.Process.Pid, "reaped": true})
}
