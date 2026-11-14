package core

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type nativeImages struct{ Launcher, LauncherSHA, OpenCode, OpenCodeSHA, Asset, AssetSHA string }

type NativeClientPackage struct {
	Launcher       string `json:"launcher"`
	LauncherSHA256 string `json:"launcher_sha256"`
	OpenCode       string `json:"opencode"`
	OpenCodeSHA256 string `json:"opencode_sha256"`
	Asset          string `json:"asset"`
	AssetSHA256    string `json:"asset_sha256"`
}

// Read-only packaging inspection; no service/API, client target or TUI launch.
func InspectNativeClientPackage() (NativeClientPackage, error) {
	images, err := nativeReleaseImages()
	return NativeClientPackage{images.Launcher, images.LauncherSHA, images.OpenCode, images.OpenCodeSHA, images.Asset, images.AssetSHA}, err
}

func trustedNativeFile(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || (st.Uid != 0 && st.Uid != uint32(os.Getuid())) {
		return nil, errors.New("native release asset must be trusted, regular and not group/world writable")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(b)) > limit {
		return nil, errors.New("native asset exceeds bound")
	}
	return b, err
}

func nativeReleaseImages() (nativeImages, error) {
	var out nativeImages
	exe, err := os.Executable()
	if err != nil {
		return out, err
	}
	out.Launcher, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return out, err
	}
	switch filepath.Base(out.Launcher) {
	case "ocq":
	case "ocq-herdr":
		dir := filepath.Dir(out.Launcher)
		out.Launcher, err = filepath.EvalSymlinks(filepath.Join(dir, "ocq"))
		if err != nil || filepath.Dir(out.Launcher) != dir || filepath.Base(out.Launcher) != "ocq" {
			return out, errors.New("matched release CLI sibling unavailable; never use bridge as CLI")
		}
	default:
		return out, errors.New("native client requires a packaged ocq or ocq-herdr executable")
	}
	_, _, out.LauncherSHA, err = dispatcherImage(out.Launcher)
	if err != nil {
		return out, err
	}
	root := filepath.Dir(out.Launcher)
	if filepath.Base(root) == "bin" {
		root = filepath.Dir(root)
	}
	out.Asset = filepath.Join(root, "share", "opencode", "ocq-handoff")
	info, err := os.Lstat(out.Asset)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return out, errors.New("packaged release-relative handoff asset unavailable; no checkout-path fallback")
	}
	canonical, err := filepath.EvalSymlinks(out.Asset)
	if err != nil || canonical != out.Asset {
		return out, errors.New("native handoff asset path must not traverse symlinks")
	}
	// A package manifest could load other entrypoints/dependencies. This release
	// contract is deliberately exactly one trusted tui.js, no JS project.
	entries, err := os.ReadDir(out.Asset)
	if err != nil || len(entries) != 1 || entries[0].Name() != "tui.js" {
		return out, errors.New("handoff release must contain exactly tui.js")
	}
	b, err := trustedNativeFile(filepath.Join(out.Asset, "tui.js"), 64<<10)
	if err != nil {
		return out, err
	}
	out.AssetSHA = sha256Hex(b)
	out.OpenCode, err = exec.LookPath("opencode")
	if err != nil {
		return out, err
	}
	out.OpenCode, err = filepath.EvalSymlinks(out.OpenCode)
	if err != nil {
		return out, err
	}
	_, _, out.OpenCodeSHA, err = dispatcherImage(out.OpenCode)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, out.OpenCode, "--version")
	c.WaitDelay = handoffWaitDelay
	var version handoffOutput
	c.Stdout = &version
	c.Stderr = io.Discard
	if c.Run() != nil || strings.TrimSpace(version.String()) != "opencode v2.3.0" {
		return out, errors.New("native client requires exact source-validated OpenCode 2.3.0")
	}
	return out, nil
}

func nativeConfigDir() (string, error) {
	if configured := os.Getenv("OPENCODE_CONFIG_DIR"); configured != "" {
		if !filepath.IsAbs(configured) {
			return "", errors.New("explicit OpenCode config directory must be absolute for safe merge")
		}
		return configured, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "opencode"), nil
}
