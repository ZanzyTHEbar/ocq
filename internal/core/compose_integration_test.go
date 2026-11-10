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
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"ocq/internal/core"
)

// A real loopback and private SQLite scenario exercises the cross-process lock,
// authenticated transport, durable intent and explicit marker without sending.
func TestComposeIntentBoundary(t *testing.T) {
	if os.Getenv("OCQ_COMPOSE_TEST_CHILD") == "1" {
		status, err := core.ReceiveLocalComposeIntent(os.Getenv("OCQ_COMPOSE_TEST_STATE"), os.Getenv("OCQ_COMPOSE_TEST_LAUNCH"))
		if err != nil {
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(status)
		os.Exit(0)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(filepath.Join(home, ".local", "state"), "ocq-compose-acceptance-")
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
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	launch := hex.EncodeToString(random[:])
	child := exec.Command(os.Args[0], "-test.run=^TestComposeIntentBoundary$")
	child.Env = append(os.Environ(), "OCQ_COMPOSE_TEST_CHILD=1", "OCQ_COMPOSE_TEST_STATE="+state, "OCQ_COMPOSE_TEST_LAUNCH="+launch)
	var output bytes.Buffer
	child.Stdout = &output
	child.Stderr = &output
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
	status, err := core.LocalComposeStatus(state)
	if err != nil || status.State != "busy" {
		t.Fatalf("cross-process lock: state=%s err=%v", status.State, err)
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
		t.Fatal("receiver did not authenticate client")
	}
	request := []byte(`{"ProjectID":"proj-test","LaneID":"lane-test","JobID":"job-test","From":"lane:lane-test","To":"integrator:proj-test","Kind":"lane.blocked","Subject":"fixture","Body":"fixture body"}`)
	if _, err := conn.Write(append(append([]byte(nil), request...), '\n')); err != nil {
		t.Fatal(err)
	}
	reply := line()
	if !strings.HasPrefix(reply, "PREPARED ") {
		t.Fatal("receiver did not prepare intent")
	}
	id := strings.TrimPrefix(reply, "PREPARED ")
	if err := child.Wait(); err != nil {
		waited = true
		t.Fatal("receiver process failed")
	}
	waited = true
	if bytes.Contains(output.Bytes(), []byte("fixture body")) {
		t.Fatal("receiver printed user content")
	}
	intent, err := core.ReadLocalComposeIntent(state, id)
	if err != nil || intent.Status.State != "prepared" || len(intent.Key) != 64 || !bytes.Equal(intent.Request, request) {
		t.Fatalf("exact intent could not be recovered: state=%s err=%v", intent.Status.State, err)
	}
	// A separate loopback connection can preview only after authenticating;
	// a failed capability consumes that one-shot listener without exposing text.
	preview := func(valid bool) {
		t.Helper()
		if _, err := rand.Read(random[:]); err != nil {
			t.Fatal(err)
		}
		previewLaunch := hex.EncodeToString(random[:])
		done := make(chan error, 1)
		go func() { _, err := core.PreviewLocalComposeIntent(state, id, previewLaunch); done <- err }()
		deadline := time.Now().Add(5 * time.Second)
		var entry core.ComposeRendezvous
		for time.Now().Before(deadline) {
			entry, err = core.LocalComposeRendezvous(state, previewLaunch)
			if err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err != nil {
			t.Fatal("preview rendezvous unavailable")
		}
		if _, err := core.CancelLocalComposeIntent(state, id); err == nil {
			t.Fatal("preview did not exclude concurrent cancellation")
		}
		key, err := hex.DecodeString(entry.Capability)
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
		conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", entry.Port), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		r := bufio.NewReader(conn)
		_, _ = fmt.Fprintln(conn, nonce)
		server, err := r.ReadString('\n')
		if err != nil || strings.TrimSuffix(server, "\n") != proof("server") {
			t.Fatal("preview server proof mismatch")
		}
		clientProof := proof("client")
		if !valid {
			clientProof = strings.Repeat("0", 64)
		}
		_, _ = fmt.Fprintln(conn, clientProof)
		result, err := r.ReadString('\n')
		if !valid {
			if err == nil || result != "" || <-done == nil {
				t.Fatal("unauthenticated preview exposed data")
			}
			return
		}
		if err != nil || result != "READY\n" {
			t.Fatal("preview client proof rejected")
		}
		payload, err := r.ReadString('\n')
		if err != nil || !bytes.Equal([]byte(strings.TrimSuffix(payload, "\n")), request) {
			t.Fatal("preview differs from retained request")
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	preview(false)
	preview(true)
	preview(true)
	if _, err := core.PreviewLocalComposeIntent(state, strings.Repeat("0", 64), launch); err == nil {
		t.Fatal("preview accepted a different intent")
	}
	for _, item := range []struct {
		path string
		mode os.FileMode
	}{
		{filepath.Join(state, "herdr-compose"), os.ModeDir | 0700},
		{filepath.Join(state, "herdr-compose", "intent.json"), 0600},
	} {
		info, err := os.Lstat(item.path)
		if err != nil || info.Mode() != item.mode {
			t.Fatalf("private journal permissions: %v", err)
		}
	}
	marked, err := core.MarkLocalComposeDispatchIntent(state, id)
	if err != nil || marked.State != "dispatch_intent" {
		t.Fatalf("explicit dispatch intent: %+v %v", marked, err)
	}
	if _, err := core.MarkLocalComposeDispatchIntent(state, id); err == nil {
		t.Fatal("dispatch intent was silently retried")
	}
	if _, err := core.PreviewLocalComposeIntent(state, id, launch); err == nil {
		t.Fatal("dispatched intent could be previewed as prepared")
	}
	if _, err := core.ReceiveLocalComposeIntent(state, launch); err == nil {
		t.Fatal("unresolved intent did not block a second preparation")
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(state, "queue.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM project_inbox_messages").Scan(&count); err != nil || count != 0 {
		t.Fatalf("preparation unexpectedly sent mail: count=%d err=%v", count, err)
	}
	// A replaced journal path must not redirect an operation outside the queue.
	journal := filepath.Join(state, "herdr-compose")
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(journal, filepath.Join(root, "retained-journal")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, journal); err != nil {
		t.Fatal(err)
	}
	if _, err := core.LocalComposeStatus(state); err == nil {
		t.Fatal("journal directory symlink was accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("journal symlink redirected writes: %v", err)
	}
}
