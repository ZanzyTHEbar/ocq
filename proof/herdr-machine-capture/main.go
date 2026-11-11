// One real private capture flow on the publication fixture's disposable repo.
// No agent/session/service runs. The existing publication assertions run intact.
package main

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"ocq/internal/core"
)

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func check(b bool, s string) {
	if !b {
		panic(s)
	}
}
func encode(v any) []byte  { b, e := json.Marshal(v); must(e); return b }
func hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func random() string {
	b := make([]byte, 32)
	_, e := rand.Read(b)
	must(e)
	return hex.EncodeToString(b)
}
func auth(cap, nonce, role string) string {
	k, e := hex.DecodeString(cap)
	must(e)
	h := hmac.New(sha256.New, k)
	h.Write([]byte("ocq-compose-v1:" + role + ":" + nonce))
	return hex.EncodeToString(h.Sum(nil))
}

func main() {
	check(len(os.Args) == 5, "usage: machine-capture BRIDGE PUBLICATION_PROOF FRESH_PRIVATE_ROOT SOURCE_REPO")
	bridge, publication, root, source := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	c := exec.Command(publication, bridge, root, source)
	b, e := c.Output()
	must(e)
	var seed struct{ State, Repo, Project, Job, Supersedes string }
	var fields map[string]json.RawMessage
	must(json.Unmarshal(b, &fields))
	for k, p := range map[string]*string{"state_dir": &seed.State, "repo": &seed.Repo, "project": &seed.Project, "job": &seed.Job, "native_supersedes": &seed.Supersedes} {
		must(json.Unmarshal(fields[k], p))
	}
	must(os.WriteFile(filepath.Join(root, "publication-proof.json"), b, 0600))
	call := func(a ...string) ([]byte, error) {
		c := exec.Command(bridge, a...)
		c.Env = append(os.Environ(), "OCQ_STATE_DIR="+seed.State)
		b, e := c.Output()
		check(!bytes.Contains(b, []byte("PRIVATE_")), "private bytes in DTO")
		return b, e
	}
	file := filepath.Join(seed.Repo, "capture.txt")
	must(os.WriteFile(file, []byte("PRIVATE_CLI_SNAPSHOT\n"), 0600))
	q, e := core.OpenQueue(seed.State)
	must(e)
	request := core.CaptureRequest{ProjectID: seed.Project, JobID: seed.Job, Worktree: seed.Repo, Supersedes: seed.Supersedes, IgnoredExclusions: []string{"ignored/external inputs not captured"}}
	a, e := q.CaptureResult(request)
	must(e)
	retry, e := q.CaptureResult(request)
	must(e)
	check(a.ID == retry.ID && a.Verification == "machine_verified_snapshot" && a.ClaimedValidationStatus == "not_run", "existing capture behavior changed")
	must(q.Close())
	must(os.WriteFile(file, []byte("PRIVATE_RETAINED_CAPTURE opérateur\u202e\u200b\n"), 0600))
	r, e := core.OpenReader(seed.State)
	must(e)
	ctx, e := r.CapturePublicationContext("sp-capture-fixture", seed.Project, seed.Job)
	must(e)
	must(r.Close())
	opts := core.CapturePreparationRequest{Context: ctx, IgnoredExclusions: []string{"PRIVATE_IGNORED_EXCLUSION"}, Supersedes: a.ID}
	args := func(action, id string, extra ...string) []string {
		a := []string{"capture-" + action, ctx.Target.Space, ctx.Target.Queue, seed.Project, seed.Job, ctx.Target.Sequence}
		if id != "" {
			a = append(a, id)
		}
		return append(a, extra...)
	}
	exchange := func(mode, id, pd string, raw []byte) (core.CapturePublicationStatus, []byte, error) {
		launch := random()
		a := args(mode, id, launch)
		if mode == "preview" {
			a = args(mode, id, pd, launch)
		}
		c := exec.Command(bridge, a...)
		c.Env = append(os.Environ(), "OCQ_STATE_DIR="+seed.State)
		var out, stderr bytes.Buffer
		c.Stdout = &out
		c.Stderr = &stderr
		must(c.Start())
		defer func() {
			if c.ProcessState == nil {
				_ = c.Process.Kill()
				_ = c.Wait()
			}
		}()
		var entry core.ComposeRendezvous
		deadline := time.Now().Add(10 * time.Second)
		for {
			entry, e = core.CapturePublicationRendezvous(seed.State, ctx.Target, launch)
			if e == nil {
				break
			}
			check(time.Now().Before(deadline), "rendezvous missing")
			time.Sleep(20 * time.Millisecond)
		}
		conn, e := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", entry.Port), time.Second)
		must(e)
		defer conn.Close()
		must(conn.SetDeadline(time.Now().Add(5 * time.Second)))
		reader := bufio.NewReader(conn)
		nonce := random()
		_, e = fmt.Fprintln(conn, nonce)
		must(e)
		line, e := reader.ReadString('\n')
		must(e)
		check(line == auth(entry.Capability, nonce, "server")+"\n", "server auth")
		_, e = fmt.Fprintln(conn, auth(entry.Capability, nonce, "client"))
		must(e)
		line, e = reader.ReadString('\n')
		must(e)
		check(line == "READY\n", "client auth")
		var exact []byte
		if mode == "prepare" {
			_, e = fmt.Fprintf(conn, "%d\n", len(raw))
			must(e)
			_, e = conn.Write(raw)
			must(e)
			_, _ = reader.ReadString('\n')
		} else {
			var h [4]byte
			_, e = io.ReadFull(reader, h[:])
			must(e)
			n := binary.BigEndian.Uint32(h[:])
			check(n > 0 && n <= 131072, "frame bound")
			exact = make([]byte, n)
			_, e = io.ReadFull(reader, exact)
			must(e)
		}
		must(conn.Close())
		err := c.Wait()
		check(!bytes.Contains(out.Bytes(), []byte("PRIVATE_")) && !bytes.Contains(stderr.Bytes(), []byte("PRIVATE_")), "private transport diagnostics")
		var status core.CapturePublicationStatus
		if err == nil {
			must(json.Unmarshal(out.Bytes(), &status))
		}
		return status, exact, err
	}
	db, e := sql.Open("sqlite", filepath.Join(seed.State, "queue.db"))
	must(e)
	defer db.Close()
	scalar := func(query string, a ...any) string { var s string; must(db.QueryRow(query, a...).Scan(&s)); return s }
	before := scalar("SELECT count(*) FROM project_result_associations")
	wrong := opts
	wrong.Context.Lane = "lane-wrong-owner"
	_, _, e = exchange("prepare", random(), "", encode(wrong))
	check(e != nil, "wrong lane accepted")
	id := random()
	prepared, _, e := exchange("prepare", id, "", encode(opts))
	must(e)
	check(prepared.State == "prepared" && prepared.Source == "machine_capture", "no real prepared capture")
	check(scalar("SELECT count(*) FROM project_result_associations") == before, "prepare published")
	_, exact, e := exchange("preview", id, prepared.PayloadDigest, nil)
	must(e)
	check(hash(exact) == prepared.PayloadDigest, "retained payload mismatch")
	var payload core.ResultPublicationPayload
	must(json.Unmarshal(exact, &payload))
	check(hash([]byte(payload.Manifest)) == prepared.ManifestDigest && strings.Contains(payload.Manifest, "PRIVATE_RETAINED_CAPTURE"), "snapshot bytes lost")
	must(os.WriteFile(filepath.Join(root, "retained-capture.json"), []byte(payload.Manifest), 0600))
	must(os.WriteFile(file, []byte("PRIVATE_POST_PREPARE_DRIFT\n"), 0600))
	_, e = call(args("prepare", id, random())...)
	check(e != nil, "same pointer silently recaptured")
	// Import boundary must still refuse an externally supplied machine manifest.
	w, e := core.OpenExistingResultPublisher(seed.State)
	must(e)
	_, e = w.Publish(payload)
	check(e != nil, "machine capture imported through operator boundary")
	must(w.Close())
	wrongTarget := ctx.Target
	wrongTarget.Space = "sp-wrong"
	_, e = core.ArmCapturePublication(seed.State, wrongTarget, id, prepared.PayloadDigest)
	check(e != nil, "wrong context armed")
	_, e = call(args("arm", id, prepared.PayloadDigest)...)
	must(e)
	lost := exec.Command("/bin/sh", append([]string{"-c", `"$@" >/dev/null || exit $?; exit 73`, "lost-capture", bridge}, args("attempt", id, prepared.PayloadDigest)...)...)
	lost.Env = append(os.Environ(), "OCQ_STATE_DIR="+seed.State)
	e = lost.Run()
	check(e != nil && lost.ProcessState.ExitCode() == 73, "lost ack harness failed")
	b, e = call(args("status", id)...)
	must(e)
	var observed core.CapturePublicationStatus
	must(json.Unmarshal(b, &observed))
	check(observed.State == "recorded_observed" && observed.Association != nil && observed.ManifestDigest == prepared.ManifestDigest && observed.Association.Source == "machine_capture" && observed.Association.Verification == "machine_verified_snapshot" && *observed.Association.Supersedes == a.ID, "exact machine recovery failed")
	var stored []byte
	must(db.QueryRow("SELECT canonical FROM project_manifests WHERE digest=?", prepared.ManifestDigest).Scan(&stored))
	check(bytes.Equal(stored, []byte(payload.Manifest)), "publish changed retained bytes")
	check(scalar("SELECT sealed_manifest_digest FROM project_lanes WHERE owner_job_id=?", seed.Job) == prepared.ManifestDigest && scalar("SELECT count(*) FROM project_inbox_messages WHERE assoc_id=? AND kind='work.submitted'", observed.Association.ID) == "1", "capture association/lane/mail not atomic")
	_, e = call(args("attempt", id, prepared.PayloadDigest)...)
	check(e != nil, "capture retried attempt")
	b, e = call("verify-worktree", seed.Project, seed.Job, observed.Association.ID, prepared.ManifestDigest, seed.Repo)
	must(e)
	check(bytes.Contains(b, []byte(`"worktree_match":"modified_since_snapshot"`)) && bytes.Contains(b, []byte(`"validation":"not_run"`)), "fresh drift check not reported")
	check(scalar("SELECT count(*) FROM jobs WHERE state='queued' AND validation='not_run'") == "2" && scalar("SELECT count(*) FROM completion_notifications") == "0", "capture invented completion/test validation")
	must(os.WriteFile(file, []byte("PRIVATE_NATIVE_PREPARE_CAPTURE\n"), 0600))
	must(os.WriteFile(filepath.Join(root, "native-supersedes.txt"), []byte(observed.Association.ID), 0600))
	must(json.NewEncoder(os.Stdout).Encode(map[string]any{"result": "PASS", "project": seed.Project, "job": seed.Job, "repo": seed.Repo, "state_dir": seed.State, "native_supersedes": observed.Association.ID, "capture_id": id, "manifest_sha256": prepared.ManifestDigest, "checks": "real cooperative capture; durable once-only preparation; exact authenticated retained preview; no publication before confirmation; wrong lane/context refusal; machine import refused; post-prepare drift preserves exact bytes; one attempt; lost-ack fresh-process exact machine recovery; atomic lane/mail; queued/not_run; unchanged CLI idempotency; fresh worktree drift"}))
}
