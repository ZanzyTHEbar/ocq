package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"ocq/internal/core"
)

const modProtocol = "ocq-herdr-local-v1"
const modClaims = "action-contracts-v1-explicit-claims-no-implicit-retry"

// Explicit, reviewed per-action semantics, not an optimistic version range.
// A change to any action's claim/recovery meaning requires a new action value
// AND matched mod release. Existing core journals still validate their own v1.
func modActions() map[string]string {
	families := map[string][]string{
		"compose":          {"receive", "preview", "dispatch-intent", "arm", "dispatch", "reconcile", "cancel", "retire"},
		"intake":           {"implementation-draft", "review-draft", "registration-draft", "receive", "receive-text", "preview", "review-preview", "arm", "dispatch", "reconcile", "cancel", "retire"},
		"notification-ack": {"prepare", "arm", "attempt"},
		"queued-cancel":    {"prepare", "arm", "attempt"},
		"command-resolve":  {"receive", "preview", "arm", "attempt"},
		"agent-reconcile":  {"prepare", "arm", "attempt"},
		"command-submit":   {"draft", "receive", "preview", "arm", "attempt", "reconcile", "cancel", "retire"},
		"agent-submit":     {"draft", "import", "receive", "preview", "arm", "attempt", "reconcile", "cancel", "retire"},
		"result-publish":   {"receive", "preview", "arm", "attempt"},
		"capture":          {"prepare", "preview", "arm", "attempt"},
		"dispatcher":       {"prepare", "arm", "attempt", "bind", "cancel", "drain-prepare", "drain-arm", "drain-attempt", "retire", "native-launch"},
	}
	out := map[string]string{"mail-ack": "mail-ack-sequence-read-before-archive-v1"}
	for family, ops := range families {
		for _, op := range ops {
			out[family+"-"+op] = family + "/" + op + "/journal-v1"
		}
	}
	return out
}

// These commands retain their existing read/observation contract without a
// handshake. Journal status/rendezvous may open their existing private locks;
// none authorizes a claim, dispatch, acknowledgement or native launch.
func modRead(action string) bool {
	switch action {
	case "compatibility", "projects", "board", "job", "recent-inbox", "inbox", "mail", "recent-results", "results", "manifest", "verify-worktree", "queue-jobs", "queue-job", "queue-log", "notifications", "notification", "agent-handoff-status", "agent-handoff-verify":
		return true
	}
	switch action {
	case "compose-context", "compose-status", "compose-rendezvous", "intake-status", "intake-rendezvous", "notification-ack-status", "queued-cancel-status", "command-resolve-status", "command-resolve-rendezvous", "agent-reconcile-status", "command-submit-context", "command-submit-status", "command-submit-rendezvous", "agent-submit-context", "agent-submit-status", "agent-submit-rendezvous", "result-publish-context", "result-publish-list", "result-publish-status", "result-publish-rendezvous", "capture-context", "capture-list", "capture-status", "capture-rendezvous", "dispatcher-context", "dispatcher-status", "dispatcher-read":
		return true
	}
	return false
}

type modCompatibility struct {
	Protocol string            `json:"protocol"`
	Claims   string            `json:"journal_claims"`
	Actions  map[string]string `json:"actions"`
	Build    string            `json:"build_kind"`
	Image    string            `json:"image_sha256"`
	Context  string            `json:"bridge_context"`
	Queue    string            `json:"queue_identity"`
	Space    string            `json:"space_id"`
	Folder   string            `json:"folder"`
}

func compatHash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func compatibility(space, folder string) (modCompatibility, error) {
	out := modCompatibility{Protocol: modProtocol, Claims: modClaims, Actions: modActions(), Build: "local-development", Space: space, Folder: folder}
	if !strings.HasPrefix(space, "sp") || len(space) <= 2 || len(space) > 128 || !utf8.ValidString(space) || strings.ContainsAny(space, "/\\ \t\r\n\x00") || !filepath.IsAbs(folder) || len(folder) > 4096 || !utf8.ValidString(folder) || strings.ContainsAny(folder, "\r\n\x00") {
		return out, errors.New("invalid compatibility location")
	}
	// Linux's executing inode, not a replacement at the executable pathname.
	f, e := os.Open("/proc/self/exe")
	if e != nil {
		return out, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(f, 128<<20+1))
	if e != nil || n > 128<<20 {
		return out, errors.New("bridge image identity unavailable")
	}
	out.Image = hex.EncodeToString(h.Sum(nil))
	dir, e := bridgeStateDir()
	if e != nil {
		return out, e
	}
	var device, inode uint64
	if r, e := core.OpenReader(dir); e == nil {
		dir = r.StateDirectory()
		s, err := r.CommandSubmissionContext(space)
		closeErr := r.Close()
		if err == nil && closeErr == nil {
			out.Queue = s.Queue
		}
		if st, err := os.Stat(filepath.Join(dir, "queue.db")); err == nil {
			s := st.Sys().(*syscall.Stat_t)
			device, inode = uint64(s.Dev), s.Ino
		}
	}
	b, _ := json.Marshal([]any{dir, device, inode, out.Queue, os.Getuid()})
	out.Context = compatHash(b)
	return out, nil
}

