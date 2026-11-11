package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Transparent stdio observation: never record protocol buffers or substitute a
// request/response. The real HC CLI generates the read-only status exchange.
func transportRelay(root, hc, guard string) {
	listener, e := net.Listen("unix", filepath.Join(root, "run", "stdio.sock"))
	must(e)
	defer listener.Close()
	c := exec.Command(guard, hc, "connect", "--session", "p")
	input, e := c.StdinPipe()
	must(e)
	output, e := c.StdoutPipe()
	must(e)
	stderr, e := os.OpenFile(filepath.Join(root, "connect.stderr"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	must(e)
	defer stderr.Close()
	c.Stderr = stderr
	must(c.Start())
	save(filepath.Join(root, "relay-ready.json"), map[string]any{"pid": os.Getpid(), "connect_guard_pid": c.Process.Pid, "stage": "listener_bound_connect_spawned", "session": "p", "guard_layers": 2})
	// No Wait/kill fallback: EOF closes normal stdio; the launch-time owner
	// alone accounts for any remaining child/guard and signals by verified pidfd.
	listener.(*net.UnixListener).SetDeadline(time.Now().Add(6 * time.Second))
	conn, e := listener.Accept()
	if e != nil {
		save(filepath.Join(root, "relay-result.json"), map[string]any{"stage": "no_status_client", "error": e.Error()})
		input.Close()
		output.Close()
		return
	}
	defer conn.Close()
	var sent, received atomic.Int64
	copyCount := func(dst io.Writer, src io.Reader, n *atomic.Int64) {
		b := make([]byte, 4096)
		for {
			r, err := src.Read(b)
			if r > 0 {
				w, we := dst.Write(b[:r])
				n.Add(int64(w))
				if we != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	go func() { copyCount(input, conn, &sent); input.Close() }()
	go copyCount(conn, output, &received)
	time.Sleep(2 * time.Second)
	save(filepath.Join(root, "relay-result.json"), map[string]any{"stage": "after_read_only_status_exchange", "client_to_connect_bytes": sent.Load(), "connect_to_client_bytes": received.Load(), "guard_layers": 2, "buffers_logged": false})
	input.Close()
	output.Close()
}

func transportEnvironment(root, socket string) []string {
	env := []string{"HOME=" + filepath.Join(root, "home"), "XDG_CONFIG_HOME=" + filepath.Join(root, "peer-config"), "XDG_DATA_HOME=" + filepath.Join(root, "peer-data"), "XDG_STATE_HOME=" + filepath.Join(root, "peer-state"), "XDG_RUNTIME_DIR=" + filepath.Join(root, "run"), "TMPDIR=" + filepath.Join(root, "tmp"), "PROBE_ROOT=" + root, "HC_SESSION=p", "PATH=/usr/bin:/bin", "SHELL=/bin/sh", "GOMAXPROCS=2", "HERDR_TELEMETRY=0", "DO_NOT_TRACK=1"}
	if socket != "" {
		env = append(env, "HC_SOCKET="+socket)
	}
	return env
}

func transportObserve(root, hc, guard, owner string) {
	self, e := os.Executable()
	must(e)
	record := func(c *exec.Cmd, label string) bool {
		r := exec.Command(owner, "cleanup-record", strconv.Itoa(c.Process.Pid), filepath.Join(root, label+"-owner.json"))
		r.Env = append(os.Environ(), "PROBE_ROOT="+root)
		b, e := r.CombinedOutput()
		if e != nil {
			save(filepath.Join(root, "identity-error.json"), map[string]any{"label": label, "error": e.Error(), "diagnostic": string(b)})
			return false
		}
		return true
	}
	peer := exec.Command(guard, hc, "serve")
	peer.Env = transportEnvironment(root, "")
	log, e := os.OpenFile(filepath.Join(root, "peer.stderr"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	must(e)
	defer log.Close()
	peer.Stdout = io.Discard
	peer.Stderr = log
	must(peer.Start())
	if !record(peer, "peer") {
		return
	}
	socket := filepath.Join(root, "run", "herdr-core", "p.sock")
	end := time.Now().Add(3 * time.Second)
	for !exists(socket) && time.Now().Before(end) {
		time.Sleep(20 * time.Millisecond)
	}
	if !exists(socket) {
		save(filepath.Join(root, "transport-result.json"), map[string]any{"stage": "peer_socket_absent", "protocol_pass": false})
		return
	}
	status := func(socket, label string) (bool, map[string]any) {
		c := exec.Command(guard, hc, "server", "status")
		c.Env = transportEnvironment(root, socket)
		var out bytes.Buffer
		c.Stdout = &out
		errFile, e := os.OpenFile(filepath.Join(root, label+".stderr"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
		must(e)
		defer errFile.Close()
		c.Stderr = errFile
		must(c.Start())
		// A status command can finish before a separate recorder exec starts.
		// Its exact spawned-child Wait is the terminal identity evidence; this
		// path never signals it. Live descendants remain owned by the subreaper.
		save(filepath.Join(root, label+"-launch.json"), map[string]any{"pid": c.Process.Pid, "parent": os.Getpid(), "at": time.Now().UTC().Format(time.RFC3339Nano), "command": "hc server status", "socket": socket, "signal_authorized": false})
		done := make(chan error, 1)
		go func() { done <- c.Wait() }()
		select {
		case e := <-done:
			var v struct {
				Core struct {
					ID string `json:"id"`
				}
				Version string `json:"version"`
				Live    int    `json:"live"`
			}
			parseErr := json.Unmarshal(out.Bytes(), &v)
			result := map[string]any{"pid": c.Process.Pid, "waited_pid": c.ProcessState.Pid(), "exit": fmt.Sprint(e), "stdout_bytes": out.Len(), "valid_status": parseErr == nil, "core_id": v.Core.ID, "version": v.Version, "live": v.Live}
			save(filepath.Join(root, label+".json"), result)
			return e == nil && parseErr == nil, result
		case <-time.After(4 * time.Second):
			result := map[string]any{"stage": "bounded_status_timeout", "pid": c.Process.Pid, "stdout_logged": false}
			save(filepath.Join(root, label+".json"), result)
			return false, result
		}
	}
	ok, direct := status(socket, "direct-status")
	if !ok {
		transportSnapshot(root, []int{peer.Process.Pid})
		save(filepath.Join(root, "transport-result.json"), map[string]any{"stage": "direct_peer_status_failed", "protocol_pass": false, "direct": direct})
		return
	}
	relay := exec.Command(guard, self, "transport-relay", hc, guard)
	relay.Env = transportEnvironment(root, "")
	relay.Stdout = io.Discard
	relay.Stderr = log
	must(relay.Start())
	if !record(relay, "relay") {
		return
	}
	end = time.Now().Add(3 * time.Second)
	for !exists(filepath.Join(root, "relay-ready.json")) && time.Now().Before(end) {
		time.Sleep(20 * time.Millisecond)
	}
	if !exists(filepath.Join(root, "relay-ready.json")) {
		transportSnapshot(root, []int{peer.Process.Pid, relay.Process.Pid})
		save(filepath.Join(root, "transport-result.json"), map[string]any{"stage": "relay_not_ready", "protocol_pass": false})
		return
	}
	// Snapshot while the nested connection is live, before its bounded EOF.
	snapshotDone := make(chan struct{})
	go func() {
		defer close(snapshotDone)
		time.Sleep(700 * time.Millisecond)
		transportSnapshot(root, []int{peer.Process.Pid, relay.Process.Pid})
	}()
	ok, via := status(filepath.Join(root, "run", "stdio.sock"), "stdio-status")
	<-snapshotDone
	end = time.Now().Add(3 * time.Second)
	for !exists(filepath.Join(root, "relay-result.json")) && time.Now().Before(end) {
		time.Sleep(20 * time.Millisecond)
	}
	save(filepath.Join(root, "transport-result.json"), map[string]any{"stage": "complete_observation_not_machine_acceptance", "protocol_pass": ok, "direct": direct, "stdio": via, "queue": "not_created", "machine_registration": "not_attempted", "payload_buffers_logged": false})
}

// One snapshot of exact launched roots/descendants, not host discovery. A denied
// metadata operation is recorded once and never retried through another reader.
func transportSnapshot(root string, roots []int) {
	seen := map[int]bool{}
	records := []map[string]any{}
	var visit func(int)
	visit = func(pid int) {
		if seen[pid] || len(seen) >= 64 {
			return
		}
		seen[pid] = true
		base := fmt.Sprintf("/proc/%d", pid)
		r := map[string]any{"pid": pid}
		for _, name := range []string{"stat", "wchan", "syscall"} {
			b, e := os.ReadFile(filepath.Join(base, name))
			if e != nil {
				r[name+"_error"] = e.Error()
			} else {
				r[name] = strings.TrimSpace(string(b))
			}
		}
		fds := map[string]string{}
		files, e := os.ReadDir(filepath.Join(base, "fd"))
		if e != nil {
			r["fd_error"] = e.Error()
		} else {
			for _, f := range files {
				v, e := os.Readlink(filepath.Join(base, "fd", f.Name()))
				if e != nil {
					fds[f.Name()] = e.Error()
				} else {
					fds[f.Name()] = v
				}
			}
		}
		r["fds"] = fds
		paths, e := filepath.Glob(filepath.Join(base, "task", "*", "children"))
		if e == nil {
			for _, p := range paths {
				b, e := os.ReadFile(p)
				if e != nil {
					r["children_error"] = e.Error()
					continue
				}
				for _, field := range strings.Fields(string(b)) {
					id, e := strconv.Atoi(field)
					if e == nil {
						visit(id)
					}
				}
			}
		}
		records = append(records, r)
	}
	for _, pid := range roots {
		visit(pid)
	}
	save(filepath.Join(root, "process-stages.json"), records)
}
