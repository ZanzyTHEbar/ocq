package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// Bounded capture only. Success bytes remain in memory; failure records never
// contain command arguments/private input. Only native/core control errors have
// a redacted textual projection; execution/protocol streams retain counts only.
type commandOutput struct {
	bytes        []byte
	total, limit int
	truncated    bool
}

func (b *commandOutput) Write(p []byte) (int, error) {
	n := len(p)
	b.total += n
	remaining := b.limit - len(b.bytes)
	if len(p) > remaining {
		b.truncated = true
		p = p[:remaining]
	}
	b.bytes = append(b.bytes, p...)
	return n, nil
}

func diagnosticText(b []byte) string {
	if len(b) > 8192 {
		b = b[:8192]
	}
	if !utf8.Valid(b) {
		return "[non-UTF8 output omitted]"
	}
	lines := strings.Split(string(b), "\n")
	for i, line := range lines {
		lower := strings.ToLower(line)
		for _, marker := range []string{"private_", "capability", "canonical", "payload", "token", "secret", "password", "authorization", "bearer", "prompt", "ocq-compose-v1"} {
			if strings.Contains(lower, marker) {
				lines[i] = "[sensitive diagnostic line omitted]"
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}

func retainCommandFailure(c *exec.Cmd, bin string, args []string, out, stderr *commandOutput, err error) string {
	route := ""
	if len(args) > 0 {
		route = args[0]
	}
	nativeControl := filepath.Base(bin) == "herdr-app" && len(args) > 1 && args[0] == "do"
	listControl := filepath.Base(bin) == "hc" && len(args) > 0 && (args[0] == "ls" || args[0] == "get" || args[0] == "machine" || (args[0] == "server" && len(args) > 1 && args[1] == "status"))
	control := nativeControl || listControl
	action := ""
	if nativeControl {
		action = args[1]
	} else if listControl {
		action = args[0]
	}
	stdout, errText := "[content omitted: non-native-control stream]", "[content omitted: non-native-control stream]"
	if control {
		// Control output is either an error envelope or a native plain error,
		// never a private bridge exchange. Preserve only the error projection.
		var envelope map[string]json.RawMessage
		if json.Unmarshal(out.bytes, &envelope) == nil {
			if body, ok := envelope["error"]; ok {
				stdout = diagnosticText(body)
			} else {
				stdout = "[JSON without error field omitted]"
			}
		} else if nativeControl {
			stdout = diagnosticText(out.bytes)
		} else {
			stdout = "[terminal-list stdout omitted]"
		}
		errText = diagnosticText(stderr.bytes)
	}
	code := -1
	classification := "start_or_wait_error"
	signal := 0
	if c.ProcessState != nil {
		code = c.ProcessState.ExitCode()
		classification = "exit"
		if s, ok := c.ProcessState.Sys().(syscall.WaitStatus); ok && s.Signaled() {
			classification = "signal"
			signal = int(s.Signal())
		}
	}
	path := filepath.Join(os.Getenv("PROBE_ROOT"), fmt.Sprintf("command-failure-%d-%d.json", os.Getpid(), time.Now().UnixNano()))
	save(path, map[string]any{"binary": bin, "route": route, "action": action, "classification": classification, "exit_code": code, "signal": signal, "error_type": fmt.Sprintf("%T", err), "stdout_bytes": out.total, "stderr_bytes": stderr.total, "stdout_truncated": out.truncated || len(out.bytes) > 8192, "stderr_truncated": stderr.truncated, "stdout_error": stdout, "stderr_error": errText, "argv_logged": false, "private_streams_logged": false})
	return path
}

// Private, parser-only preflight. Unknown options are rejected before core lookup;
// no existing/shared core request, shell, network or execution payload is involved.
func verifyParserFailureCapture(root, hc string) {
	before, e := filepath.Glob(filepath.Join(root, "command-failure-*.json"))
	must(e)
	refused := false
	func() { defer func() { refused = recover() != nil }(); run(hc, "ls", "--ocq-invalid-diagnostic-flag") }()
	check(refused, "HC parser diagnostic unexpectedly accepted")
	after, e := filepath.Glob(filepath.Join(root, "command-failure-*.json"))
	must(e)
	check(len(after) == len(before)+1, "HC parser diagnostic not retained")
	seen := map[string]bool{}
	for _, p := range before {
		seen[p] = true
	}
	for _, p := range after {
		if !seen[p] {
			var v map[string]any
			load(p, &v)
			text, _ := v["stderr_error"].(string)
			// HC emits JSON when piped (including parser failures), not its
			// interactive help text. Require that exact real error envelope.
			var envelope struct {
				Error struct{ Kind, Message, Hint string }
			}
			must(json.Unmarshal([]byte(text), &envelope))
			check(v["classification"] == "exit" && v["exit_code"] == float64(2) && envelope.Error.Kind == "usage" && envelope.Error.Message == "unexpected argument '--ocq-invalid-diagnostic-flag' found" && envelope.Error.Hint == "see `hc --help`", "actual bounded HC parser error missing")
			save(filepath.Join(root, "parser-diagnostic-check.json"), map[string]any{"failure_record": p, "exit_code": 2, "parser_text_retained": true, "no_core_operation": true})
		}
	}
	check(diagnosticText([]byte("harmless parser error\nAuthorization: secret-value\npayload: private-value\nUsage: hc ls")) == "harmless parser error\n[sensitive diagnostic line omitted]\n[sensitive diagnostic line omitted]\nUsage: hc ls", "sensitive diagnostic redaction failed")
}

// One real native error-boundary check: an explicitly nonexistent target cannot
// fall back to the active space (installed command contract). No space is made.
func verifyNativeFailureCapture(root, app string) {
	before, e := filepath.Glob(filepath.Join(root, "command-failure-*.json"))
	must(e)
	panicked := false
	func() {
		defer func() { panicked = recover() != nil }()
		run(app, "do", "space.select", "--id", "sp-absent-diagnostic-target")
	}()
	check(panicked, "nonexistent native selection unexpectedly succeeded")
	after, e := filepath.Glob(filepath.Join(root, "command-failure-*.json"))
	must(e)
	check(len(after) == len(before)+1, "failed native result was not retained")
	seen := map[string]bool{}
	for _, p := range before {
		seen[p] = true
	}
	for _, p := range after {
		if !seen[p] {
			var v map[string]any
			load(p, &v)
			check(v["action"] == "space.select" && v["classification"] == "exit" && v["exit_code"] != float64(0) && v["argv_logged"] == false, "native error classification missing")
			useful := false
			for _, key := range []string{"stdout_error", "stderr_error"} {
				s, _ := v[key].(string)
				s = strings.TrimSpace(s)
				if s != "" && !strings.HasPrefix(s, "[") {
					useful = true
				}
			}
			check(useful, "native error text missing or omitted")
			save(filepath.Join(root, "expected-diagnostic.json"), map[string]any{"failure_record": p, "expected_refusal": true, "space_created": false})
		}
	}
}
