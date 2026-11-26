package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// Official 2.3.0/2.3.2 Service.discover contract, NOT CLI service configuration:
// packages/client/src/{effect/service.ts,service-probe.ts}. Never marshal this
// private input or include it in errors, journals, plugin options or argv.
// 2.3.2 source: anomalyco/opencode@c0ad18b402eb2827fc1ee71c171b1177b3fc02d1;
// these files are byte-identical to the validated 2.3.0 release.
type nativeRegistration struct {
	ID       string `json:"id"`
	Version  string `json:"version"`
	URL      string `json:"url"`
	PID      int    `json:"pid"`
	Password string `json:"password"`
}

func readNativeRegistration(ctx context.Context, directory string) (nativeRegistration, error) {
	var reg nativeRegistration
	c := exec.CommandContext(ctx, "opencode", "debug", "paths", "state")
	c.Dir = directory
	c.WaitDelay = handoffWaitDelay
	c.Stderr = io.Discard
	var out handoffOutput
	c.Stdout = &out
	if c.Run() != nil {
		return reg, errors.New("installed read-only OpenCode state path unavailable")
	}
	state := strings.TrimSpace(out.String())
	return readNativeRegistrationAt(state)
}

// The normal TUI integration is explicitly configured with its own state
// directory, so resolving an Open target needs no invocation of the OpenCode CLI.
func readNativeRegistrationAt(state string) (nativeRegistration, error) {
	var reg nativeRegistration
	if !filepath.IsAbs(state) || strings.ContainsAny(state, "\r\n\x00") {
		return reg, errors.New("invalid OpenCode state path")
	}
	// Use only the known managed registration filename and verify its shape.
	// No legacy/channel scan, config/password getter, migration or ensure/start.
	fd, err := syscall.Open(state, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return reg, errors.New("trusted OpenCode registration directory unavailable")
	}
	dir := os.NewFile(uintptr(fd), state)
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil {
		return reg, errors.New("registration directory stat unavailable")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0002 != 0 || info.Mode().Perm()&0020 != 0 && st.Gid != uint32(os.Getgid()) {
		return reg, errors.New("registration directory must be same-user, not world writable, with only the owning primary group writable")
	}
	file, err := syscall.Openat(fd, "service.json", syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return reg, errors.New("official OpenCode service registration unavailable")
	}
	f := os.NewFile(uintptr(file), "service.json")
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return reg, errors.New("registration stat unavailable")
	}
	st, ok = info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || st.Uid != uint32(os.Getuid()) {
		return reg, errors.New("registration must be same-user regular private0600")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 16<<10+1))
	defer clear(raw)
	if err != nil || len(raw) > 16<<10 || json.Unmarshal(raw, &reg) != nil {
		return nativeRegistration{}, errors.New("official registration invalid/bounded")
	}
	u, err := url.Parse(reg.URL)
	if err != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.Port() == "" || strings.ContainsAny(reg.URL, "\r\n\x00") || !isOpenCodeV2(reg.Version) || reg.PID <= 0 || reg.ID == "" {
		return nativeRegistration{}, errors.New("exact managed OpenCode V2 registration identity/origin unavailable")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nativeRegistration{}, errors.New("managed authentication handoff requires literal loopback origin")
	}
	return reg, nil
}

// Choose credentials ONCE before transport; an explicit inherited choice is
// never retried with registration auth. Registration must still match the exact
// discovered service instance even when inherited authentication is selected.
func nativePassword(ctx context.Context, directory string, s nativeService) (string, error) {
	reg, err := readNativeRegistration(ctx, directory)
	if err != nil {
		return "", err
	}
	if reg.URL != s.Origin || reg.PID != s.PID || reg.ID != s.RegistrationID {
		return "", errors.New("managed service registration changed; no auth fallback or request")
	}
	if value, present := os.LookupEnv("OPENCODE_PASSWORD"); present {
		return value, nil
	}
	if value, present := os.LookupEnv("OPENCODE_SERVER_PASSWORD"); present {
		return value, nil
	}
	if reg.Password == "" {
		return "", errors.New("managed service has no supported private authentication handoff")
	}
	return reg.Password, nil
}

// Scoped child environment only. Explicit --server remains mandatory, so the
// full TUI does not capture terminal ENV into the target session.
func nativeCommandEnvironment(password string) []string {
	env := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "OPENCODE_PASSWORD=") && !strings.HasPrefix(entry, "OPENCODE_SERVER_PASSWORD=") {
			env = append(env, entry)
		}
	}
	return append(env, "OPENCODE_PASSWORD="+password)
}
