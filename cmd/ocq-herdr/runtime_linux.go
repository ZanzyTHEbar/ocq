package main

// Machine-local runtime discovery is configuration only. Herdr supplies execution
// routing; OCQ does not start a runtime or manufacture a network route here.
import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"ocq/internal/core"
)

const runtimeProtocol = "ocq-runtime-v1"

var errRuntimeAbsent = errors.New("runtime registration absent")
var errRuntimeUnsupported = errors.New("unsupported runtime registration")

type runtimeRegistration struct {
	Protocol  string `json:"protocol"`
	Core      string `json:"core_id"`
	Queue     string `json:"queue_identity"`
	Endpoint  string `json:"endpoint"`
	LocalOnly bool   `json:"local_only"`
	Token     string `json:"token"` // trusted exec/bootstrap channel only
	PID       int32  `json:"pid"`
	Start     string `json:"start_ticks"`
	Boot      string `json:"boot_id"`
	Image     string `json:"image_sha256"`
	Space     string `json:"space"`
	Folder    string `json:"folder"`
}

func runtimeFile() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ocq", "runtime.json"), nil
}

func readRuntimeRegistration() (runtimeRegistration, error) {
	var out runtimeRegistration
	name, err := runtimeFile()
	if err != nil {
		return out, err
	}
	parent, err := os.Lstat(filepath.Dir(name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, errRuntimeAbsent
		}
		return out, err
	}
	st, ok := parent.Sys().(*syscall.Stat_t)
	canonical, e := filepath.EvalSymlinks(filepath.Dir(name))
	if e != nil || canonical != filepath.Dir(name) || !ok || !parent.IsDir() || parent.Mode().Perm() != 0700 || st.Uid != uint32(os.Getuid()) {
		return out, errors.New("runtime directory unavailable")
	}
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, errRuntimeAbsent
		}
		return out, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return out, err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || owner.Uid != uint32(os.Getuid()) || info.Size() > 16384 {
		return out, errors.New("runtime registration unavailable")
	}
	d := json.NewDecoder(io.LimitReader(f, 16385))
	d.DisallowUnknownFields()
	if d.Decode(&out) != nil || d.Decode(new(any)) != io.EOF || out.Protocol != runtimeProtocol ||
		out.Core == "" || !clientDigest.MatchString(out.Queue) || !clientDigest.MatchString(out.Image) ||
		!clientDigest.MatchString(out.Token) || out.PID <= 0 {
		return out, errRuntimeUnsupported
	}
	u, err := url.Parse(out.Endpoint)
	if err != nil || len(out.Endpoint) > 2048 || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/ocq" ||
		(u.Scheme != "wss" && (u.Scheme != "ws" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "::1"))) {
		return out, errors.New("runtime endpoint unavailable")
	}
	start, err := hcStart(out.PID)
	boot, e := os.ReadFile("/proc/sys/kernel/random/boot_id")
	image, imageErr := os.ReadFile(fmt.Sprintf("/proc/%d/exe", out.PID))
	if err != nil || e != nil || imageErr != nil || start != out.Start || strings.TrimSpace(string(boot)) != out.Boot || compatHash(image) != out.Image {
		return out, errors.New("runtime owner unavailable")
	}
	return out, nil
}

func runtimeDiscoverCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "runtime-discover", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		value, err := readRuntimeRegistration()
		if err != nil {
			state := "unavailable"
			if errors.Is(err, errRuntimeAbsent) {
				state = "absent"
			} else if errors.Is(err, errRuntimeUnsupported) {
				state = "unsupported"
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"protocol": runtimeProtocol, "state": state})
		}
		// Never log/copy this stdout. Only the trusted native controller consumes it.
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"protocol": runtimeProtocol, "state": "available", "runtime": value})
	}}
	cmd.Flags().Bool("json", false, "Bounded private discovery response")
	return cmd
}

