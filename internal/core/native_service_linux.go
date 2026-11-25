package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

type nativeService struct {
	Origin         string `json:"origin"`
	Identity       string `json:"identity"`
	PID            int    `json:"pid"`
	RegistrationID string `json:"registration_id"`
}

// Same-user read-only Service.discover registration contract; never ensure/start,
// CLI service configuration or a credential-bearing persistent nativeService.
func discoverNativeService(directory string) (nativeService, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	reg, err := readNativeRegistration(ctx, directory)
	if err != nil {
		return nativeService{}, err
	}
	service := nativeService{Origin: reg.URL, PID: reg.PID, RegistrationID: reg.ID}
	body, err := nativeCallContext(ctx, directory, service, "get", "/api/info", nil)
	if err != nil {
		return nativeService{}, err
	}
	var info map[string]any
	if json.Unmarshal(body, &info) != nil || info["version"] != reg.Version || info["pid"] != float64(reg.PID) || info["hostname"] == nil {
		return nativeService{}, errors.New("native client requires matching source-validated OpenCode server 2.3.0 or 2.3.2 identity")
	}
	identity, _ := json.Marshal(map[string]any{"origin": reg.URL, "version": info["version"], "pid": info["pid"], "hostname": info["hostname"], "registration_id": reg.ID})
	service.Identity = sha256Hex(identity)
	return service, nil
}

func nativeCall(directory string, service nativeService, method, path string, body any) ([]byte, error) {
	return nativeCallContext(context.Background(), directory, service, method, path, body)
}

func nativeCallContext(parent context.Context, directory string, service nativeService, method, path string, body any) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	password, err := nativePassword(ctx, directory, service)
	if err != nil {
		return nil, err
	}
	args := []string{"--server", service.Origin, "api", method, path}
	if body != nil {
		return nativePrivateBody(ctx, service, method, path, body, password)
	}
	c := exec.CommandContext(ctx, "opencode", args...)
	c.Dir = directory
	c.Env = nativeCommandEnvironment(password)
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

// Official 2.3.0/2.3.2 api --data has no stdin/file form. Main inputs therefore use
// this bounded no-redirect, fresh-connection transport, AFTER the explicit CLI
// GET verified the discovered origin/auth. Auth is selected from the exact
// official managed registration or explicit inherited choice before transport.
func nativePrivateBody(ctx context.Context, service nativeService, method, path string, body any, password string) ([]byte, error) {
	return nativePrivateBodyLimit(ctx, service, method, path, body, password, WorkRequestBytes, 256<<10)
}

// Larger project-review prompts use the same source-validated discovery/auth and
// private HTTP transport as Main, not a second server or a large argv argument.
func nativeInlineReviewPrompt(directory, path string, body any) ([]byte, error) {
	service, err := discoverNativeService(directory)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	password, err := nativePassword(ctx, directory, service)
	if err != nil {
		return nil, err
	}
	return nativePrivateBodyLimit(ctx, service, "post", path+locationQuery(directory), body, password, projectPayloadBytes, projectPayloadBytes)
}

func nativePrivateBodyLimit(ctx context.Context, service nativeService, method, path string, body any, password string, limit, responseLimit int) ([]byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	if len(b) > limit || !strings.HasPrefix(path, "/") {
		return nil, errors.New("private Main API body/path exceeds contract")
	}
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), service.Origin+path, bytes.NewReader(b))
	if err != nil {
		return nil, errors.New("private Main request unavailable")
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("opencode", password)
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DisableKeepAlives: true, MaxConnsPerHost: 1, ResponseHeaderTimeout: 10 * time.Second}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("private Main API acknowledgement uncertain; never repeat the attempted mutation")
	}
	defer response.Body.Close()
	out, err := io.ReadAll(io.LimitReader(response.Body, int64(responseLimit)+1))
	if err == nil && response.StatusCode == http.StatusNoContent && len(out) == 0 {
		return []byte("null"), nil
	}
	if err != nil || len(out) > responseLimit || response.StatusCode < 200 || response.StatusCode >= 300 || !json.Valid(out) {
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
