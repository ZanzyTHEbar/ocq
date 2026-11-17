// Real isolated socket acceptance: TLS, retained cancellation, dropped receipt
// recovery, cross-process producer push, scope refusal and owned endpoint stop.
// No dispatcher, agent, default store or mocked collaborator.
package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/sys/unix"
	"ocq/internal/core"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func check(ok bool, why string) {
	if !ok {
		panic(why)
	}
}
func save(path string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	must(err)
	must(os.WriteFile(path, b, 0600))
}

type client struct {
	c       *websocket.Conn
	welcome map[string]any
	n       int
	ctx     context.Context
}

func (c *client) write(kind string, request any) string {
	c.n++
	id := fmt.Sprint(c.n)
	frame := map[string]any{"type": kind, "id": id}
	if request != nil {
		frame["request"] = request
	}
	b, err := json.Marshal(frame)
	must(err)
	must(c.c.Write(c.ctx, websocket.MessageText, b))
	return id
}
func (c *client) read() map[string]any {
	_, b, err := c.c.Read(c.ctx)
	must(err)
	var f map[string]any
	must(json.Unmarshal(b, &f))
	return f
}
func (c *client) call(args []string) map[string]any {
	return c.callPayload(args, nil)
}
func (c *client) callPayload(args []string, payload *string) map[string]any {
	request := map[string]any{"connection_id": c.welcome["connection_id"], "args": args}
	if payload != nil {
		request["payload"] = *payload
	}
	id := c.write("command", request)
	for {
		f := c.read()
		if f["type"] == "command-result" && f["id"] == id {
			return f["result"].(map[string]any)
		}
	}
}
func (c *client) ok(args []string) map[string]any {
	r := c.call(args)
	check(r["code"] == float64(0), "socket command refused: "+args[0])
	var v map[string]any
	must(json.Unmarshal([]byte(r["stdout"].(string)), &v))
	return v
}

