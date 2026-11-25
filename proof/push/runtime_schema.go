package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"
	_ "modernc.org/sqlite"
	"ocq/internal/core"
)

type runtimeProofOwner struct {
	PID   int    `json:"pid"`
	Start string `json:"start_ticks"`
	Boot  string `json:"boot_id"`
}

func runtimeProofIdentity(pid int) runtimeProofOwner {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	must(err)
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	must(err)
	return runtimeProofOwner{pid, strings.Fields(string(raw)[strings.LastIndex(string(raw), ") ")+2:])[19], strings.TrimSpace(string(boot))}
}

type runtimeProofProcess struct {
	cmd        *exec.Cmd
	identity   runtimeProofOwner
	done       chan error
	root, name string
	stopped    bool
}

func (p *runtimeProofProcess) stop() {
	if p.stopped {
		return
	}
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", p.identity.PID)); err == nil {
		check(runtimeProofIdentity(p.identity.PID) == p.identity, "owned process identity changed; no signal")
		save(filepath.Join(p.root, p.name+"-stop-intent.json"), p.identity)
		must(p.cmd.Process.Signal(syscall.SIGTERM))
	} else {
		check(os.IsNotExist(err), "owned process liveness unavailable")
	}
	select {
	case <-p.done:
		p.stopped = true
	case <-time.After(10 * time.Second):
		panic("owned process did not settle; retain ownership, no broad kill/retry")
	}
	_, err := os.Stat(fmt.Sprintf("/proc/%d", p.identity.PID))
	check(os.IsNotExist(err), "owned process remains after wait")
	save(filepath.Join(p.root, p.name+"-exit.json"), map[string]any{"identity": p.identity, "reaped": true})
}