// Same path resolution as core storage, without opening or initializing it.
// Dispatcher intents/helpers require the resolved absolute path, including when
// OCQ_STATE_DIR is unset. Keep handshake and command execution on that path.
func bridgeStateDir() (string, error) {
	dir := os.Getenv("OCQ_STATE_DIR")
	if dir == "" {
		base := os.Getenv("XDG_STATE_HOME")
		if base == "" {
			home, e := os.UserHomeDir()
			if e != nil {
				return "", e
			}
			base = filepath.Join(home, ".local", "state")
		}
		if !filepath.IsAbs(base) {
			return "", errors.New("state base must be absolute")
		}
		dir = filepath.Join(base, "ocq")
	}
	return filepath.Abs(dir)
}

func existingBridgeStateDir() (string, error) {
	dir, e := bridgeStateDir()
	if e != nil {
		return "", e
	}
	r, e := core.OpenReader(dir)
	if e != nil {
		return "", e
	}
	dir = r.StateDirectory()
	if e = r.Close(); e != nil {
		return "", e
	}
	return dir, nil
}

func installCompatibility(root *cobra.Command) {
	var protocol, image, context, space, folder, queue, semantics string
	root.PersistentFlags().StringVar(&protocol, "mod-contract", "", "Matched mod protocol (required for mutations)")
	root.PersistentFlags().StringVar(&image, "mod-image", "", "Exact handshake bridge image")
	root.PersistentFlags().StringVar(&context, "mod-context", "", "Exact handshake bridge context")
	root.PersistentFlags().StringVar(&space, "mod-space", "", "Selected local space")
	root.PersistentFlags().StringVar(&folder, "mod-folder", "", "Selected local folder")
	root.PersistentFlags().StringVar(&queue, "mod-queue", "", "Selected queue identity")
	root.PersistentFlags().StringVar(&semantics, "mod-action", "", "Exact per-action semantic contract")
	root.AddCommand(&cobra.Command{Use: "compatibility SPACE FOLDER", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, a []string) error {
		s, e := compatibility(a[0], a[1])
		if e != nil {
			return errors.New("compatibility unavailable")
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(s)
	}})
	root.PersistentPreRunE = func(cmd *cobra.Command, a []string) error {
		name := cmd.Name()
		if modRead(name) {
			return nil
		}
		// Runtime entry already checks exact immutable intent v1, binary image,
		// queue and native argv before admission. Adding CLI flags would break that
		// identity. No prior/new image can adopt an old intent; see core runner.
		if name == "dispatcher-helper" {
			return nil
		}
		if _, ok := modActions()[name]; !ok {
			return errors.New("unsupported bridge action")
		}
		if protocol != modProtocol || semantics != modActions()[name] {
			return errors.New("matching mod compatibility contract required; quiesce before upgrading")
		}
		s, e := compatibility(space, folder)
		if e != nil || s.Image != image || s.Context != context || s.Queue == "" || s.Queue != queue {
			return errors.New("bridge compatibility/context changed; no action attempted")
		}
		for _, family := range []string{"notification-ack-", "queued-cancel-", "command-resolve-", "agent-reconcile-", "command-submit-", "agent-submit-", "result-publish-", "capture-", "dispatcher-"} {
			if strings.HasPrefix(name, family) && (len(a) < 2 || a[0] != space || a[1] != queue) {
				return errors.New("selected compatibility scope differs from action")
			}
		}
		if strings.HasPrefix(name, "dispatcher-") && (len(a) < 6 || a[5] != folder) {
			return errors.New("selected compatibility folder differs from dispatcher target")
		}
		return nil
	}
}
