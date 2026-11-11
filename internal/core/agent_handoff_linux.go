package core

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// AgentHandoff is navigation information, never an execution authorization or
// request acknowledgement. No request body, owner capability or endpoint crosses
// this boundary. The recorded path is an intentional inspect destination.
type AgentHandoff struct {
	Target          AgentReconcileTarget `json:"target"`
	Service         string               `json:"service_label"`
	ServiceIdentity string               `json:"service_identity,omitempty"`
	Observation     string               `json:"observation"`
	Unavailable     string               `json:"open_unavailable_reason"`
}

const handoffNoAttach = "Automatic open unavailable: installed OpenCode 2.2.6 --session may create an absent session; no existing-session-only, exact-service isolated navigation route is validated. Copy the recorded location/session and select it in the actual OpenCode interface."

// One request deadline covers both commands; inherited pipes may add at most
// this drain allowance, even after the direct CLI process has exited.
const handoffWaitDelay = 250 * time.Millisecond

// Deliberate opt-in, not shared-service discovery. Inherited CLI authentication
// remains untouched. Only an explicit credential-free loopback origin is allowed
// in this first local slice. Nothing ever falls back to a discovered service.
func handoffOrigin() (string, string) {
	s := os.Getenv("OCQ_HANDOFF_SERVER_URL")
	u, e := url.Parse(s)
	if e != nil || u.Scheme != "http" || u.User != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") || u.Port() == "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.Path != "" || u.RawPath != "" || strings.ContainsAny(s, "\r\n\x00") {
		return "", ""
	}
	return s, sha256Hex([]byte(s))
}

// A capped writer prevents API output or diagnostics from becoming an unbounded
// bridge payload. Errors are mapped to fixed safe copy; raw stderr is discarded.
// Do not embed Buffer: its promoted ReadFrom would let exec's io.Copy bypass
// Write and its limit.
type handoffOutput struct{ data bytes.Buffer }

func (b *handoffOutput) Bytes() []byte  { return b.data.Bytes() }
func (b *handoffOutput) String() string { return b.data.String() }

func (b *handoffOutput) Write(p []byte) (int, error) {
	if b.data.Len()+len(p) > 256<<10 {
		return 0, errors.New("handoff API output exceeds bound")
	}
	return b.data.Write(p)
}
func handoffGet(ctx context.Context, origin, directory, path string, out any) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	c := exec.CommandContext(ctx, "opencode", "--server", origin, "api", "get", path)
	c.WaitDelay = handoffWaitDelay
	c.Dir = directory
	var b handoffOutput
	c.Stdout, c.Stderr = &b, io.Discard
	if e := c.Run(); e != nil {
		return e
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	return decodeData(b.Bytes(), out)
}

// Stored reads do no API lookup. Verification is explicitly requested by the
// human and consists solely of bounded GETs against the same configured origin.
// Neither path initializes/migrates SQLite, writes evidence, or changes jobs.
func InspectAgentHandoff(dir string, t AgentReconcileTarget, verifyService string) (AgentHandoff, error) {
	out := AgentHandoff{Target: t, Service: "Not configured", Observation: "stored_only", Unavailable: handoffNoAttach}
	if !t.valid() {
		return out, errors.New("invalid exact local handoff target")
	}
	r, e := OpenReader(dir)
	if e != nil {
		return out, e
	}
	defer r.Close()
	before, e := agentReconcileSnapshot(r.store, t)
	if e != nil {
		return out, e
	}
	origin, identity := handoffOrigin()
	if identity != "" {
		out.Service, out.ServiceIdentity = "Configured local OpenCode", identity
	}
	if verifyService == "" {
		return out, nil
	}
	if identity == "" || verifyService != identity {
		out.Observation = "service_unavailable"
		return out, nil
	}
	j, e := r.store.get(t.Job)
	if e != nil {
		return out, e
	}
	if j.Agent == nil || j.agentRequest == nil || j.Agent.SessionID != t.Session || j.Agent.Worktree != t.Worktree {
		return out, errors.New("exact handoff ownership changed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// The existing CLI --server route preserves the configured authentication
	// environment; there is no pairing, service start, config reload or discovery.
	// /api/info is not a data-envelope route in installed 2.2.6. Session GET
	// itself supplies the identity/location/ownership fence; version is inspected
	// with the installed CLI, without connecting or starting a service.
	version := exec.CommandContext(ctx, "opencode", "--version")
	version.WaitDelay = handoffWaitDelay
	var v handoffOutput
	version.Stdout, version.Stderr = &v, io.Discard
	if version.Run() != nil || ctx.Err() != nil || strings.TrimSpace(v.String()) != "opencode v2.2.6" {
		out.Observation = "unsupported_client"
		return out, nil
	}
	var session sessionInfo
	if handoffGet(ctx, origin, t.Worktree, "/api/session/"+url.PathEscape(t.Session), &session) != nil {
		out.Observation = "session_unavailable"
	} else if validateSession(j, session) != nil {
		out.Observation = "session_mismatch"
	} else {
		out.Observation = "verified_snapshot"
	}
	after, e := agentReconcileSnapshot(r.store, t)
	if e != nil || after.Digest != before.Digest {
		return out, errors.New("stored handoff target changed during verification")
	}
	return out, nil
}
