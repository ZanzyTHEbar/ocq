package core_test

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"ocq/internal/core"
)

// This scenario uses the actual receiver, SQLite and a separate owned process.
// The child sends once, then exits without recording completion or an ack.
func TestComposeDispatchRecovery(t *testing.T) {
	if os.Getenv("OCQ_COMPOSE_LOCKED_DISPATCH_CHILD") == "1" {
		_, err := core.DispatchLocalComposeIntent(os.Getenv("OCQ_COMPOSE_DISPATCH_STATE"), os.Getenv("OCQ_COMPOSE_DISPATCH_ID"))
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if os.Getenv("OCQ_COMPOSE_DISPATCH_CHILD") == "1" {
		state, id := os.Getenv("OCQ_COMPOSE_DISPATCH_STATE"), os.Getenv("OCQ_COMPOSE_DISPATCH_ID")
		intent, err := core.ReadLocalComposeIntent(state, id)
		if err != nil {
			os.Exit(1)
		}
		var request core.MailRequest
		if json.Unmarshal(intent.Request, &request) != nil {
			os.Exit(1)
		}
		request.Key = intent.Key
		if _, err := core.MarkLocalComposeDispatchIntent(state, id); err != nil {
			os.Exit(1)
		}
		_, _ = fmt.Fprintln(os.Stdout, "marked")
		var release [1]byte
		if _, err := io.ReadFull(os.Stdin, release[:]); err != nil {
			os.Exit(1)
		}
		sender, err := core.OpenExistingMailSender(state)
		if err != nil {
			os.Exit(1)
		}
		_, err = sender.Send(request)
		closeErr := sender.Close()
		if err != nil || closeErr != nil {
			os.Exit(1)
		}
		os.Exit(23)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(filepath.Join(home, ".local", "state"), "ocq-compose-dispatch-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	state := filepath.Join(root, "queue")
	q, err := core.OpenQueue(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(state, "queue.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const fixture = `
INSERT INTO receipts(key,job_id,canonical,digest,accepted_at) VALUES('fixture-job','job-test',x'7b7d','fixture','time');
INSERT INTO jobs(id,state,reason) VALUES('job-test','unknown','ownership uncertain');
INSERT INTO projects(id,label,created_at) VALUES('proj-test','fixture','time');
INSERT INTO project_manifests(digest,project_id,version,kind,canonical,recorded_at) VALUES('brief','proj-test',1,'brief',x'7b7d','time');
INSERT INTO project_bindings(project_id,revision,repo_root,git_common_dir,commit_anchor,elephant_session_id,opencode_project_id,opencode_directory,handoff_manifest_digest,auth_ref,verified_at,recorded_at)
 VALUES('proj-test',1,'/','/','base','ses','project','/','brief','auth','time','time');
INSERT INTO project_jobs(job_id,project_id,binding_revision,role,brief_digest,recorded_at) VALUES('job-test','proj-test',1,'implementation','brief','time');
INSERT INTO project_lanes(lane_id,project_id,owner_job_id,role,base_commit,fsfork_driver,fsfork_path,created_at) VALUES('lane-test','proj-test','job-test','implementation','base','git_worktree','/fixture','time');`
	_, err = db.Exec(fixture)
	if err != nil {
		t.Fatal(err)
	}

	prepare := func(body, path string) string {
		t.Helper()
		var random [32]byte
		if _, err := rand.Read(random[:]); err != nil {
			t.Fatal(err)
		}
		launch := hex.EncodeToString(random[:])
		done := make(chan error, 1)
		go func() { _, err := core.ReceiveLocalComposeIntent(state, launch); done <- err }()
		var rendezvous core.ComposeRendezvous
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			rendezvous, err = core.LocalComposeRendezvous(state, launch)
			if err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil {
			t.Fatal("receiver did not publish rendezvous")
		}
		key, err := hex.DecodeString(rendezvous.Capability)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rand.Read(random[:]); err != nil {
			t.Fatal(err)
		}
		nonce := hex.EncodeToString(random[:])
		proof := func(role string) string {
			h := hmac.New(sha256.New, key)
			_, _ = h.Write([]byte("ocq-compose-v1:" + role + ":" + nonce))
			return hex.EncodeToString(h.Sum(nil))
		}
		conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", rendezvous.Port), 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		r := bufio.NewReader(conn)
		line := func() string {
			t.Helper()
			s, err := r.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			return strings.TrimSuffix(s, "\n")
		}
		_, _ = fmt.Fprintln(conn, nonce)
		if line() != proof("server") {
			t.Fatal("server proof mismatch")
		}
		_, _ = fmt.Fprintln(conn, proof("client"))
		if line() != "READY" {
			t.Fatal("client proof rejected")
		}
		request, err := json.Marshal(map[string]string{"ProjectID": "proj-test", "LaneID": "lane-test", "JobID": "job-test", "From": "lane:lane-test", "To": "integrator:proj-test", "Kind": "lane.blocked", "Subject": "fixture", "Body": body, "FsforkPath": path})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write(append(request, '\n')); err != nil {
			t.Fatal(err)
		}
		reply := line()
		if !strings.HasPrefix(reply, "PREPARED ") {
			t.Fatal("receiver failed to prepare")
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		return strings.TrimPrefix(reply, "PREPARED ")
	}

	first := prepare("first fixture", "/fixture")
	child := exec.Command(os.Args[0], "-test.run=^TestComposeDispatchRecovery$")
	child.Env = append(os.Environ(), "OCQ_COMPOSE_DISPATCH_CHILD=1", "OCQ_COMPOSE_DISPATCH_STATE="+state, "OCQ_COMPOSE_DISPATCH_ID="+first)
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	}()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "marked\n" {
		t.Fatalf("child did not mark intent: %v", err)
	}
	status, err := core.ReconcileLocalComposeIntent(state, first)
	if err != nil || status.State != "dispatch_intent" || status.Observation != "not_observed" {
		t.Fatalf("missing row not held uncertain: %+v %v", status, err)
	}
	if _, err := core.DispatchLocalComposeIntent(state, first); err == nil {
		t.Fatal("uncertain intent was resent")
	}
	if err := core.RetireLocalComposeIntent(state, first); err == nil {
		t.Fatal("uncertain intent was retired")
	}
	if _, err := stdin.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	err = child.Wait()
	waited = true
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 23 {
		t.Fatalf("child did not simulate lost acknowledgement: %v", err)
	}
	status, err = core.ReconcileLocalComposeIntent(state, first)
	if err != nil || status.State != "completed" || status.Observation != "matched" {
		t.Fatalf("exact read-back did not resolve: %+v %v", status, err)
	}
	if err := core.RetireLocalComposeIntent(state, first); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state, "herdr-compose-retired-"+first, "intent.json")); err != nil {
		t.Fatal("retired intent was not retained")
	}

	cancelled := prepare("cancelled fixture", "")
	status, err = core.CancelLocalComposeIntent(state, cancelled)
	if err != nil || status.State != "cancelled" {
		t.Fatalf("cancel failed: %+v %v", status, err)
	}
	if err := core.RetireLocalComposeIntent(state, cancelled); err != nil {
		t.Fatal(err)
	}
	// Suspend a real dispatch child at exec, before Go can acquire the journal
	// lock. A different view revokes and retires its claim, then prepares a new
	// message before the delayed child is allowed to run.
	lateID := prepare("late fixture", "")
	armed, err := core.ArmLocalComposeIntent(state, lateID)
	if err != nil || armed.State != "armed" {
		t.Fatalf("late dispatch claim failed: %+v %v", armed, err)
	}
	late := exec.Command(os.Args[0], "-test.run=^TestComposeDispatchRecovery$")
	late.Env = append(os.Environ(), "OCQ_COMPOSE_LOCKED_DISPATCH_CHILD=1", "OCQ_COMPOSE_DISPATCH_STATE="+state, "OCQ_COMPOSE_DISPATCH_ID="+lateID)
	late.SysProcAttr = &syscall.SysProcAttr{Ptrace: true}
	// Linux associates ptrace ownership with the tracing OS thread, not the Go
	// goroutine; keep start, stop and detach on that same thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := late.Start(); err != nil {
		t.Fatal(err)
	}
	lateWaited := false
	defer func() {
		if !lateWaited {
			_ = late.Process.Kill()
			_ = syscall.PtraceDetach(late.Process.Pid)
			_ = late.Wait()
		}
	}()
	var stopped syscall.WaitStatus
	if _, err := syscall.Wait4(late.Process.Pid, &stopped, 0, nil); err != nil || !stopped.Stopped() || stopped.StopSignal() != syscall.SIGTRAP {
		t.Fatalf("dispatch child did not stop before execution: %v %v", stopped, err)
	}
	discovered, err := core.LocalComposeStatus(state)
	if err != nil || discovered.State != "armed" || discovered.ID != lateID {
		t.Fatalf("another view did not discover the claim: %+v %v", discovered, err)
	}
	if _, err := core.ArmLocalComposeIntent(state, lateID); err == nil {
		t.Fatal("armed intent was reconfirmed")
	}
	if err := core.RetireLocalComposeIntent(state, lateID); err == nil {
		t.Fatal("armed intent was retired without revocation")
	}
	revoked, err := core.CancelLocalComposeIntent(state, lateID)
	if err != nil || revoked.State != "cancelled" || revoked.Observation != "revoked_before_attempt" {
		t.Fatalf("unattempted claim was not revoked: %+v %v", revoked, err)
	}
	if err := core.RetireLocalComposeIntent(state, lateID); err != nil {
		t.Fatal(err)
	}
	replacement := prepare("replacement fixture", "")
	if err := syscall.PtraceDetach(late.Process.Pid); err != nil {
		t.Fatal(err)
	}
	err = late.Wait()
	lateWaited = true
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Fatalf("revoked late dispatch was not refused: %v", err)
	}
	discovered, err = core.LocalComposeStatus(state)
	if err != nil || discovered.State != "prepared" || discovered.ID != replacement {
		t.Fatalf("late dispatch changed replacement: %+v %v", discovered, err)
	}
	var beforeSecond int
	if err := db.QueryRow("SELECT count(*) FROM project_inbox_messages").Scan(&beforeSecond); err != nil || beforeSecond != 1 {
		t.Fatalf("late dispatch sent mail: %d %v", beforeSecond, err)
	}
	if _, err := core.CancelLocalComposeIntent(state, replacement); err != nil {
		t.Fatal(err)
	}
	if err := core.RetireLocalComposeIntent(state, replacement); err != nil {
		t.Fatal(err)
	}
	second := prepare("second fixture", "")
	intent, err := core.ReadLocalComposeIntent(state, second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.DispatchLocalComposeIntent(state, second); err == nil {
		t.Fatal("unclaimed prepared intent was dispatched")
	}
	armed, err = core.ArmLocalComposeIntent(state, second)
	if err != nil || armed.State != "armed" {
		t.Fatalf("explicit claim failed: %+v %v", armed, err)
	}
	status, err = core.DispatchLocalComposeIntent(state, second)
	if err != nil || status.State != "completed" || status.Observation != "matched" {
		t.Fatalf("explicit dispatch failed: %+v %v", status, err)
	}
	if _, err := core.DispatchLocalComposeIntent(state, second); err == nil {
		t.Fatal("completed intent was resent")
	}
	reader, err := core.OpenReader(state)
	if err != nil {
		t.Fatal(err)
	}
	message, err := reader.MailByKey("proj-test", intent.Key)
	_ = reader.Close()
	if err != nil || message.Body != "second fixture" || message.FsforkPath != "/fixture" {
		t.Fatalf("normalized stored message mismatch: %v", err)
	}
	if err := core.RetireLocalComposeIntent(state, second); err != nil {
		t.Fatal(err)
	}
	var count int
	var jobState string
	if err := db.QueryRow("SELECT count(*) FROM project_inbox_messages").Scan(&count); err != nil || count != 2 {
		t.Fatalf("unexpected message count %d: %v", count, err)
	}
	if err := db.QueryRow("SELECT state FROM jobs WHERE id='job-test'").Scan(&jobState); err != nil || jobState != "unknown" {
		t.Fatalf("unknown job capacity was altered: %v", err)
	}
	// A retained row with the right key but different content is not a receipt
	// for this intent, and must remain blocked rather than become retryable.
	mismatched := prepare("expected fixture", "/fixture")
	mismatchIntent, err := core.ReadLocalComposeIntent(state, mismatched)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.MarkLocalComposeDispatchIntent(state, mismatched); err != nil {
		t.Fatal(err)
	}
	var other core.MailRequest
	if err := json.Unmarshal(mismatchIntent.Request, &other); err != nil {
		t.Fatal(err)
	}
	other.Key, other.Body = mismatchIntent.Key, "different fixture"
	sender, err := core.OpenExistingMailSender(state)
	if err != nil {
		t.Fatal(err)
	}
	_, sendErr := sender.Send(other)
	closeErr := sender.Close()
	if sendErr != nil || closeErr != nil {
		t.Fatalf("fixture insert failed: %v %v", sendErr, closeErr)
	}
	status, err = core.ReconcileLocalComposeIntent(state, mismatched)
	if err != nil || status.State != "dispatch_intent" || status.Observation != "mismatch" {
		t.Fatalf("mismatched content was not held uncertain: %+v %v", status, err)
	}
	if err := core.RetireLocalComposeIntent(state, mismatched); err == nil {
		t.Fatal("mismatched content was retired")
	}

	// Kill the actual dispatch process only after its durable marker appears
	// while a separate connection holds SQLite's real writer lock.
	state = filepath.Join(root, "locked-queue")
	q, err = core.OpenQueue(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Close(); err != nil {
		t.Fatal(err)
	}
	lockedDB, err := sql.Open("sqlite", "file:"+filepath.Join(state, "queue.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer lockedDB.Close()
	if _, err = lockedDB.Exec(fixture); err != nil {
		t.Fatal(err)
	}
	lockedID := prepare("locked fixture", "")
	armed, err = core.ArmLocalComposeIntent(state, lockedID)
	if err != nil || armed.State != "armed" {
		t.Fatalf("locked dispatch claim failed: %+v %v", armed, err)
	}
	conn, err := lockedDB.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(t.Context(), "ROLLBACK")
	lockedChild := exec.Command(os.Args[0], "-test.run=^TestComposeDispatchRecovery$")
	lockedChild.Env = append(os.Environ(), "GOTRACEBACK=all", "OCQ_COMPOSE_LOCKED_DISPATCH_CHILD=1", "OCQ_COMPOSE_DISPATCH_STATE="+state, "OCQ_COMPOSE_DISPATCH_ID="+lockedID)
	var trace bytes.Buffer
	lockedChild.Stderr = &trace
	if err = lockedChild.Start(); err != nil {
		t.Fatal(err)
	}
	lockedWaited := false
	defer func() {
		if !lockedWaited {
			_ = lockedChild.Process.Kill()
			_ = lockedChild.Wait()
		}
	}()
	deadline := time.Now().Add(4 * time.Second)
	for {
		b, readErr := os.ReadFile(filepath.Join(state, "herdr-compose", "attempt.json"))
		var marker struct {
			Version int    `json:"version"`
			ID      string `json:"id"`
		}
		if readErr == nil && json.Unmarshal(b, &marker) == nil && marker.Version == 1 && marker.ID == lockedID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("dispatch did not persist marker while SQLite was locked")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The child must still own the journal lock after the marker, while the
	// independent BEGIN IMMEDIATE prevents its transaction from committing.
	time.Sleep(200 * time.Millisecond)
	busy, err := core.LocalComposeStatus(state)
	if err != nil || busy.State != "busy" {
		t.Fatalf("dispatch did not remain active: %+v %v", busy, err)
	}
	if err = lockedChild.Process.Signal(syscall.SIGQUIT); err != nil {
		t.Fatal(err)
	}
	err = lockedChild.Wait()
	lockedWaited = true
	if _, ok := err.(*exec.ExitError); !ok {
		t.Fatalf("dispatch child was not terminated: %v", err)
	}
	if !bytes.Contains(trace.Bytes(), []byte("ocq/internal/core.(*Queue).sendMail")) || !bytes.Contains(trace.Bytes(), []byte("ocq/internal/core.(*store).write")) || !bytes.Contains(trace.Bytes(), []byte("modernc.org/sqlite")) {
		t.Fatal("terminated child was not observed inside the real SQLite write transaction")
	}
	if _, err = conn.ExecContext(t.Context(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	status, err = core.ReconcileLocalComposeIntent(state, lockedID)
	if err != nil || status.State != "dispatch_intent" || status.Observation != "not_observed" {
		t.Fatalf("killed dispatch not held uncertain: %+v %v", status, err)
	}
	if _, err = core.CancelLocalComposeIntent(state, lockedID); err == nil {
		t.Fatal("attempted dispatch was revoked")
	}
	if _, err = core.DispatchLocalComposeIntent(state, lockedID); err == nil {
		t.Fatal("killed dispatch was resent")
	}
	if err = lockedDB.QueryRow("SELECT count(*) FROM project_inbox_messages").Scan(&count); err != nil || count != 0 {
		t.Fatalf("unexpected message after killed dispatch: %d %v", count, err)
	}
}
