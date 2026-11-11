package core

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real bridge, loopback transport, private SQLite and an owned delayed process.
func TestProjectIntakeTransportAndOldDescriptor(t *testing.T) {
	if os.Getenv("OCQ_INTAKE_DESCRIPTOR_CHILD") == "1" {
		state := os.Getenv("OCQ_INTAKE_STATE")
		j, err := openIntakeJournal(state)
		if err != nil {
			os.Exit(2)
		}
		defer j.close()
		_, _ = fmt.Fprintln(os.Stdout, "opened")
		var b [1]byte
		if _, err := io.ReadFull(os.Stdin, b[:]); err != nil {
			os.Exit(3)
		}
		_, err = dispatchIntakeWithJournal(j, state, os.Getenv("OCQ_INTAKE_ID"))
		if err == nil || !strings.Contains(err.Error(), "journal identity changed") {
			os.Exit(4)
		}
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(filepath.Join(home, ".local", "state"), "ocq-intake-transport-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	state := filepath.Join(root, "queue")
	q, err := OpenQueue(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	bridge := filepath.Join(root, "ocq-herdr")
	build := exec.Command("go", "build", "-p", "1", "-o", bridge, "../../cmd/ocq-herdr")
	build.Env = append(os.Environ(), "GOMAXPROCS=2")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build bridge: %v %s", err, output)
	}
	// This real bridge caller speaks the same versioned mutation contract as the
	// mod. No test bypass: the action process rechecks this image/context itself.
	handshake := exec.Command(bridge, "compatibility", "sp-transport", root)
	handshake.Env = append(os.Environ(), "OCQ_STATE_DIR="+state)
	b, err := handshake.Output()
	if err != nil {
		t.Fatal(err)
	}
	var compat struct {
		Protocol string            `json:"protocol"`
		Image    string            `json:"image_sha256"`
		Context  string            `json:"bridge_context"`
		Queue    string            `json:"queue_identity"`
		Actions  map[string]string `json:"actions"`
	}
	if err := json.Unmarshal(b, &compat); err != nil {
		t.Fatal(err)
	}
	compatArgs := []string{"--mod-contract", compat.Protocol, "--mod-image", compat.Image, "--mod-context", compat.Context, "--mod-space", "sp-transport", "--mod-folder", root, "--mod-queue", compat.Queue}
	handoff := []byte(`{"version":1,"kind":"handoff","project_id":"proj-transport","source":"operator_import","observed_at":null,"intent":"fixture","decisions":[],"risks":[]}`)
	revision := 0
	repo := filepath.Join(root, "missing-repo")
	req := ProjectRegistrationRequest{Version: 1, Key: "transport-key", ProjectID: "proj-transport", Label: "Test é", ExpectedRevision: &revision, Repo: repo, CommitAnchor: strings.Repeat("a", 40), ElephantSessionID: "ses_fixture", ExpectedOpenCodeProjectID: "fixture", ExpectedOpenCodeDirectory: repo, AuthRef: "fixture", HandoffManifestDigest: sha256Hex(handoff), HandoffManifest: handoff}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(append([]byte(" \n"), raw...), '\n')

	start := func(args ...string) (*exec.Cmd, *bytes.Buffer) {
		t.Helper()
		cmd := exec.Command(bridge, append(append(args, compatArgs...), "--mod-action", compat.Actions[args[0]])...)
		cmd.Env = append(os.Environ(), "OCQ_STATE_DIR="+state)
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		})
		return cmd, &output
	}
	connect := func(launch string, bad bool) (net.Conn, *bufio.Reader) {
		t.Helper()
		var entry ComposeRendezvous
		deadline := time.Now().Add(5 * time.Second)
		for {
			entry, err = LocalProjectIntakeRendezvous(state, launch)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("rendezvous: %v", err)
			}
			time.Sleep(10 * time.Millisecond)
		}
		conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", entry.Port), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		r := bufio.NewReader(conn)
		nonce, err := composeRandom()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprintln(conn, nonce)
		proof, err := r.ReadString('\n')
		if err != nil || strings.TrimSuffix(proof, "\n") != composeProof(entry.Capability, nonce, "server") {
			t.Fatal("server authentication failed")
		}
		clientProof := composeProof(entry.Capability, nonce, "client")
		if bad {
			clientProof = strings.Repeat("0", 64)
		}
		_, _ = fmt.Fprintln(conn, clientProof)
		if !bad {
			ready, err := r.ReadString('\n')
			if err != nil || ready != "READY\n" {
				t.Fatal("client authentication failed")
			}
		}
		return conn, r
	}
	launch, _ := composeRandom()
	denied, deniedOutput := start("intake-receive", "registration", launch)
	conn, _ := connect(launch, true)
	_ = conn.Close()
	if err := denied.Wait(); err == nil {
		t.Fatal("bad authentication accepted")
	}
	if bytes.Contains(deniedOutput.Bytes(), raw) {
		t.Fatal("payload leaked to output")
	}
	if s, err := LocalProjectIntakeStatus(state); err != nil || s.State != "idle" {
		t.Fatalf("rejected authentication prepared intake: %+v %v", s, err)
	}
	launch, _ = composeRandom()
	oversize, _ := start("intake-receive", "registration", launch)
	conn, _ = connect(launch, false)
	var tooLarge [4]byte
	binary.BigEndian.PutUint32(tooLarge[:], composeLimit+1)
	if _, err := conn.Write(tooLarge[:]); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if err := oversize.Wait(); err == nil {
		t.Fatal("oversized frame accepted")
	}
	if s, err := LocalProjectIntakeStatus(state); err != nil || s.State != "idle" {
		t.Fatalf("oversized frame prepared intake: %+v %v", s, err)
	}

	launch, _ = composeRandom()
	receiver, receiverOutput := start("intake-receive-text", "registration", launch)
	conn, r := connect(launch, false)
	if _, err := fmt.Fprintf(conn, "%d\n", len(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(raw); err != nil {
		t.Fatal(err)
	}
	line, err := r.ReadString('\n')
	_ = conn.Close()
	if err != nil || !strings.HasPrefix(line, "PREPARED ") {
		t.Fatalf("prepare acknowledgement: %v", err)
	}
	id := strings.TrimSpace(strings.TrimPrefix(line, "PREPARED "))
	if err := receiver.Wait(); err != nil {
		t.Fatalf("receiver: %v %s", err, receiverOutput)
	}
	if bytes.Contains(receiverOutput.Bytes(), raw) {
		t.Fatal("payload leaked to output")
	}
	checkPreview := func(intentID, expectedState string) {
		t.Helper()
		launch, _ := composeRandom()
		preview, previewOutput := start("intake-preview", intentID, launch)
		conn, r := connect(launch, false)
		var header [5]byte
		if _, err := io.ReadFull(r, header[:]); err != nil {
			t.Fatal(err)
		}
		if header[0] != 'R' || binary.BigEndian.Uint32(header[1:]) != uint32(len(raw)) {
			t.Fatal("bad preview frame")
		}
		got := make([]byte, len(raw))
		if _, err := io.ReadFull(r, got); err != nil || !bytes.Equal(got, raw) {
			t.Fatal("preview did not preserve exact bytes")
		}
		_ = conn.Close()
		if err := preview.Wait(); err != nil {
			t.Fatalf("preview: %v %s", err, previewOutput)
		}
		if bytes.Contains(previewOutput.Bytes(), raw) {
			t.Fatal("preview leaked to output")
		}
		var status ProjectIntakeStatus
		if json.Unmarshal(previewOutput.Bytes(), &status) != nil || status.ID != intentID || status.State != expectedState {
			t.Fatalf("preview state: %+v", status)
		}
	}
	checkPreview(id, "prepared")
	if _, err := ArmLocalProjectIntake(state, id); err != nil {
		t.Fatal(err)
	}
	checkPreview(id, "armed")
	child := exec.Command(os.Args[0], "-test.run=^TestProjectIntakeTransportAndOldDescriptor$")
	child.Env = append(os.Environ(), "OCQ_INTAKE_DESCRIPTOR_CHILD=1", "OCQ_INTAKE_STATE="+state, "OCQ_INTAKE_ID="+id)
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
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "opened\n" {
		t.Fatalf("old descriptor not opened: %v", err)
	}
	if _, err := CancelLocalProjectIntake(state, id); err != nil {
		t.Fatal(err)
	}
	checkPreview(id, "cancelled")
	if err := RetireLocalProjectIntake(state, id); err != nil {
		t.Fatal(err)
	}
	replacement, err := PrepareLocalProjectIntake(state, "registration", raw)
	if err != nil || replacement.ID == id {
		t.Fatalf("replacement: %+v %v", replacement, err)
	}
	if _, err := stdin.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	err = child.Wait()
	waited = true
	if err != nil {
		t.Fatalf("old descriptor dispatch not refused: %v", err)
	}
	if status, err := LocalProjectIntakeStatus(state); err != nil || status.ID != replacement.ID || status.State != "prepared" {
		t.Fatalf("replacement affected: %+v %v", status, err)
	}
	reader, err := OpenReader(state)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.RegistrationReceipt(raw); err == nil {
		t.Fatal("delayed process admitted registration")
	}
	if _, err := ArmLocalProjectIntake(state, replacement.ID); err != nil {
		t.Fatal(err)
	}
	if status, err := DispatchLocalProjectIntake(state, replacement.ID); err != nil || status.State != "attempted" {
		t.Fatalf("failed live registration not held uncertain: %+v %v", status, err)
	}
	checkPreview(replacement.ID, "attempted")
}
