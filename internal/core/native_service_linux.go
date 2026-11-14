package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

type nativeService struct {
	Origin   string `json:"origin"`
	Identity string `json:"identity"`
}

// service status is read-only discover, unlike api's automatic ensure/start.
// Explicit --server uses only the inherited intended authentication environment.
func discoverNativeService(directory string) (nativeService, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "opencode", "service", "status")
	c.Dir = directory
	c.WaitDelay = handoffWaitDelay
	var out handoffOutput
	c.Stdout = &out
	c.Stderr = io.Discard
	if err := c.Run(); err != nil {
		return nativeService{}, errors.New("OpenCode service discovery unavailable; no start/restart attempted")
	}
	origin := strings.TrimSpace(out.String())
	u, err := url.Parse(origin)
	if err != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || strings.ContainsAny(origin, "\r\n\x00") {
		return nativeService{}, errors.New("exact credential-free existing service origin unavailable")
	}
	body, err := nativeCall(directory, nativeService{Origin: origin}, "get", "/api/info", nil)
	if err != nil {
		return nativeService{}, err
	}
	var info map[string]any
	if json.Unmarshal(body, &info) != nil || info["version"] != "2.3.0" || info["pid"] == nil || info["hostname"] == nil {
		return nativeService{}, errors.New("native client requires source-validated OpenCode server 2.3.0 identity")
	}
	identity, _ := json.Marshal(map[string]any{"origin": origin, "version": info["version"], "pid": info["pid"], "hostname": info["hostname"]})
	return nativeService{origin, sha256Hex(identity)}, nil
}

func nativeCall(directory string, service nativeService, method, path string, body any) ([]byte, error) {
	return nativeCallContext(context.Background(), directory, service, method, path, body)
}

func nativeCallContext(parent context.Context, directory string, service nativeService, method, path string, body any) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	args := []string{"--server", service.Origin, "api", method, path}
	if body != nil {
		return nativePrivateBody(ctx, service, method, path, body)
	}
	c := exec.CommandContext(ctx, "opencode", args...)
	c.Dir = directory
	c.WaitDelay = handoffWaitDelay
	var out handoffOutput
	c.Stdout = &out
	c.Stderr = io.Discard
	if err := c.Run(); err != nil {
		return nil, errors.New("exact OpenCode API request unavailable/uncertain; never retry an attempted POST")
	}
	if !json.Valid(out.Bytes()) {
		return nil, errors.New("OpenCode acknowledgement is not valid JSON")
	}
	return append([]byte(nil), out.Bytes()...), nil
}

// Official 2.3.0 api --data has no stdin/file form. Main inputs therefore use
// this bounded no-redirect, fresh-connection transport, AFTER the explicit CLI
// GET verified the discovered origin/auth. Credentials exactly mirror
// packages/cli/src/{env.ts,services/server-connection.ts}: inherited password
// (legacy name second), Basic username opencode. Never guessed auth or argv data.
func nativePrivateBody(ctx context.Context, service nativeService, method, path string, body any) ([]byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	if len(b) > WorkRequestBytes || !strings.HasPrefix(path, "/") {
		return nil, errors.New("private Main API body/path exceeds contract")
	}
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), service.Origin+path, bytes.NewReader(b))
	if err != nil {
		return nil, errors.New("private Main request unavailable")
	}
	req.Header.Set("Content-Type", "application/json")
	password, present := os.LookupEnv("OPENCODE_PASSWORD")
	if !present {
		password, present = os.LookupEnv("OPENCODE_SERVER_PASSWORD")
	}
	if present {
		req.SetBasicAuth("opencode", password)
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DisableKeepAlives: true, MaxConnsPerHost: 1, ResponseHeaderTimeout: 10 * time.Second}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("private Main API acknowledgement uncertain; never repeat the attempted mutation")
	}
	defer response.Body.Close()
	out, err := io.ReadAll(io.LimitReader(response.Body, 256<<10+1))
	if err == nil && response.StatusCode == http.StatusNoContent && len(out) == 0 {
		return []byte("null"), nil
	}
	if err != nil || len(out) > 256<<10 || response.StatusCode < 200 || response.StatusCode >= 300 || !json.Valid(out) {
		return nil, errors.New("private Main API acknowledgement unavailable/uncertain; inspect exact ID only")
	}
	return out, nil
}

func nativeSession(directory string, service nativeService, id string) (map[string]any, error) {
	if !strings.HasPrefix(id, "ses_") || strings.ContainsAny(id, "/?#\\ \t\r\n\x00") {
		return nil, errors.New("invalid exact session ID")
	}
	b, err := nativeCall(directory, service, "get", "/api/session/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	var session map[string]any
	if err = decodeData(b, &session); err != nil {
		return nil, err
	}
	if session == nil {
		return nil, errors.New("exact session missing")
	}
	return session, nil
}

func nativeHash(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return sha256Hex(b)
}
func nativeProfile(s map[string]any) string {
	return nativeHash(map[string]any{"agent": s["agent"], "model": s["model"], "permissions": s["permissions"]})
}

func nativeIdentity(s map[string]any, id, directory, project, ownership string) error {
	loc, _ := s["location"].(map[string]any)
	if s["id"] != id || s["parentID"] != nil && s["parentID"] != "" || loc["directory"] != directory || s["projectID"] != project || nativeHash(s["metadata"]) != ownership {
		return errors.New("exact session identity/location/project/ownership mismatch; never replace")
	}
	return nil
}