func main() {
	check(len(os.Args) == 3, "usage: push-proof BRIDGE FRESH_PRIVATE_ROOT")
	bridge, err := filepath.Abs(os.Args[1])
	must(err)
	root, err := filepath.Abs(os.Args[2])
	must(err)
	must(os.Mkdir(root, 0700))
	if stored := os.Getenv("OCQ_PROOF_STORED_STATE"); stored != "" {
		storedProjection(stored, os.Getenv("OCQ_PROOF_STORED_PROJECT"), root)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()
	state := filepath.Join(root, "queue")
	folder := root
	project := ""
	if repo := os.Getenv("OCQ_PROOF_REPO"); repo != "" {
		folder = repo
		project = "proj-socket-proof"
	}
	q, err := core.OpenQueue(state)
	must(err)
	job, err := q.SubmitCommand("socket-cancel-proof", root, []string{"/bin/true"}, 0)
	must(err)
	if project != "" {
		session, projectID := os.Getenv("OCQ_PROOF_SESSION"), os.Getenv("OCQ_PROOF_OPENCODE_PROJECT")
		check(session != "" && projectID != "", "explicit existing session context required")
		anchor, err := exec.CommandContext(ctx, "git", "-C", folder, "rev-parse", "HEAD").Output()
		must(err)
		zero := 0
		handoff, _ := json.Marshal(map[string]any{"version": 1, "kind": "handoff", "project_id": project, "source": "operator_import", "observed_at": nil, "intent": "Isolated socket admission acceptance; no execution/result/validation claim.", "decisions": []string{}, "risks": []string{}})
		raw, _ := json.Marshal(core.ProjectRegistrationRequest{Version: 1, Key: "socket-register", ProjectID: project, Label: "Socket acceptance", ExpectedRevision: &zero, Repo: folder, CommitAnchor: strings.TrimSpace(string(anchor)), ElephantSessionID: session, ExpectedOpenCodeProjectID: projectID, ExpectedOpenCodeDirectory: folder, AuthRef: "explicit-local-acceptance", HandoffManifest: handoff})
		registered, err := q.RegisterProject(raw)
		must(err)
		save(filepath.Join(root, "project-registration.json"), registered)
	}
	must(q.Close())
	if project != "" {
		raw, _ := json.Marshal(core.WorkPrepareRequest{Protocol: core.WorkProtocol, ProjectID: project, RequestID: "socket-task", Kind: "implementation", BindingRevision: "1", Brief: "Acceptance-only prepared task. Never dispatch or execute this fixture.", Profile: `{"agent":"build","provider_id":"openai","model_id":"gpt-6.1-sol","variant":"","memory_bytes":"0","permissions":[{"action":"*","resource":"*","effect":"deny"}]}`})
		prepared, err := core.PrepareProjectWork(state, raw)
		must(err)
		check(prepared.State == "prepared", "real private task journal")
		save(filepath.Join(root, "project-task.json"), prepared)
	}
	token := make([]byte, 32)
	_, err = rand.Read(token)
	must(err)
	secret := fmt.Sprintf("%x", token)
	must(os.WriteFile(filepath.Join(root, "token"), []byte(secret+"\n"), 0600))
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "isolated OCQ proof"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	must(err)
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	must(os.WriteFile(filepath.Join(root, "cert.pem"), cert, 0600))
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	must(err)
	must(os.WriteFile(filepath.Join(root, "key.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600))
	space := "sp" + strings.Repeat("3", 32)
	args := []string{"--mod-space", space, "--mod-folder", folder, "push-serve", "--queue-dir", state, "--token-file", filepath.Join(root, "token"), "--tls-cert", filepath.Join(root, "cert.pem"), "--tls-key", filepath.Join(root, "key.pem"), "--allow-actions"}
	save(filepath.Join(root, "endpoint-intent.json"), map[string]any{"bridge": bridge, "argv": args, "owner_pid": os.Getpid(), "scope": "private queue only"})
	// Receipt is written by the exact owned child BEFORE it execs the listener.
	script := `umask 077; printf '{"pid":%s,"start_ticks":"%s"}\n' "$$" "$(awk '{print $22}' /proc/$$/stat)" > "$1"; shift; exec "$@"`
	cmd := exec.CommandContext(ctx, "/bin/sh", append([]string{"-c", script, "push-proof", filepath.Join(root, "endpoint-owner.json"), bridge}, args...)...)
	out, err := cmd.StdoutPipe()
	must(err)
	log, err := os.OpenFile(filepath.Join(root, "endpoint.stderr"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	must(err)
	defer log.Close()
	cmd.Stderr = log
	must(cmd.Start())
	stopped := false
	defer func() {
		if !stopped {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			_ = cmd.Wait()
		}
	}()
	line, err := bufio.NewReader(out).ReadBytes('\n')
	must(err)
	var endpoint map[string]any
	must(json.Unmarshal(line, &endpoint))
	url := endpoint["endpoint"].(string)
	check(strings.HasPrefix(url, "wss://127.0.0.1:"), "TLS loopback endpoint")
	pool := x509.NewCertPool()
	check(pool.AppendCertsFromPEM(cert), "private TLS trust")
	httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	_, _, err = websocket.Dial(ctx, url, nil)
	check(err != nil, "untrusted certificate must refuse")
	_, _, err = websocket.Dial(ctx, strings.Replace(url, "127.0.0.1", "localhost", 1), &websocket.DialOptions{HTTPClient: httpClient})
	check(err != nil, "trusted certificate with wrong hostname must refuse")
	dial := func(folder, credential string) *client {
		conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: httpClient})
		must(err)
		conn.SetReadLimit(core.ObservationResponseBytes + 16384)
		b, _ := json.Marshal(map[string]any{"protocol": "ocq-push-v1", "type": "auth", "token": credential, "space": space, "folder": folder})
		must(conn.Write(ctx, websocket.MessageText, b))
		return &client{c: conn, ctx: ctx}
	}
	for _, invalid := range []struct{ folder, token string }{{folder, "0" + secret[1:]}, {folder + "-wrong", secret}} {
		if invalid.token == secret && invalid.folder == folder {
			invalid.token = "1" + secret[1:]
		}
		c := dial(invalid.folder, invalid.token)
		_, _, err = c.c.Read(ctx)
		check(err != nil, "auth/scope refusal")
		c.c.CloseNow()
	}
	open := func() *client {
		c := dial(folder, secret)
		c.welcome = c.read()
		check(c.welcome["mutations"] == true, "explicit action capability")
		return c
	}
	c := open()
	defer c.c.CloseNow()
	d := c.welcome["descriptor"].(map[string]any)
	pin := []string{"--mod-contract", "ocq-herdr-local-v1", "--mod-image", d["image_sha256"].(string), "--mod-context", d["bridge_context"].(string), "--mod-space", space, "--mod-folder", folder, "--mod-queue", d["queue_identity"].(string)}
	if project != "" && os.Getenv("OCQ_PROOF_AGENT_ADMISSION") == "1" {
		preview := c.ok([]string{"work-preview", "--work-observation-context", d["observation_context"].(string), "--", space, d["queue_identity"].(string), project, "socket-task", "local", folder, d["observation_store_context"].(string), d["image_sha256"].(string)})
		save(filepath.Join(root, "task-retained-preview.json"), preview)
		check(preview["status"].(map[string]any)["state"] == "prepared", "retained task preview")
		a := append([]string{"work-confirm"}, pin...)
		a = append(a, "--mod-action", "ocq-work-v1", "--work-observation-context", d["observation_context"].(string), "--", space, d["queue_identity"].(string), project, "socket-task", "local", folder, d["observation_store_context"].(string), d["image_sha256"].(string), preview["preview_sha256"].(string))
		admitted := c.ok(a)
		save(filepath.Join(root, "task-confirmation.json"), admitted)
		check(admitted["state"] == "recorded_observed" && admitted["job"].(map[string]any)["state"] == "queued", "socket task admission, never execution")
		recovered := c.ok([]string{"work-recover", "--", space, d["queue_identity"].(string), project, "socket-task"})
		check(recovered["receipt"].(map[string]any)["job_id"] == admitted["receipt"].(map[string]any)["job_id"], "exact task receipt recovery")
		stop := exec.CommandContext(ctx, strings.TrimSuffix(bridge, "ocq-herdr")+"ocq", "--state-dir", state, "cancel", admitted["job"].(map[string]any)["job_id"].(string))
		must(stop.Run()) // Owned queued fixture only, no dispatch.
	}
	submission := func(mode string, payload *string, extra ...string) map[string]any {
		a := []string{"command-submit-" + mode, space, d["queue_identity"].(string)}
		a = append(a, extra...)
		a = append(a, pin...)
		a = append(a, "--mod-action", "command-submit/"+mode+"/journal-v1")
		result := c.callPayload(a, payload)
		check(result["code"] == float64(0), "command submission "+mode)
		var out map[string]any
		must(json.Unmarshal([]byte(result["stdout"].(string)), &out))
		return out
	}
	gate := filepath.Join(root, "log-gate")
	must(unix.Mkfifo(gate, 0600))
	logScript := `printf 'first-log-chunk\n'; read ignored < "$1"; head -c 70000 /dev/zero | tr '\000' x; printf '\nlast-log-chunk\n'`
	draftBytes, err := json.Marshal(map[string]any{"key": "native-socket-admission", "executable": "/bin/sh", "arguments": []string{"-c", logScript, "socket-log-proof", gate}, "cwd": root, "memory_bytes": "0"})
	must(err)
	draftInput := string(draftBytes)
	launch := strings.Repeat("4", 64)
	drafted := submission("draft", &draftInput, launch)
	draftReview := drafted["review"].(map[string]any)
	raw := draftReview["raw"].(string)
	preparedAdmission := submission("receive", &raw, launch)
	admissionID := preparedAdmission["status"].(map[string]any)["id"].(string)
	retained := submission("preview", nil, admissionID, launch)
	check(retained["review"].(map[string]any)["raw"] == raw, "exact retained bytes reviewed before arm")
	submission("arm", nil, admissionID)
	admitted := submission("attempt", nil, admissionID)
	save(filepath.Join(root, "socket-admission.json"), admitted)
	check(admitted["state"] == "completed" && admitted["receipt"] != nil, "original immutable admission receipt")
	subscribe := func(c *client) map[string]any {
		c.write("subscribe", map[string]any{"protocol": "ocq-observe-v1", "queue_identity": d["queue_identity"], "store_context": d["observation_store_context"], "bridge_context": d["observation_context"], "selectors": []any{map[string]any{"slot": "jobs", "kind": "queue-jobs", "cursor": "0"}}})
		f := c.read()
		check(f["type"] == "snapshot", "initial coherent pushed snapshot")
		return f
	}
	first := subscribe(c)
	save(filepath.Join(root, "initial-snapshot.json"), first)
	if project != "" {
		c.write("subscribe", map[string]any{"protocol": "ocq-observe-v1", "queue_identity": d["queue_identity"], "store_context": d["observation_store_context"], "bridge_context": d["observation_context"], "selectors": []any{map[string]any{"slot": "project", "kind": "board", "project_id": project}}, "work": map[string]any{"project_id": project, "fingerprint": ""}})
		before := c.read()
		check(before["type"] == "snapshot", "real work journal subscription")
		profile := filepath.Join(root, "work-profile.json")
		must(os.WriteFile(profile, []byte(`{"agent":"build","provider_id":"openai","model_id":"gpt-6.1-sol","variant":"","memory_bytes":"0","permissions":[{"action":"*","resource":"*","effect":"deny"}]}`), 0600))
		brief := filepath.Join(root, "brief.txt")
		must(os.WriteFile(brief, []byte("Acceptance-only journal update. Never dispatch this fixture."), 0600))
		writer := exec.CommandContext(ctx, strings.TrimSuffix(bridge, "ocq-herdr")+"ocq", "--state-dir", state, "project", "work", "prepare", "--request-id", "socket-task-two", "--binding-revision", "1", "--profile-file", profile, "--brief-file", brief, project)
		output, err := writer.CombinedOutput()
		must(err)
		must(os.WriteFile(filepath.Join(root, "journal-writer.json"), output, 0600))
		var pushed map[string]any
		for {
			pushed = c.read()
			b, _ := json.Marshal(pushed)
			if strings.Contains(string(b), "socket-task-two") {
				break
			}
		}
		check(before["snapshot"].(map[string]any)["revision"] == pushed["snapshot"].(map[string]any)["revision"], "filesystem journal update separate from SQL revision")
		save(filepath.Join(root, "journal-push.json"), pushed)
		subscribe(c)
	}
	detail := c.ok([]string{"queue-job", job.ID})
	row := detail["job"].(map[string]any)
	base := []string{space, d["queue_identity"].(string), job.ID, row["sequence"].(string), "queued"}
	action := func(name string, extra ...string) []string {
		a := append([]string{"queued-cancel-" + name}, base...)
		a = append(a, extra...)
		a = append(a, pin...)
		return append(a, "--mod-action", "queued-cancel/"+name+"/journal-v1")
	}
	prepared := c.ok(action("prepare"))
	save(filepath.Join(root, "prepared.json"), prepared)
	intent := prepared["intent_id"].(string)
	armed := c.ok(action("arm", intent))
	save(filepath.Join(root, "armed.json"), armed)
	attemptID := c.write("command", map[string]any{"connection_id": c.welcome["connection_id"], "args": action("attempt", intent)})
	for {
		f := c.read()
		if f["type"] == "command-received" && f["id"] == attemptID {
			break
		}
	}
	// Discard the response intentionally; do not resend the mutation.
	must(c.c.Close(websocket.StatusNormalClosure, "drop receipt acceptance"))
	c = open()
	defer c.c.CloseNow()
	fresh := subscribe(c)
	// A dropped command reply can race exact status recovery. Stay subscribed
	// until committed state arrives; never reissue the mutation or poll reads.
	for {
		b, _ := json.Marshal(fresh)
		if strings.Contains(string(b), `"state":"cancelled"`) {
			break
		}
		fresh = c.read()
	}
	recovered := c.ok(append([]string{"queued-cancel-status"}, base...))
	save(filepath.Join(root, "recovered.json"), recovered)
	check(recovered["state"] == "cancelled_observed" && recovered["intent_id"] == intent, "exact receipt recovery after disconnect")
	// A real separate CLI producer, not a direct SQL fixture or mock.
	cli := strings.TrimSuffix(bridge, "ocq-herdr") + "ocq"
	producer := exec.CommandContext(ctx, cli, "--state-dir", state, "submit", "--key", "separate-producer", "--cwd", root, "--", "/bin/true")
	produced, err := producer.CombinedOutput()
	must(err)
	must(os.WriteFile(filepath.Join(root, "producer.json"), produced, 0600))
	pushed := c.read()
	check(pushed["type"] == "snapshot", "cross-process producer pushed without a client read")
	save(filepath.Join(root, "producer-push.json"), pushed)
	check(c.call([]string{"client-open"})["code"] == float64(1), "native client launch remains refused")
	if project != "" {
		byteAdapters(c, d, pin, state, root, project)
	}
	if mainRepo := os.Getenv("OCQ_PROOF_MAIN_REPO"); mainRepo != "" {
		mainJournal(ctx, c, d, bridge, state, root, mainRepo)
	}
	readOnlyControls(ctx, bridge, state, root, folder, project, space)
	continuousLog(ctx, c, d, bridge, state, root, gate, admitted["receipt"].(map[string]any)["job"].(map[string]any)["id"].(string))
	staleControls(ctx, open, d, folder, root)
	// Bounded real slow reader: eight selected slots and a finite control burst,
	// no consumption of the resulting state frames. The server must close on its
	// five-second write bound, not accumulate an unbounded projection queue.
	fill, err := core.OpenQueue(state)
	must(err)
	for n := 0; n < 20; n++ {
		_, err = fill.SubmitCommand(fmt.Sprintf("slow-%d", n), root, []string{"/bin/true"}, 0)
		must(err)
	}
	must(fill.Close())
	normalHTTP := httpClient
	var slowTCP net.Conn
	httpClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err == nil {
			slowTCP = conn
		}
		return conn, err
	}}}
	slow := open()
	httpClient = normalHTTP
	selectors := []any{}
	for n := 0; n < 8; n++ {
		selectors = append(selectors, map[string]any{"slot": fmt.Sprintf("slow-%d", n), "kind": "queue-jobs", "cursor": "0"})
	}
	began := time.Now()
	for n := 1; n <= 3000; n++ {
		frame, _ := json.Marshal(map[string]any{"type": "subscribe", "id": fmt.Sprint(n), "request": map[string]any{"protocol": "ocq-observe-v1", "queue_identity": d["queue_identity"], "store_context": d["observation_store_context"], "bridge_context": d["observation_context"], "selectors": selectors}})
		writeCtx, stop := context.WithTimeout(ctx, 12*time.Second)
		err = slow.c.Write(writeCtx, websocket.MessageText, frame)
		stop()
		if err != nil {
			break
		}
	}
	check(!errors.Is(err, context.DeadlineExceeded), "client write deadline must not create slow-reader success")
	// Wait for peer half-close without consuming any application frames. This
	// is a real kernel notification, not a timed read/drain that accidentally
	// unblocks the server before its backpressure deadline.
	if err == nil {
		control, e := slowTCP.(*net.TCPConn).SyscallConn()
		must(e)
		closed := false
		var pollErr error
		must(control.Control(func(fd uintptr) {
			deadline := time.Now().Add(15 * time.Second)
			for time.Now().Before(deadline) {
				fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLRDHUP}}
				_, pollErr = unix.Poll(fds, int(time.Until(deadline).Milliseconds()))
				if errors.Is(pollErr, unix.EINTR) {
					continue
				}
				closed = fds[0].Revents&(unix.POLLRDHUP|unix.POLLHUP) != 0
				break
			}
		}))
		must(pollErr)
		check(closed, "server closes stalled socket without a client read")
	}
	slow.c.CloseNow()
	check(time.Since(began) < 20*time.Second, "slow consumer settles within bounded server work/write interval")
	check(time.Since(began) >= 4*time.Second, "slow-reader evidence must reach the server deadline, not a client framing refusal")
	save(filepath.Join(root, "slow-consumer.json"), map[string]any{"settled": true, "elapsed_ms": time.Since(began).Milliseconds(), "slots": 8, "burst_limit": 3000, "proof": "kernel peer half-close; no application reads"})
	check(websocket.CloseStatus(err) != websocket.StatusPolicyViolation, "slow reader must not pass because a subscription was invalid")
	replaceQueue(ctx, open, d, state, root)
	_ = c.c.CloseNow()
	must(cmd.Process.Signal(syscall.SIGTERM))
	must(cmd.Wait())
	stopped = true
	_, err = os.Stat(fmt.Sprintf("/proc/%d", cmd.Process.Pid))
	check(os.IsNotExist(err), "endpoint PID reaped")
	save(filepath.Join(root, "RESULT.json"), map[string]any{"status": "pass", "endpoint_pid": cmd.Process.Pid, "reaped": true, "tls": "private CA; untrusted refused", "command": "prepare arm attempt; response discarded; exact receipt recovered without replay", "subscription": "one initial plus explicit reconnect; cross-process producer pushes", "agent_admission_explicit_opt_in": os.Getenv("OCQ_PROOF_AGENT_ADMISSION") == "1", "native_launch": "refused", "native_gpui": "separate acceptance required"})
	fmt.Println("PASS real TLS socket command/push/recovery; owned endpoint reaped:", root)
}
