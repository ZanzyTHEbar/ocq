// One real interrupted foreground-command / authenticated bridge scenario.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"ocq/internal/core"
)

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func check(ok bool, s string) {
	if !ok {
		panic(s)
	}
}
func hash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func random() string {
	b := make([]byte, 32)
	_, e := rand.Read(b)
	must(e)
	return hex.EncodeToString(b)
}
func proof(cap, nonce, role string) string {
	key, e := hex.DecodeString(cap)
	must(e)
	h := hmac.New(sha256.New, key)
	h.Write([]byte("ocq-compose-v1:" + role + ":" + nonce))
	return hex.EncodeToString(h.Sum(nil))
}
func wait(why string, f func() bool) {
	deadline := time.Now().Add(10 * time.Second)
	for !f() {
		check(time.Now().Before(deadline), why)
		time.Sleep(20 * time.Millisecond)
	}
}

func main() {
	if len(os.Args) == 3 && os.Args[1] == "--probe" {
		for n := 0; n < 1200; n++ {
			if _, e := os.Stat(os.Args[2]); e == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		panic("probe not released")
	}
	if len(os.Args) == 3 && os.Args[1] == "--dispatcher" {
		q, e := core.OpenQueue(os.Args[2])
		must(e)
		defer q.Close()
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer cancel()
		must(q.Dispatch(ctx, 1, 0, true, nil))
		return
	}
	check(len(os.Args) == 3 || len(os.Args) == 4 && os.Args[3] == "--fixture", "usage: herdr-command-resolution BRIDGE FRESH_PRIVATE_ROOT [--fixture]")
	bridge, e := filepath.Abs(os.Args[1])
	must(e)
	root, e := filepath.Abs(os.Args[2])
	must(e)
	must(os.Mkdir(root, 0700))
	state := filepath.Join(root, "queue")
	q, e := core.OpenQueue(state)
	must(e)
	defer q.Close()
	self, e := os.Executable()
	must(e)
	release := filepath.Join(root, "release")
	defer os.WriteFile(release, nil, 0600)
	job, e := q.SubmitCommand("interrupted", root, []string{self, "--probe", release}, 0)
	must(e)
	spare, e := q.SubmitCommand("untouched", root, []string{self, "--probe", filepath.Join(root, "never-run")}, 1<<62)
	must(e)
	start := func() *exec.Cmd {
		c := exec.Command(self, "--dispatcher", state)
		log, e := os.OpenFile(filepath.Join(root, "dispatch.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		must(e)
		c.Stdout, c.Stderr = log, log
		must(c.Start())
		must(log.Close())
		return c
	}
	first := start()
	defer func() {
		if first.ProcessState == nil {
			_ = first.Process.Kill()
			_ = first.Wait()
		}
	}()
	wait("command not admitted", func() bool { job, e = q.Job(job.ID); must(e); return job.State == "running" })
	owned := job.Process
	check(owned.PID > 0 && owned.Start > 0 && owned.Boot != "", "real process identity missing")
	must(first.Process.Kill())
	check(first.Wait() != nil, "dispatcher crash did not occur")
	second := start()
	defer func() {
		if second.ProcessState == nil {
			_ = second.Process.Signal(syscall.SIGTERM)
			_ = second.Wait()
		}
	}()
	wait("restart did not retain unknown ownership", func() bool { job, e = q.Job(job.ID); must(e); return job.State == "unknown" })
	check(job.Process == owned, "recovery changed process identity")
	r, e := core.OpenReader(state)
	must(e)
	detail, e := r.QueueJob(job.ID)
	must(e)
	must(r.Close())
	t := core.CommandResolutionTarget{Machine: "local", Space: "sp-resolve-fixture", Queue: detail.QueueIdentity, Job: job.ID, Sequence: detail.Job.Sequence}
	s := core.CommandResolutionScope{CommandResolutionTarget: t, Kind: "command", State: "unknown", ReasonDigest: hash([]byte(job.Reason))}
	// The legal maximum evidence string expands sixfold in JSON, then again as
	// base64 in intent.json. Exercise the real transport and retained review,
	// rather than testing a parser or constructing a fake process observation.
	evidence := strings.Repeat("\x01", 16<<10)
	writer, e := core.OpenExistingCommandResolver(state)
	must(e)
	e = writer.Resolve(s, evidence)
	check(e != nil && strings.Contains(e.Error(), "dispatcher lock held"), "running dispatcher was not refused")
	must(second.Process.Signal(syscall.SIGTERM))
	must(second.Wait())
	e = writer.Resolve(s, evidence)
	check(e != nil && strings.Contains(e.Error(), "still exists"), "live foreground process was not refused")
	must(writer.Close())
	must(os.WriteFile(release, nil, 0600))
	wait("cooperative owned process/group did not exit", func() bool {
		_, e := os.Stat(fmt.Sprintf("/proc/%d", owned.PID))
		return errors.Is(e, os.ErrNotExist) && errors.Is(syscall.Kill(-owned.PID, 0), syscall.ESRCH)
	})
	job, e = q.Job(job.ID)
	must(e)
	check(job.State == "unknown" && job.Validation == "not_run", "process exit incorrectly completed job")
	db, e := sql.Open("sqlite", filepath.Join(state, "queue.db"))
	must(e)
	defer db.Close()
	_, e = db.Exec(`CREATE TABLE proof_resolutions(job_id TEXT);
 CREATE TRIGGER proof_resolution AFTER UPDATE OF evidence ON jobs WHEN NEW.state='resolved'
 BEGIN INSERT INTO proof_resolutions VALUES(NEW.id); END;`)
	must(e)
	if len(os.Args) == 4 {
		must(json.NewEncoder(os.Stdout).Encode(map[string]any{"state_dir": state, "target": t, "scope": s, "spare": spare.ID}))
		return
	}
	args := func(action string, t core.CommandResolutionTarget, extra ...string) []string {
		return append([]string{"command-resolve-" + action, t.Space, t.Queue, t.Job, t.Sequence}, extra...)
	}
	call := func(a ...string) ([]byte, error) {
		c := exec.Command(bridge, a...)
		c.Env = append(os.Environ(), "OCQ_STATE_DIR="+state)
		return c.Output()
	}
	read := func(t core.CommandResolutionTarget) core.CommandResolutionStatus {
		b, e := call(args("status", t)...)
		must(e)
		check(!bytes.Contains(b, []byte(`\u0001`)) && !bytes.Contains(b, []byte(`"evidence"`)) && !bytes.Contains(b, []byte(`"payload_base64"`)), "private evidence leaked in status")
		var o core.CommandResolutionStatus
		must(json.Unmarshal(b, &o))
		return o
	}
	beforeSpare := func() string {
		var b string
		must(db.QueryRow("SELECT json_array(j.id,j.state,j.reason,j.pid,j.start_ticks,j.boot,j.stdout,j.stderr,j.exit_code,j.outcome,j.validation,j.evidence,hex(r.canonical),r.digest) FROM jobs j JOIN receipts r ON r.job_id=j.id WHERE j.id=?", spare.ID).Scan(&b))
		return b
	}
	originalSpare := beforeSpare()
	exchange := func(mode string, t core.CommandResolutionTarget, digest string, raw []byte) (core.CommandResolutionStatus, []byte, error) {
		launch := random()
		a := args(mode, t, launch)
		if mode == "preview" {
			a = args(mode, t, digest, launch)
		}
		cmd := exec.Command(bridge, a...)
		cmd.Env = append(os.Environ(), "OCQ_STATE_DIR="+state)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		must(cmd.Start())
		defer func() {
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		}()
		var entry core.ComposeRendezvous
		wait("resolution rendezvous unavailable", func() bool { entry, e = core.CommandResolutionRendezvous(state, t, launch); return e == nil })
		conn, e := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", entry.Port), time.Second)
		must(e)
		defer conn.Close()
		must(conn.SetDeadline(time.Now().Add(5 * time.Second)))
		input := bufio.NewReader(conn)
		nonce := random()
		_, e = fmt.Fprintln(conn, nonce)
		must(e)
		line, e := input.ReadString('\n')
		must(e)
		check(line == proof(entry.Capability, nonce, "server")+"\n", "server authentication failed")
		_, e = fmt.Fprintln(conn, proof(entry.Capability, nonce, "client"))
		must(e)
		line, e = input.ReadString('\n')
		must(e)
		check(line == "READY\n", "client authentication failed")
		var preview []byte
		if mode == "receive" {
			_, e = fmt.Fprintf(conn, "%d\n", len(raw))
			must(e)
			_, e = conn.Write(raw)
			must(e)
			_, _ = input.ReadString('\n')
		} else {
			var h [4]byte
			_, e = io.ReadFull(input, h[:])
			must(e)
			n := binary.BigEndian.Uint32(h[:])
			check(n > 0 && n <= 131072, "invalid preview size")
			preview = make([]byte, n)
			_, e = io.ReadFull(input, preview)
			must(e)
		}
		must(conn.Close())
		err := cmd.Wait()
		for _, output := range [][]byte{stdout.Bytes(), stderr.Bytes()} {
			check(!bytes.Contains(output, []byte(`\u0001`)) && !bytes.Contains(output, []byte(`"evidence"`)) && !bytes.Contains(output, []byte(`"payload_base64"`)), "private evidence leaked from transport")
		}
		var out core.CommandResolutionStatus
		if err == nil {
			must(json.Unmarshal(stdout.Bytes(), &out))
		}
		return out, preview, err
	}
	check(read(t).State == "none", "view prepared resolution")
	payload := core.CommandResolutionPayload{Scope: s, Reason: job.Reason, Evidence: ""}
	raw, e := json.Marshal(payload)
	must(e)
	_, _, e = exchange("receive", t, "", raw)
	check(e != nil && read(t).State == "none", "missing evidence accepted")
	payload.Evidence = evidence
	raw, e = json.Marshal(payload)
	must(e)
	digest := hash(raw)
	check(len(raw) <= 128<<10 && base64.StdEncoding.EncodedLen(len(raw)) > 128<<10, "escaped maximum evidence did not exercise encoded record expansion")
	prepared, _, e := exchange("receive", t, "", raw)
	must(e)
	check(prepared.State == "prepared" && prepared.PayloadDigest == digest && prepared.EvidenceDigest == hash([]byte(evidence)), "exact evidence intent missing")
	check(read(t).State == "prepared", "fresh bridge process could not recover maximum escaped evidence")
	recordPath := filepath.Join(state, "herdr-command-resolve-"+prepared.IntentID, "intent.json")
	recordBytes, e := os.ReadFile(recordPath)
	must(e)
	recordLimit := base64.StdEncoding.EncodedLen(128<<10) + 4096
	check(len(recordBytes) > 128<<10 && len(recordBytes) <= recordLimit, "encoded evidence record outside its distinct bound")
	wrong := t
	wrong.Space = "sp-other"
	check(read(wrong).State == "blocked", "cross-copy wrong space adopted private evidence")
	_, e = call(args("arm", wrong, digest)...)
	check(e != nil, "wrong context armed resolution")
	wrong = t
	wrong.Queue = strings.Repeat("0", 64)
	_, e = call(args("status", wrong)...)
	check(e != nil, "wrong queue observed resolution")
	reviewed, private, e := exchange("preview", t, digest, nil)
	must(e)
	check(bytes.Equal(private, raw) && reviewed.State == "prepared", "private exact-byte review changed evidence or scope")
	_, e = call(args("arm", t, digest)...)
	must(e)
	check(read(t).State == "armed", "reload lost claim or dispatched it")
	lostArgs := append([]string{"-c", `"$@" >/dev/null || exit $?; exit 73`, "lost-resolution-response", bridge}, args("attempt", t, digest)...)
	lost := exec.Command("/bin/sh", lostArgs...)
	lost.Env = append(os.Environ(), "OCQ_STATE_DIR="+state)
	e = lost.Run()
	check(e != nil && lost.ProcessState.ExitCode() == 73, "lost response fixture failed")
	observed := read(t)
	check(observed.State == "resolved_observed" && observed.Observation.Job.Validation == "not_run" && observed.Observation.Completion.State == "resolved" && observed.Observation.Completion.Validation == "not_run" && observed.Observation.EvidenceDigest == hash([]byte(evidence)), "resolution fabricated success or lost matching completion/evidence")
	_, e = call(args("attempt", t, digest)...)
	check(e != nil, "second attempt allowed")
	var count int
	must(db.QueryRow("SELECT count(*) FROM proof_resolutions").Scan(&count))
	check(count == 1, "resolution mutation repeated")
	var retained string
	must(db.QueryRow("SELECT evidence FROM jobs WHERE id=?", t.Job).Scan(&retained))
	check(retained == evidence, "evidence bytes changed")
	check(beforeSpare() == originalSpare, "resolution changed another job/receipt")
	public, e := call("queue-job", job.ID)
	must(e)
	check(!bytes.Contains(public, []byte(`\u0001`)) && !bytes.Contains(public, []byte(`"resolution_evidence"`)), "safe queue view leaked raw evidence")
	// A malformed oversized record for the untouched receipt must fail closed
	// through the real read-only bridge, without affecting either stored job.
	bad := t
	bad.Job, bad.Sequence = spare.ID, fmt.Sprint(spare.Sequence)
	badDir := filepath.Join(state, "herdr-command-resolve-"+hash([]byte(bad.Queue+"\n"+bad.Job+"\n"+bad.Sequence)))
	must(os.Mkdir(badDir, 0700))
	must(os.WriteFile(filepath.Join(badDir, "intent.json"), bytes.Repeat([]byte{'x'}, recordLimit+1), 0600))
	check(read(bad).State == "blocked" && beforeSpare() == originalSpare, "oversized malformed journal did not fail closed")
	missing := filepath.Join(root, "missing")
	_, e = core.OpenExistingCommandResolver(missing)
	check(e != nil, "writer initialized missing state")
	_, e = os.Stat(missing)
	check(os.IsNotExist(e), "missing state created")
	must(json.NewEncoder(os.Stdout).Encode(map[string]any{"result": "PASS", "state_dir": state, "evidence_bytes": len(evidence), "payload_bytes": len(raw), "journal_bytes": len(recordBytes), "payload_limit": 128 << 10, "journal_limit": recordLimit, "checks": "real interrupted dispatch->unknown; dispatcher-lock and live-process refusals; maximum escaped evidence prepared/private preview/fresh-process recovery; malformed oversized journal blocked; empty evidence/wrong context rejected; lost response recovery; one attempt; atomic terminal event, validation unchanged; safe summaries; other job unchanged"}))
}
