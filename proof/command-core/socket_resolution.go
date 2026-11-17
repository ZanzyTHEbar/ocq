package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/coder/websocket"
	"ocq/internal/core"
)

// Reuses only this scenario's real crash/exit ownership proof. No historical
// queue, fake terminal agent, direct SQL mutation or mocked collaborator.
func (s *scenario) resolveThroughSocket(bridge, id string) {
	token := make([]byte, 32)
	_, err := rand.Read(token)
	must(err)
	secret := fmt.Sprintf("%x", token)
	tokenFile := filepath.Join(s.root, "socket-token")
	must(os.WriteFile(tokenFile, []byte(secret+"\n"), 0600))
	space := "sp" + strings.Repeat("6", 32)
	args := []string{"--mod-space", space, "--mod-folder", s.root, "push-serve", "--queue-dir", s.state, "--token-file", tokenFile, "--allow-actions"}
	intent, _ := json.Marshal(map[string]any{"argv": append([]string{bridge}, args...), "target_job": id, "scope": "this fresh command-only acceptance; exact owned orphan already exited"})
	save(filepath.Join(s.root, "socket-resolution-endpoint-intent.json"), intent)
	cmd := exec.CommandContext(s.ctx, bridge, args...)
	output, err := cmd.StdoutPipe()
	must(err)
	log, err := os.OpenFile(filepath.Join(s.root, "socket-endpoint.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	must(err)
	defer log.Close()
	cmd.Stderr = log
	must(cmd.Start())
	owner := who(cmd.Process.Pid)
	b, _ := json.Marshal(owner)
	save(filepath.Join(s.root, "socket-endpoint-owner.json"), b)
	stopped := false
	defer func() {
		if !stopped {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			_ = cmd.Wait()
		}
	}()
	line, err := bufio.NewReader(output).ReadBytes('\n')
	must(err)
	var endpoint struct {
		Endpoint string `json:"endpoint"`
	}
	must(json.Unmarshal(line, &endpoint))
	conn, _, err := websocket.Dial(s.ctx, endpoint.Endpoint, nil)
	must(err)
	defer conn.CloseNow()
	conn.SetReadLimit(core.ObservationResponseBytes + 16384)
	write := func(v any) {
		raw, err := json.Marshal(v)
		must(err)
		must(conn.Write(s.ctx, websocket.MessageText, raw))
	}
	read := func() map[string]any {
		_, raw, err := conn.Read(s.ctx)
		must(err)
		var frame map[string]any
		must(json.Unmarshal(raw, &frame))
		return frame
	}
	write(map[string]any{"protocol": "ocq-push-v1", "type": "auth", "token": secret, "space": space, "folder": s.root})
	welcome := read()
	check(welcome["type"] == "welcome" && welcome["mutations"] == true, "explicit socket resolution capability")
	d := welcome["descriptor"].(map[string]any)
	serial := 0
	call := func(args []string, payload *string) map[string]any {
		serial++
		request := map[string]any{"connection_id": welcome["connection_id"], "args": args}
		if payload != nil {
			request["payload"] = *payload
		}
		rid := fmt.Sprint(serial)
		write(map[string]any{"type": "command", "id": rid, "request": request})
		for {
			f := read()
			if f["type"] != "command-result" || f["id"] != rid {
				continue
			}
			reply := f["result"].(map[string]any)
			check(reply["code"] == float64(0), "guarded socket resolution refused")
			var out map[string]any
			must(json.Unmarshal([]byte(reply["stdout"].(string)), &out))
			return out
		}
	}
	job := call([]string{"queue-job", id}, nil)["job"].(map[string]any)
	check(job["state"] == "unknown" && job["kind"] == "command", "real exact unknown command, not agent")
	target := core.CommandResolutionTarget{Machine: "local", Space: space, Queue: d["queue_identity"].(string), Job: id, Sequence: job["sequence"].(string)}
	reason := job["reason"].(string)
	raw, err := json.Marshal(core.CommandResolutionPayload{Scope: core.CommandResolutionScope{CommandResolutionTarget: target, Kind: "command", State: "unknown", ReasonDigest: fmt.Sprintf("%x", sha256.Sum256([]byte(reason)))}, Reason: reason, Evidence: "Real marked foreground command exited after owned release; exact retained PID/start absent; output and marker inspected by acceptance. No deliverable validation claim."})
	must(err)
	exact := string(raw)
	base := []string{space, target.Queue, id, target.Sequence}
	pin := []string{"--mod-contract", "ocq-herdr-local-v1", "--mod-space", space, "--mod-folder", s.root, "--mod-queue", target.Queue, "--mod-image", d["image_sha256"].(string), "--mod-context", d["bridge_context"].(string)}
	action := func(mode string, payload *string, extra ...string) map[string]any {
		args := append([]string{"command-resolve-" + mode}, base...)
		args = append(args, extra...)
		args = append(args, pin...)
		args = append(args, "--mod-action", "command-resolve/"+mode+"/journal-v1")
		return call(args, payload)
	}
	launch := strings.Repeat("7", 64)
	prepared := action("receive", &exact, launch)["status"].(map[string]any)
	digest := prepared["payload_sha256"].(string)
	preview := action("preview", nil, digest, launch)
	check(preview["exact"] == exact, "exact resolution evidence retained and reviewed before arm")
	armed := action("arm", nil, digest)
	check(armed["state"] == "armed", "durable command resolution arm")
	completed := action("attempt", nil, digest)
	check(completed["state"] == "resolved_observed", "exact resolved receipt observed, not job success")
	recovered := call(append([]string{"command-resolve-status"}, base...), nil)
	check(recovered["state"] == "resolved_observed", "receipt-first status; no repeat attempt")
	reader, err := core.OpenReader(s.state)
	must(err)
	_, publicationErr := reader.ResultPublicationContext(space, "proj-command-only-proof", id)
	must(reader.Close())
	check(publicationErr != nil, "command must not acquire an agent result publication context")
	publication, _ := json.MarshalIndent(map[string]any{"status": "refusal_verified", "job": id, "job_kind": "command", "result_publication_context_available": false, "reason": "Existing publication ownership contract requires an agent receipt, project linkage and recorded agent location; operator command resolution is not an agent deliverable/result", "positive_publication_acceptance": "not_run; no agent proof fabricated"}, "", "  ")
	save(filepath.Join(s.root, "publication-command-boundary.json"), publication)
	result, _ := json.MarshalIndent(map[string]any{"status": "pass", "job": id, "retained_preview_exact": true, "prepared": prepared, "armed": armed, "completed": completed, "recovered": recovered, "validation": "not_run", "causality": "operator reconciliation only, never command success or agent proof"}, "", "  ")
	save(filepath.Join(s.root, "socket-resolution-result.json"), result)
	must(conn.Close(websocket.StatusNormalClosure, "acceptance complete"))
	must(cmd.Process.Signal(syscall.SIGTERM))
	must(cmd.Wait())
	stopped = true
	check(!live(owner), "owned socket endpoint reaped")
	s.note(fmt.Sprintf("PASS socket resolution exact preview/arm/one attempt/matched receipt; endpoint pid=%d start=%d exited", owner.PID, owner.Start))
}
