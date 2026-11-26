package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
	"ocq/internal/core"
)

var tuiNonce = regexp.MustCompile(`^[0-9a-f]{32}$`)

func tuiForeground(pid int) (string, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", err
	}
	i := strings.LastIndex(string(raw), ") ")
	if i >= 0 {
		f := strings.Fields(string(raw[i+2:]))
		// Require the leader of a real controlling terminal's foreground
		// process group, not a background/suspended TUI or a reused pane.
		if len(f) > 19 && f[0] != "T" && f[0] != "t" && f[0] != "Z" &&
			f[4] != "0" && f[2] == strconv.Itoa(pid) && f[2] == f[5] {
			return f[19], nil
		}
	}
	return "", errors.New("OpenCode client is not the foreground process")
}

type tuiOpenFrame struct {
	Mode      string               `json:"mode"`
	Challenge string               `json:"challenge"`
	Client    string               `json:"client"`
	Terminal  string               `json:"terminal"`
	Cwd       string               `json:"cwd"`
	Target    *core.TUIOpenPointer `json:"target"`
}

type tuiOpenReply struct {
	Challenge string `json:"challenge"`
	Client    string `json:"client"`
	Terminal  string `json:"terminal"`
	Session   string `json:"session"`
	Outcome   string `json:"outcome"`
}

// Local GUI-to-TUI control only. Never admitted to the remote push allowlist;
// the selected, same-user foreground TUI does the authoritative session read.
func tuiOpenCommand() *cobra.Command {
	return &cobra.Command{Use: "tui-open MODE PID TERMINAL CWD CLIENT TARGET", Args: cobra.ExactArgs(6),
		RunE: func(cmd *cobra.Command, a []string) error {
			pid, err := strconv.Atoi(a[1])
			if err != nil || pid < 2 || len(a[2]) > 32 || !nativeTerminalID.MatchString(a[2]) ||
				!filepath.IsAbs(a[3]) || len(a[3]) > 4096 || strings.ContainsAny(a[3], "\r\n\x00") ||
				!(a[0] == "check" && a[4] == "-" || a[0] == "open" && tuiNonce.MatchString(a[4])) ||
				len(a[5]) > 2048 {
				return errors.New("invalid exact TUI request")
			}
			var target core.TUIOpenPointer
			if json.Unmarshal([]byte(a[5]), &target) != nil || target.Protocol != core.TUIOpenProtocol {
				return errors.New("invalid exact TUI target")
			}
			reply, err := requestTUI(cmd.Context(), pid, tuiOpenFrame{
				Mode: a[0], Terminal: a[2], Cwd: a[3], Client: a[4], Target: &target,
			})
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(reply)
		}}
}

func requestTUI(parent context.Context, pid int, req tuiOpenFrame) (tuiOpenReply, error) {
	var reply tuiOpenReply
	start, err := tuiForeground(pid)
	if err != nil {
		return reply, err
	}
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if !filepath.IsAbs(runtime) {
		return reply, errors.New("private XDG_RUNTIME_DIR required")
	}
	canonical, err := filepath.EvalSymlinks(runtime)
	if err != nil || canonical != runtime {
		return reply, errors.New("canonical XDG_RUNTIME_DIR required")
	}
	fd, err := unix.Open(runtime, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return reply, err
	}
	defer unix.Close(fd)
	var dir unix.Stat_t
	if err := unix.Fstat(fd, &dir); err != nil || dir.Uid != uint32(os.Getuid()) || dir.Mode&0777 != 0700 {
		return reply, errors.New("private owned0700 XDG_RUNTIME_DIR required")
	}
	name := "ocq-tui-" + strconv.Itoa(pid) + "-" + start
	var socket unix.Stat_t
	if err := unix.Fstatat(fd, name, &socket, unix.AT_SYMLINK_NOFOLLOW); err != nil ||
		socket.Uid != dir.Uid || socket.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return reply, errors.New("no opted-in foreground OpenCode client")
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	path := "/proc/self/fd/" + strconv.Itoa(fd) + "/" + name
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return reply, err
	}
	defer conn.Close()
	raw, err := conn.(*net.UnixConn).SyscallConn()
	if err != nil {
		return reply, err
	}
	var peer *unix.Ucred
	err = raw.Control(func(fd uintptr) {
		peer, _ = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if err != nil || peer == nil || peer.Uid != dir.Uid || int(peer.Pid) != pid {
		return reply, errors.New("exact foreground OpenCode socket owner did not match")
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	read := bufio.NewReaderSize(conn, 1024)
	line, err := read.ReadSlice('\n')
	if err != nil || json.Unmarshal(line, &reply) != nil || !tuiNonce.MatchString(reply.Challenge) ||
		!tuiNonce.MatchString(reply.Client) || reply.Terminal != req.Terminal ||
		req.Mode == "open" && reply.Client != req.Client {
		return tuiOpenReply{}, errors.New("exact OpenCode client changed")
	}
	if now, err := tuiForeground(pid); err != nil || now != start || ctx.Err() != nil {
		return tuiOpenReply{}, errors.New("OpenCode foreground changed")
	}
	req.Challenge, req.Client = reply.Challenge, reply.Client
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return tuiOpenReply{}, err
	}
	reply = tuiOpenReply{}
	line, err = read.ReadSlice('\n')
	if err != nil || json.Unmarshal(line, &reply) != nil ||
		reply.Client != req.Client || reply.Challenge != req.Challenge || reply.Terminal != req.Terminal ||
		reply.Session != req.Target.Session ||
		!(req.Mode == "check" && reply.Outcome == "ready" || req.Mode == "open" && reply.Outcome == "routed") {
		return tuiOpenReply{}, errors.New("exact OpenCode route not confirmed")
	}
	// The challenge is connection-local, never retained or exposed to the UI.
	reply.Challenge = ""
	return reply, nil
}