// One actual authenticated machine-runtime boundary, sequential schema6/7 and
// unsupported-version cases. Private real HC, queues and endpoints; no agents,
// dispatcher, GUI, forwarding, shared registration or mocked transport.
func runtimeSchemaProof(bridge, cli, root string) {
	syscall.Umask(0077)
	var err error
	bridge, err = filepath.Abs(bridge)
	must(err)
	cli, err = filepath.Abs(cli)
	must(err)
	root, err = filepath.Abs(root)
	must(err)
	must(os.Mkdir(root, 0700))
	for _, dir := range []string{"home", "config", "data", "state", "cache", "run", "tmp"} {
		must(os.Mkdir(filepath.Join(root, dir), 0700))
	}
	env := append([]string{}, os.Environ()...)
	set := func(key, value string) {
		next := []string{}
		for _, v := range env {
			if !strings.HasPrefix(v, key+"=") {
				next = append(next, v)
			}
		}
		env = append(next, key+"="+value)
	}
	for k, v := range map[string]string{"HOME": "home", "XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "data", "XDG_STATE_HOME": "state", "XDG_CACHE_HOME": "cache", "XDG_RUNTIME_DIR": "run", "TMPDIR": "tmp"} {
		set(k, filepath.Join(root, v))
	}
	set("HC_SOCKET", filepath.Join(root, "run", "h.sock"))
	set("HC_SESSION", "r")
	run := func(program string, args ...string) []byte {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, program, args...)
		cmd.Env = env
		cmd.Dir = root
		b, e := cmd.Output()
		if e != nil {
			panic(fmt.Sprintf("%s failed (private output not logged): %v", filepath.Base(program), e))
		}
		return b
	}
	processes := []*runtimeProofProcess{}
	success := false
	defer func() {
		cleanupErrors := []string{}
		for i := len(processes) - 1; i >= 0; i-- {
			func() {
				defer func() {
					if e := recover(); e != nil {
						cleanupErrors = append(cleanupErrors, fmt.Sprint(e))
					}
				}()
				processes[i].stop()
			}()
		}
		unsettled := []runtimeProofOwner{}
		for _, p := range processes {
			if !p.stopped {
				unsettled = append(unsettled, p.identity)
			}
		}
		save(filepath.Join(root, "OWNERSHIP.json"), map[string]any{"success": success && len(cleanupErrors) == 0, "owned_running_resources": unsettled, "cleanup_errors": cleanupErrors, "owned_process_count": len(processes), "all_exact_identities_reaped": len(unsettled) == 0})
		check(len(cleanupErrors) == 0, "cleanup incomplete; inspect exact ownership receipts")
	}()
	start := func(name, program string, args ...string) (*runtimeProofProcess, *bufio.Reader) {
		save(filepath.Join(root, name+"-intent.json"), map[string]any{"program": program, "args": args, "owner": runtimeProofIdentity(os.Getpid())})
		cmd := exec.Command(program, args...)
		cmd.Env = env
		cmd.Dir = root
		out, e := cmd.StdoutPipe()
		must(e)
		log, e := os.OpenFile(filepath.Join(root, name+".stderr"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		must(e)
		cmd.Stderr = log
		must(cmd.Start())
		p := &runtimeProofProcess{cmd: cmd, identity: runtimeProofIdentity(cmd.Process.Pid), done: make(chan error, 1), root: root, name: name}
		processes = append(processes, p)
		save(filepath.Join(root, name+"-birth.json"), p.identity)
		go func() { p.done <- cmd.Wait(); log.Close() }()
		return p, bufio.NewReader(out)
	}
	hcPath, err := exec.LookPath("herdr-core")
	must(err)
	// HC normally creates a 0775 socket under its private directory. Do not
	// mask that real integration boundary with the proof's private-file umask.
	syscall.Umask(0002)
	hc, _ := start("hc", hcPath, "serve")
	syscall.Umask(0077)
	socket := filepath.Join(root, "run", "h.sock")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err = os.Stat(socket); err == nil {
			break
		}
		check(time.Now().Before(deadline), "private HC socket deadline")
		time.Sleep(25 * time.Millisecond)
	}
	conn, err := net.DialTimeout("unix", socket, 5*time.Second)
	must(err)
	must(conn.SetDeadline(time.Now().Add(5 * time.Second)))
	must(json.NewEncoder(conn).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "hello", "params": map[string]any{"generation": 1, "framed": false}}))
	var hello struct {
		Result struct {
			Core struct {
				ID string `json:"id"`
			} `json:"core"`
			Version string `json:"version"`
		} `json:"result"`
	}
	must(json.NewDecoder(conn).Decode(&hello))
	must(conn.Close())
	check(hello.Result.Core.ID != "" && hello.Result.Version == "0.1.21", "actual private HC hello")
	info, err := os.Stat(socket)
	must(err)
	check(info.Mode().Perm() == 0775, "real HC must expose normal0775 socket inside owned0700 directory")
	socketStat := info.Sys().(*syscall.Stat_t)
	host, err := os.Hostname()
	must(err)
	project := "proj-runtime-schema"
	results := []any{}
	for _, version := range []int{6, 7} {
		dir := filepath.Join(root, "schema"+strconv.Itoa(version))
		must(os.Mkdir(dir, 0700))
		state := filepath.Join(dir, "queue")
		set("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
		must(os.Mkdir(filepath.Join(dir, "config"), 0700))
		set("OCQ_STATE_DIR", state)
		run(cli, "--state-dir", state, "list")
		if version == 7 {
			run(cli, "--state-dir", state, "activate-scheduling")
		}
		var job struct {
			ID string `json:"id"`
		}
		must(json.Unmarshal(run(cli, "--state-dir", state, "submit", "--key", "projection-owner", "--cwd", root, "--memory-bytes", "0", "--", "/bin/true"), &job))
		run(cli, "--state-dir", state, "cancel", job.ID)
		// Historical projection fixture, not fabricated agent execution/capture.
		db, e := sql.Open("sqlite", "file:"+filepath.Join(state, "queue.db")+"?_pragma=foreign_keys(1)")
		must(e)
		tx, e := db.Begin()
		must(e)
		_, e = tx.Exec("INSERT INTO projects VALUES(?, 'Runtime projection fixture','fixture')", project)
		must(e)
		digest := strings.Repeat("a", 64)
		_, e = tx.Exec("INSERT INTO project_manifests VALUES(?,?,1,'brief',?,'fixture')", digest, project, []byte(`{}`))
		must(e)
		_, e = tx.Exec("INSERT INTO project_bindings VALUES(?,1,?,?,?,?,?,?,?,'fixture','fixture','fixture')", project, root, root, strings.Repeat("a", 40), "ses_fixture", "fixture", root, digest)
		must(e)
		_, e = tx.Exec("INSERT INTO project_jobs VALUES(?,?,1,'implementation',?,NULL,NULL,'fixture')", job.ID, project, digest)
		must(e)
		_, e = tx.Exec("INSERT INTO project_lanes(lane_id,project_id,owner_job_id,role,base_commit,fsfork_driver,created_at) VALUES('lane-runtime',?,?,'implementation',?,'git_worktree','fixture')", project, job.ID, strings.Repeat("a", 40))
		must(e)
		must(tx.Commit())
		must(db.Close())
		space := "sp" + strings.Repeat(strconv.Itoa(version), 32)
		var descriptor map[string]any
		must(json.Unmarshal(run(bridge, "compatibility", space, root), &descriptor))
		check(descriptor["observation_schema"] == float64(version), "public compatibility schema")
		binding := map[string]any{"protocol": "ocq-private-hc-v1", "socket": socket, "device": socketStat.Dev, "inode": socketStat.Ino, "uid": os.Getuid(), "pid": hc.identity.PID, "start_ticks": hc.identity.Start, "boot_id": hc.identity.Boot, "host": host, "core_id": hello.Result.Core.ID, "space": space, "folder": root, "queue_identity": descriptor["queue_identity"]}
		bindingPath := filepath.Join(dir, "hc-binding.json")
		save(bindingPath, binding)
		if version == 7 {
			// Exercise the public preflight against the real listener, including
			// boundaries that must still refuse before endpoint registration.
			refuse := func(file, diagnostic string) {
				cmd := exec.Command(bridge, "hc-binding-check", file, space, root, fmt.Sprint(descriptor["queue_identity"]))
				cmd.Env, cmd.Dir = env, root
				out, e := cmd.CombinedOutput()
				check(e != nil && strings.Contains(string(out), diagnostic), "binding preflight must refuse: "+diagnostic)
			}
			must(os.Chmod(filepath.Dir(socket), 0750))
			refuse(bindingPath, "private owned HC socket directory required")
			must(os.Chmod(filepath.Dir(socket), 0700))
			binding["inode"] = socketStat.Ino + 1
			badSocket := filepath.Join(dir, "wrong-socket.json")
			save(badSocket, binding)
			refuse(badSocket, "HC socket ownership/incarnation changed")
			binding["inode"] = socketStat.Ino
			self := runtimeProofIdentity(os.Getpid())
			binding["pid"], binding["start_ticks"] = self.PID, self.Start
			badPeer := filepath.Join(dir, "wrong-peer.json")
			save(badPeer, binding)
			refuse(badPeer, "HC listener owner changed")
			binding["pid"], binding["start_ticks"] = hc.identity.PID, hc.identity.Start
			var checked map[string]any
			must(json.Unmarshal(run(bridge, "hc-binding-check", bindingPath, space, root, fmt.Sprint(descriptor["queue_identity"])), &checked))
			check(checked["validation"] == "existing_socket_peer_and_core_verified" && checked["launch"] == false, "real HC read-only binding preflight")
			save(filepath.Join(dir, "binding-boundary.json"), map[string]any{"socket_mode": "0775", "parent_mode": "0700", "real_hc_preflight": checked, "nonprivate_parent_refused": true, "wrong_socket_identity_refused": true, "wrong_peer_refused": true})
		}
		tokenPath := filepath.Join(dir, "token")
		run(bridge, "push-token", tokenPath)
		endpoint, output := start(fmt.Sprintf("endpoint%d", version), bridge, "--mod-space", space, "--mod-folder", root, "push-serve", "--runtime", "--queue-dir", state, "--listen", "127.0.0.1:0", "--token-file", tokenPath, "--hc-binding-file", bindingPath)
		line := make(chan []byte, 1)
		go func() { b, _ := output.ReadBytes('\n'); line <- b }()
		var ready map[string]any
		select {
		case b := <-line:
			must(json.Unmarshal(b, &ready))
		case <-time.After(10 * time.Second):
			panic("actual endpoint readiness deadline")
		}
		check(ready["read_only"] == true && ready["queue_identity"] == descriptor["queue_identity"], "read-only endpoint identity")
		// Runtime discovery returns auth material: consume only in process memory.
		var discovery struct {
			State   string `json:"state"`
			Runtime struct {
				Token    string `json:"token"`
				Endpoint string `json:"endpoint"`
				Core     string `json:"core_id"`
				Queue    string `json:"queue_identity"`
				PID      int    `json:"pid"`
			} `json:"runtime"`
		}
		raw := run(bridge, "runtime-discover", "--json")
		must(json.Unmarshal(raw, &discovery))
		clear(raw)
		token, e := os.ReadFile(tokenPath)
		must(e)
		check(discovery.Runtime.Token == strings.TrimSpace(string(token)) && discovery.Runtime.Core == hello.Result.Core.ID && discovery.Runtime.Queue == descriptor["queue_identity"] && discovery.Runtime.PID == endpoint.identity.PID, "actual runtime registration/discovery linkage")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		open := func(token, queue string) *websocket.Conn {
			c, _, e := websocket.Dial(ctx, discovery.Runtime.Endpoint, nil)
			must(e)
			b, e := json.Marshal(map[string]any{"type": "auth", "protocol": "ocq-push-v1", "token": token, "core_id": hello.Result.Core.ID, "queue_identity": queue, "peer": "schema-proof"})
			must(e)
			must(c.Write(ctx, websocket.MessageText, b))
			return c
		}
		bad := open(strings.Repeat("0", 64), discovery.Runtime.Queue)
		_, _, e = bad.Read(ctx)
		check(e != nil, "unauthenticated runtime accepted")
		bad.CloseNow()
		wrongQueue := open(discovery.Runtime.Token, strings.Repeat("0", 64))
		_, _, e = wrongQueue.Read(ctx)
		check(e != nil, "wrong queue identity accepted")
		wrongQueue.CloseNow()
		c := open(discovery.Runtime.Token, discovery.Runtime.Queue)
		c.SetReadLimit(core.ObservationResponseBytes + 16384)
		read := func() map[string]any {
			_, b, e := c.Read(ctx)
			must(e)
			var v map[string]any
			must(json.Unmarshal(b, &v))
			return v
		}
		welcome := read()
		check(welcome["type"] == "welcome" && welcome["mutations"] == false, "actual authenticated welcome")
		d := welcome["descriptor"].(map[string]any)
		check(d["observation_schema"] == float64(version) && d["queue_identity"] == descriptor["queue_identity"] && d["observation_store_context"] == descriptor["observation_store_context"] && d["image_sha256"] == descriptor["image_sha256"], "welcome source pins")
		req := map[string]any{"type": "subscribe", "id": "1", "request": map[string]any{"protocol": core.ObservationProtocol, "queue_identity": d["queue_identity"], "store_context": d["observation_store_context"], "bridge_context": d["observation_context"], "selectors": []any{map[string]any{"slot": "projects", "kind": "projects", "cursor": "0"}, map[string]any{"slot": "lanes", "kind": "lanes", "project_id": project, "cursor": "0"}}}}
		b, e := json.Marshal(req)
		must(e)
		must(c.Write(ctx, websocket.MessageText, b))
		frame := read()
		check(frame["type"] == "snapshot" && frame["id"] == "1", "actual subscribed snapshot")
		snapshot := frame["snapshot"].(map[string]any)
		check(snapshot["schema"] == float64(version) && snapshot["status"] == "ready" && snapshot["queue_identity"] == d["queue_identity"] && snapshot["store_context"] == d["observation_store_context"] && snapshot["bridge_context"] == d["observation_context"] && snapshot["consistency"] == "single_sqlite_snapshot", "schema/source/coherence pins")
		slots := snapshot["slots"].([]any)
		check(len(slots) == 2, "both requested slots")
		for _, value := range slots {
			slot := value.(map[string]any)
			check(slot["status"] == "ready" && len(slot["data"].([]any)) == 1 && strings.HasPrefix(slot["cursor"].(string), "k1."), "actual populated projects/lanes keyset projection")
			item := slot["data"].([]any)[0].(map[string]any)
			check(item["project_id"] == project, "projection project identity")
			if slot["kind"] == "lanes" {
				check(item["owner_job_id"] == job.ID, "lane exact owner")
			}
		}
		save(filepath.Join(dir, "snapshot.json"), frame) // No auth frame/discovery token.
		c.CloseNow()
		cancel()
		clear(token)
		discovery.Runtime.Token = ""
		endpoint.stop()
		_, e = os.Stat(filepath.Join(dir, "config", "ocq", "runtime.json"))
		check(os.IsNotExist(e), "owned registration did not retire")
		results = append(results, map[string]any{"schema": version, "welcome": true, "projects": 1, "lanes": 1, "authentication_refused_without_token": true, "wrong_queue_refused": true, "registration_retired": true, "read_only": true})
	}
	// Explicit unsupported future-version fixture. Never call an initializer after
	// setting it, and never claim this is an authentic future migration.
	future := filepath.Join(root, "future")
	must(os.Mkdir(future, 0700))
	set("XDG_CONFIG_HOME", filepath.Join(future, "config"))
	must(os.Mkdir(filepath.Join(future, "config"), 0700))
	state := filepath.Join(future, "queue")
	set("OCQ_STATE_DIR", state)
	run(cli, "--state-dir", state, "list")
	db, err := sql.Open("sqlite", "file:"+filepath.Join(state, "queue.db"))
	must(err)
	_, err = db.Exec("PRAGMA user_version=9")
	must(err)
	must(db.Close())
	tokenPath := filepath.Join(future, "token")
	run(bridge, "push-token", tokenPath)
	futureContext, cancelFuture := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelFuture()
	cmd := exec.CommandContext(futureContext, bridge, "--mod-space", "spfuture", "--mod-folder", root, "push-serve", "--runtime", "--queue-dir", state, "--listen", "127.0.0.1:0", "--token-file", tokenPath)
	cmd.Env = env
	var refusal strings.Builder
	cmd.Stderr = &refusal
	out, err := cmd.Output()
	check(err != nil && len(out) == 0, "unsupported schema reached listen/readiness")
	check(strings.Contains(refusal.String(), "push requires an existing private schema6/7/8 store"), "unsupported version did not fail at the startup schema boundary")
	head, err := core.ObserveHead(context.Background(), state)
	must(err)
	check(head.Status != "ready" && head.Schema == 9, "unsupported observation or automatic migration")
	_, err = os.Stat(filepath.Join(future, "config", "ocq", "runtime.json"))
	check(os.IsNotExist(err), "unsupported store registered runtime")
	hc.stop()
	success = true
	save(filepath.Join(root, "RESULT.json"), map[string]any{"pass": true, "cases": results, "unsupported9_rejected_before_listen": true, "schema7_explicitly_activated": true, "qualification": "Real compiled CLI/bridge, private real HC and authenticated runtime sockets. Populated projects/lanes are historical projection fixtures, not live agent or workflow execution."})
	fmt.Println("PASS real runtime schema6/schema7 welcome + projects/lanes snapshot; unsupported9 refusal; private registration retirement")
}