func (e *pushEndpoint) publishRuntime(endpoint string, localOnly bool) (func(), error) {
	file, err := runtimeFile()
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return nil, err
	}
	parent, err := os.Lstat(filepath.Dir(file))
	if err != nil {
		return nil, err
	}
	st, ok := parent.Sys().(*syscall.Stat_t)
	resolved, resolveErr := filepath.EvalSymlinks(filepath.Dir(file))
	if resolveErr != nil || resolved != filepath.Dir(file) || !ok || !parent.IsDir() || parent.Mode().Perm() != 0700 || st.Uid != uint32(os.Getuid()) {
		return nil, errors.New("private runtime directory required")
	}
	start, err := hcStart(int32(os.Getpid()))
	if err != nil {
		return nil, err
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, err
	}
	image, err := os.ReadFile("/proc/self/exe")
	if err != nil {
		return nil, err
	}
	binding, err := compatibilityContext(e.ctx, e.space, e.folder)
	if err != nil {
		return nil, err
	}
	value := runtimeRegistration{runtimeProtocol, e.hc.Core, binding.Queue, endpoint, localOnly, e.token, int32(os.Getpid()), start, strings.TrimSpace(string(boot)), compatHash(image), e.space, e.folder}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, errors.New("runtime registration already exists or is unavailable; never adopt/overwrite")
	}
	info, err := f.Stat()
	if err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	syncDir := func() error {
		d, err := os.Open(filepath.Dir(file))
		if err != nil {
			return err
		}
		defer d.Close()
		return d.Sync()
	}
	if err = syncDir(); err != nil {
		return nil, err
	}
	check, err := readRuntimeRegistration()
	if err != nil || check != value {
		return nil, errors.New("runtime registration read-back failed; retained, do not retry")
	}
	return func() {
		current, err := os.Lstat(file)
		bytesNow, readErr := os.ReadFile(file)
		if err == nil && readErr == nil && os.SameFile(info, current) && bytes.Equal(bytesNow, raw) {
			if os.Remove(file) == nil {
				_ = syncDir()
			}
		}
	}, nil
}

type runtimePeerKey struct{}
type runtimeProjectContext struct {
	Project string `json:"project_id"`
	Space   string `json:"space"`
	Folder  string `json:"folder"`
}

// One machine stream; explicit project actions get their own binding context.
// Discovery/observation never enrolls a repository. No caller peer is rewritten.
func (e *pushEndpoint) projectEndpoint(ctx context.Context, c runtimeProjectContext, name string, payload *string) (*pushEndpoint, error) {
	if !workSpace.MatchString(c.Space) || !filepath.IsAbs(c.Folder) || c.Folder != filepath.Clean(c.Folder) {
		return nil, errors.New("explicit project space/folder required")
	}
	r, err := core.OpenReader(e.state)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	b, err := r.WorkBinding(c.Project)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		var in workflowAuthor
		if name != "main-author-v1" || payload == nil || json.Unmarshal([]byte(*payload), &in) != nil || (in.Mode != "create" && in.Mode != "adopt") {
			// Readiness before explicit new-project creation is read-only.
			if name != "compatibility" && name != "workflow-context" && name != "hc-binding" && !strings.HasPrefix(name, "main-") {
				return nil, errors.New("project binding unavailable")
			}
		}
		if _, err = core.OperatorRepository(c.Folder); err != nil {
			return nil, err
		}
	} else if b.RepoRoot != c.Folder {
		return nil, errors.New("project canonical folder changed")
	}
	out := &pushEndpoint{workflow: e.workflow, hc: e.hc, allowActions: e.allowActions, state: e.state, space: c.Space, folder: c.Folder, token: e.token, ctx: e.ctx, runtime: true}
	if e.hc != nil {
		copy := *e.hc
		copy.Space, copy.Folder = c.Space, c.Folder
		out.hc = &copy
	}
	if e.workflow != nil {
		copy := *e.workflow
		copy.Space, copy.Folder = c.Space, c.Folder
		copy.Peer, _ = ctx.Value(runtimePeerKey{}).(string)
		out.workflow = &copy
	}
	return out, nil
}
