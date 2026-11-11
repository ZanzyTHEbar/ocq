// One private real SQLite/bridge publication flow, with a subsequent imported
// review. Seeded ownership is explicit; no agent/session/service/dispatch runs.
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
func hash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func random() string {
	b := make([]byte, 32)
	_, e := rand.Read(b)
	must(e)
	return hex.EncodeToString(b)
}
func proof(cap, nonce, role string) string {
	k, e := hex.DecodeString(cap)
	must(e)
	h := hmac.New(sha256.New, k)
	h.Write([]byte("ocq-compose-v1:" + role + ":" + nonce))
	return hex.EncodeToString(h.Sum(nil))
}

const privateText = "PRIVATE_MANIFEST_SENTINEL opérateur\u202e\u200b\n\x01"

func main() {
	check(len(os.Args) == 4, "usage: herdr-result-publication BRIDGE FRESH_PRIVATE_ROOT SOURCE_REPO")
	bridge, e := filepath.Abs(os.Args[1])
	must(e)
	root, e := filepath.Abs(os.Args[2])
	must(e)
	source, e := filepath.Abs(os.Args[3])
	must(e)
	must(os.Mkdir(root, 0700))
	state := filepath.Join(root, "queue")
	repo := filepath.Join(root, "repo")
	c := exec.Command("git", "-c", "core.hooksPath=/dev/null", "clone", "--no-hardlinks", source, repo)
	must(c.Run())
	baseBytes, e := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	must(e)
	base := strings.TrimSpace(string(baseBytes))
	q, e := core.OpenQueue(state)
	must(e)
	permissions := []byte(`[{"action":"*","resource":"*","effect":"deny"}]`)
	job, e := q.SubmitAgent(core.AgentSubmission{Key: "private-never-sent", Repo: repo, Base: base, Agent: "fixture", Model: "fixture/never-executed", Prompt: []byte("PRIVATE_REQUEST_SENTINEL"), Permissions: permissions})
	must(e)
	reviewer, e := q.SubmitAgent(core.AgentSubmission{Key: "private-review-never-sent", Repo: repo, Base: base, Agent: "fixture-review", Model: "fixture/never-executed", Prompt: []byte("PRIVATE_REVIEW_REQUEST_SENTINEL"), Permissions: permissions})
	must(e)
	const project = "proj-publication-fixture"
	ts := "2026-11-11T00:00:00Z"
	handoff := encode(map[string]any{"version": 1, "kind": "handoff", "project_id": project, "source": "operator_import", "observed_at": nil, "intent": "Private import fixture", "decisions": []string{}, "risks": []string{}})
	brief := encode(map[string]any{"version": 1, "kind": "brief", "project_id": project, "source": "operator_import", "observed_at": nil, "goal": "Import unverified claims", "scope": []string{"publication"}, "exclusions": []string{}, "acceptance_criteria": []string{"exact receipt"}, "base": base, "agent": "fixture", "model": map[string]string{"id": "never-executed", "providerID": "fixture"}, "permissions": json.RawMessage(permissions), "allowed_actions": []string{"read"}, "context_references": []string{}})
	manifest := core.ResultManifest{Version: 1, Kind: "result", ProjectID: project, Source: "operator_import", JobID: job.ID, RequestDigest: job.Digest, Base: base, Worktree: repo, StagedStatus: "clean", UnstagedStatus: "clean", UntrackedPaths: []string{}, IgnoredExclusions: []string{}, ChangedFiles: []core.ManifestChangedFile{}, BeforeStatusDigest: "not_recorded", AfterStatusDigest: "not_recorded", BeforeContentDigest: "not_recorded", AfterContentDigest: "not_recorded", InlineOutput: privateText, ClaimedValidation: core.ClaimedValidation{Status: "claimed_pass", Commands: []string{"PRIVATE_COMMAND_NOT_RUN"}, Results: privateText, Provenance: "operator_assertion", Limitations: "Unverified imported assertion; no test execution"}}
	raw := encode(manifest)
	digest := hash(raw)
	db, e := sql.Open("sqlite", filepath.Join(state, "queue.db"))
	must(e)
	defer db.Close()
	tx, e := db.Begin()
	must(e)
	write := func(query string, args ...any) { _, e := tx.Exec(query, args...); must(e) }
	write("INSERT INTO projects(id,label,created_at) VALUES(?,?,?)", project, "Imported result publication fixture", ts)
	for _, m := range []struct {
		kind string
		b    []byte
	}{{"handoff", handoff}, {"brief", brief}} {
		write("INSERT INTO project_manifests(digest,project_id,version,kind,canonical,recorded_at) VALUES(?,?,1,?,?,?)", hash(m.b), project, m.kind, m.b, ts)
	}
	write("INSERT INTO project_bindings(project_id,revision,repo_root,git_common_dir,commit_anchor,elephant_session_id,opencode_project_id,opencode_directory,handoff_manifest_digest,auth_ref,verified_at,recorded_at) VALUES(?,1,?,?,?,?,?,?,?,?,?,?)", project, repo, filepath.Join(repo, ".git"), base, "ses_fixture_not_created", "fixture-not-created", repo, hash(handoff), "PRIVATE_AUTH_SENTINEL", ts, ts)
	write("INSERT INTO project_jobs(job_id,project_id,binding_revision,role,brief_digest,recorded_at) VALUES(?,?,1,'implementation',?,?)", job.ID, project, hash(brief), ts)
	write("INSERT INTO project_jobs(job_id,project_id,binding_revision,role,brief_digest,target_job_id,target_result_digest,recorded_at) VALUES(?,?,1,'review',?,?,?,?)", reviewer.ID, project, hash(brief), job.ID, digest, ts)
	for _, j := range []core.Job{job, reviewer} {
		write("UPDATE agent_ownership SET worktree=?,phase='worktree_created',terminal_evidence='PRIVATE_EVIDENCE_SENTINEL' WHERE job_id=?", repo, j.ID)
	}
	must(tx.Commit())
	prior := manifest
	prior.InlineOutput = "Prior imported result"
	prior.ClaimedValidation.Status = "claimed_fail"
	previous, e := q.RecordResult(project, job.ID, encode(prior), "")
	must(e)
	must(q.Close())
	// Full-table snapshots bracket preparation, private review, stored-only
	// recovery and each intentional core transaction separately.
	snapshot := func() []byte {
		rows, e := db.Query("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
		must(e)
		var names []string
		for rows.Next() {
			var n string
			must(rows.Scan(&n))
			names = append(names, n)
		}
		must(rows.Err())
		must(rows.Close())
		var all []any
		for _, n := range names {
			rows, e := db.Query(`SELECT * FROM "` + strings.ReplaceAll(n, `"`, `""`) + `" ORDER BY rowid`)
			must(e)
			cols, e := rows.Columns()
			must(e)
			for rows.Next() {
				v := make([]any, len(cols))
				p := make([]any, len(cols))
				for i := range v {
					p[i] = &v[i]
				}
				must(rows.Scan(p...))
				all = append(all, []any{n, v})
			}
			must(rows.Err())
			must(rows.Close())
		}
		return encode(all)
	}
	scalar := func(query string, args ...any) string {
		var s string
		must(db.QueryRow(query, args...).Scan(&s))
		return s
	}
	private := func(b []byte) {
		for _, s := range []string{"PRIVATE_", job.Agent.Token, reviewer.Agent.Token, `"payload_base64"`, `"manifest":`, `"request":`, `"terminal_evidence":`} {
			check(!bytes.Contains(b, []byte(s)), "private payload/request leaked from standard DTO/diagnostics")
		}
	}
	call := func(args ...string) ([]byte, error) {
		c := exec.Command(bridge, args...)
		c.Env = append(os.Environ(), "OCQ_STATE_DIR="+state)
		b, e := c.Output()
		private(b)
		if x, ok := e.(*exec.ExitError); ok {
			private(x.Stderr)
		}
		return b, e
	}
	args := func(action string, t core.ResultPublicationTarget, extra ...string) []string {
		return append([]string{"result-publish-" + action, t.Space, t.Queue, t.Project, t.Job, t.Sequence}, extra...)
	}
	contextFor := func(id string) core.ResultPublicationContext {
		b, e := call("result-publish-context", "sp-publication-fixture", project, id)
		must(e)
		var c core.ResultPublicationContext
		must(json.Unmarshal(b, &c))
		return c
	}
	ctx := contextFor(job.ID)
	p := core.ResultPublicationPayload{Target: ctx.Target, OwnershipDigest: ctx.OwnershipDigest, Manifest: string(raw), Supersedes: previous.ID}
	payload := encode(p)
	payloadDigest := hash(payload)
	exchange := func(mode string, p core.ResultPublicationPayload, payload []byte) (core.ResultPublicationStatus, []byte, error) {
		launch := random()
		digest := hash([]byte(p.Manifest))
		a := args(mode, p.Target, launch)
		if mode == "preview" {
			a = args(mode, p.Target, digest, hash(payload), launch)
		}
		c := exec.Command(bridge, a...)
		c.Env = append(os.Environ(), "OCQ_STATE_DIR="+state)
		var out, errout bytes.Buffer
		c.Stdout = &out
		c.Stderr = &errout
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
			entry, e = core.ResultPublicationRendezvous(state, p.Target, launch)
			if e == nil {
				break
			}
			check(time.Now().Before(deadline), "publication rendezvous missing")
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
		check(line == proof(entry.Capability, nonce, "server")+"\n", "server auth failed")
		_, e = fmt.Fprintln(conn, proof(entry.Capability, nonce, "client"))
		must(e)
		line, e = reader.ReadString('\n')
		must(e)
		check(line == "READY\n", "client auth failed")
		var exact []byte
		if mode == "receive" {
			_, e = fmt.Fprintf(conn, "%d\n", len(payload))
			must(e)
			_, e = conn.Write(payload)
			must(e)
			_, _ = reader.ReadString('\n')
		} else {
			var h [4]byte
			_, e = io.ReadFull(reader, h[:])
			must(e)
			n := binary.BigEndian.Uint32(h[:])
			check(n > 0 && n <= 131072, "preview frame invalid")
			exact = make([]byte, n)
			_, e = io.ReadFull(reader, exact)
			must(e)
		}
		must(conn.Close())
		err := c.Wait()
		private(out.Bytes())
		private(errout.Bytes())
		var status core.ResultPublicationStatus
		if err == nil {
			must(json.Unmarshal(out.Bytes(), &status))
		}
		return status, exact, err
	}
	read := func(t core.ResultPublicationTarget, d string) core.ResultPublicationStatus {
		b, e := call(args("status", t, d)...)
		must(e)
		var s core.ResultPublicationStatus
		must(json.Unmarshal(b, &s))
		return s
	}
	before := snapshot()
	check(read(ctx.Target, digest).State == "none", "inspection created publication")
	prepared, _, e := exchange("receive", p, payload)
	must(e)
	check(prepared.State == "prepared" && prepared.PayloadDigest == payloadDigest && prepared.Kind == "result" && prepared.Supersedes == previous.ID, "retained intent mismatch")
	wrong := ctx.Target
	wrong.Space = "sp-another-copy"
	check(read(wrong, digest).State == "blocked", "wrong context adopted intent")
	_, e = call(args("arm", wrong, digest, payloadDigest)...)
	check(e != nil, "wrong context armed publication")
	reviewed, exact, e := exchange("preview", p, payload)
	must(e)
	check(reviewed.State == "prepared" && bytes.Equal(exact, payload), "authenticated retained preview changed bytes")
	_, e = call(args("arm", ctx.Target, digest, payloadDigest)...)
	must(e)
	check(read(ctx.Target, digest).State == "armed", "fresh process lost claim or dispatched it")
	check(bytes.Equal(before, snapshot()), "preparation/review/arm mutated SQLite")
	lost := exec.Command("/bin/sh", append([]string{"-c", `"$@" >/dev/null || exit $?; exit 73`, "lost-publication-response", bridge}, args("attempt", ctx.Target, digest, payloadDigest)...)...)
	lost.Env = append(os.Environ(), "OCQ_STATE_DIR="+state)
	e = lost.Run()
	check(e != nil && lost.ProcessState.ExitCode() == 73, "lost response wrapper failed")
	observed := read(ctx.Target, digest)
	check(observed.State == "recorded_observed" && observed.Association != nil && observed.Association.ManifestDigest == digest && observed.Association.Supersedes != nil && *observed.Association.Supersedes == previous.ID && observed.Association.Verification == "structure_hash_linkage_only", "exact association/content/lineage not observed")
	check(scalar("SELECT count(*) FROM project_result_associations WHERE job_id=?", job.ID) == "2" && scalar("SELECT count(*) FROM project_inbox_messages WHERE job_id=?", job.ID) == "2" && scalar("SELECT sealed_manifest_digest FROM project_lanes WHERE owner_job_id=?", job.ID) == digest, "association/lane/submitted mail not atomic")
	check(scalar("SELECT count(*) FROM project_inbox_messages WHERE job_id=? AND manifest_digest=? AND assoc_id=? AND kind='work.submitted' AND read_at IS NULL AND archived_at IS NULL", job.ID, digest, observed.Association.ID) == "1", "exact automatic mail missing")
	var stored []byte
	must(db.QueryRow("SELECT canonical FROM project_manifests WHERE digest=?", digest).Scan(&stored))
	check(bytes.Equal(stored, raw), "manifest bytes changed")
	after := snapshot()
	_, e = call(args("attempt", ctx.Target, digest, payloadDigest)...)
	check(e != nil, "duplicate bridge attempt allowed")
	for n := 0; n < 2; n++ {
		check(read(ctx.Target, digest).State == "recorded_observed", "reload/cross-copy read-only recovery lost exact receipt")
	}
	_, e = call(args("list", ctx.Target)...)
	must(e)
	check(bytes.Equal(after, snapshot()), "read-only recovery or refused duplicate changed SQLite")
	w, e := core.OpenExistingResultPublisher(state)
	must(e)
	retry, e := w.Publish(p)
	must(e)
	check(retry.ID == observed.Association.ID, "core exact retry not idempotent")
	conflict := p
	conflict.Supersedes = ""
	_, e = w.Publish(conflict)
	check(e != nil, "core conflicting lineage accepted")
	must(w.Close())
	check(bytes.Equal(after, snapshot()), "exact retry or rejected conflict changed stored result")
	// Real review_result import uses the same writer/core validators, with the
	// exact seeded review session and immutable target result linkage.
	rc := contextFor(reviewer.ID)
	reviewRaw := encode(map[string]any{"version": 1, "kind": "review_result", "project_id": project, "source": "author_import", "observed_at": nil, "review_job_id": reviewer.ID, "review_session_id": reviewer.Agent.SessionID, "target_job_id": job.ID, "reviewed_result_digest": digest, "verdict": "needs_revision", "findings": []map[string]string{{"location": "result.txt", "severity": "low", "message": privateText}}, "limitations": privateText})
	rp := core.ResultPublicationPayload{Target: rc.Target, OwnershipDigest: rc.OwnershipDigest, Manifest: string(reviewRaw)}
	rpayload := encode(rp)
	rd := hash(reviewRaw)
	rpd := hash(rpayload)
	_, _, e = exchange("receive", rp, rpayload)
	must(e)
	_, exact, e = exchange("preview", rp, rpayload)
	must(e)
	check(bytes.Equal(exact, rpayload), "review_result private fields lost")
	_, e = call(args("arm", rc.Target, rd, rpd)...)
	must(e)
	_, e = call(args("attempt", rc.Target, rd, rpd)...)
	must(e)
	check(read(rc.Target, rd).State == "recorded_observed", "review_result not published")
	check(scalar("SELECT count(*) FROM project_inbox_messages WHERE job_id=? AND kind='review.submitted' AND manifest_digest=?", reviewer.ID, rd) == "1", "review submitted mail missing")
	for _, a := range [][]string{{"job", project, job.ID}, {"board", project}, {"queue-job", job.ID}, {"result-publish-list", ctx.Target.Space, ctx.Target.Queue, project, job.ID, ctx.Target.Sequence}} {
		_, e = call(a...)
		must(e)
	}
	check(scalar("SELECT count(*) FROM jobs WHERE state='queued' AND validation='not_run'") == "2" && scalar("SELECT count(*) FROM completion_notifications") == "0", "import fabricated agent/test completion or released capacity")
	missing := filepath.Join(root, "missing")
	_, e = core.OpenExistingResultPublisher(missing)
	check(e != nil, "writer initialized missing queue")
	_, e = os.Stat(missing)
	check(os.IsNotExist(e), "missing queue created")
	// Retain one fresh different manifest for native input/review/publication on
	// this same fixture; no second agent/service fixture is needed.
	manifest.InlineOutput = privateText + " native retained review"
	nativeRaw := encode(manifest)
	must(os.WriteFile(filepath.Join(root, "native-manifest.json"), nativeRaw, 0600))
	must(os.WriteFile(filepath.Join(root, "native-supersedes.txt"), []byte(observed.Association.ID), 0600))
	must(json.NewEncoder(os.Stdout).Encode(map[string]any{"result": "PASS", "state_dir": state, "repo": repo, "project": project, "job": job.ID, "review_job": reviewer.ID, "manifest_sha256": digest, "association_id": observed.Association.ID, "supersedes_id": previous.ID, "native_manifest": filepath.Join(root, "native-manifest.json"), "native_manifest_sha256": hash(nativeRaw), "native_supersedes": observed.Association.ID, "checks": "real core imported result + review_result; mutual-auth exact retained preview; stored-only prepare/arm; wrong context refused; lost response/fresh-process exact association+bytes+lineage recovery; one bridge attempt; core exact retry idempotent/conflicting lineage refused; atomic lane sealing/submitted mail; safe standard DTOs; jobs remain queued/validation not_run; no completion or test success; noninitializing writer"}))
}
