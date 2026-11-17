package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Owner-supplied authority, never discovered from HC's environment or defaults.
// Local-only: remote machine routing, CLI connect and autostart are unsupported.
type hcBinding struct {
	Protocol string `json:"protocol"`
	Socket   string `json:"socket"`
	Device   uint64 `json:"device"`
	Inode    uint64 `json:"inode"`
	UID      uint32 `json:"uid"`
	PID      int32  `json:"pid"`
	Start    string `json:"start_ticks"`
	Boot     string `json:"boot_id"`
	Host     string `json:"host"`
	Core     string `json:"core_id"`
	Space    string `json:"space"`
	Folder   string `json:"folder"`
	Queue    string `json:"queue_identity"`
}

type hcBindingKey struct{}

func (b *hcBinding) digest() string {
	raw, _ := json.Marshal(b)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func loadHCBinding(file, space, folder, queue string) (*hcBinding, error) {
	f, err := os.OpenFile(file, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || st.Uid != uint32(os.Getuid()) || info.Size() > 16384 || !filepath.IsAbs(file) {
		return nil, errors.New("HC binding requires owned private0600 regular file")
	}
	var b hcBinding
	d := json.NewDecoder(io.LimitReader(f, 16385))
	d.DisallowUnknownFields()
	if d.Decode(&b) != nil || d.Decode(new(any)) != io.EOF ||
		b.Protocol != "ocq-private-hc-v1" || b.Space != space || b.Folder != folder || b.Queue != queue ||
		!filepath.IsAbs(b.Socket) || b.Socket != filepath.Clean(b.Socket) || b.PID <= 0 || b.Start == "" ||
		b.Core == "" || b.Host == "" || b.Boot == "" || b.UID != uint32(os.Getuid()) || b.Inode == 0 {
		return nil, errors.New("exact private HC binding required")
	}
	return &b, nil
}

type hcConnection struct {
	net.Conn
	reader *bufio.Reader
	next   int
}

func (c *hcConnection) call(method string, params any, result any) error {
	c.next++
	if err := json.NewEncoder(c.Conn).Encode(map[string]any{"jsonrpc": "2.0", "id": c.next, "method": method, "params": params}); err != nil {
		return err
	}
	line, err := c.reader.ReadSlice('\n')
	if err != nil {
		return errors.New("HC response unavailable or exceeds bound; never retry")
	}
	var reply struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(line, &reply) != nil || reply.JSONRPC != "2.0" || reply.ID != c.next ||
		(len(reply.Error) != 0 && string(reply.Error) != "null") || len(reply.Result) == 0 {
		return errors.New("HC exact response unavailable; outcome may be unknown")
	}
	return json.Unmarshal(reply.Result, result)
}

func hcStart(pid int32) (string, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(int(pid)) + "/stat")
	if err != nil {
		return "", err
	}
	at := bytes.LastIndex(raw, []byte(") "))
	if at < 0 {
		return "", errors.New("HC process identity unavailable")
	}
	fields := strings.Fields(string(raw[at+2:]))
	if len(fields) < 20 {
		return "", errors.New("HC process identity unavailable")
	}
	return fields[19], nil
}

func (b *hcBinding) connect(ctx context.Context) (*hcConnection, error) {
	host, err := os.Hostname()
	if err != nil || host != b.Host {
		return nil, errors.New("HC execution host changed")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil || strings.TrimSpace(string(boot)) != b.Boot {
		return nil, errors.New("HC boot changed")
	}
	start, err := hcStart(b.PID)
	if err != nil || start != b.Start {
		return nil, errors.New("HC owner process changed")
	}
	info, err := os.Lstat(b.Socket)
	if err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(b.Socket)
	if err != nil || canonical != b.Socket {
		return nil, errors.New("canonical HC socket required")
	}
	parent, err := os.Lstat(filepath.Dir(b.Socket))
	if err != nil {
		return nil, err
	}
	owner, ok := parent.Sys().(*syscall.Stat_t)
	if !ok || !parent.IsDir() || parent.Mode().Perm() != 0700 || owner.Uid != b.UID {
		return nil, errors.New("private owned HC socket directory required")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0077 != 0 || st.Uid != b.UID || uint64(st.Dev) != b.Device || st.Ino != b.Inode {
		return nil, errors.New("HC socket ownership/incarnation changed")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", b.Socket)
	if err != nil {
		return nil, err
	} // no ensure/start/reconnect
	fail := func(err error) (*hcConnection, error) { conn.Close(); return nil, err }
	u, ok := conn.(*net.UnixConn)
	if !ok {
		return fail(errors.New("local Unix HC required"))
	}
	raw, err := u.SyscallConn()
	if err != nil {
		return fail(err)
	}
	var cred *unix.Ucred
	var credErr error
	if err = raw.Control(func(fd uintptr) { cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil {
		return fail(err)
	}
	if credErr != nil || cred == nil || cred.Pid != b.PID || cred.Uid != b.UID {
		return fail(errors.New("HC listener owner changed"))
	}
	deadline := time.Now().Add(20 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return fail(err)
	}
	c := &hcConnection{Conn: conn, reader: bufio.NewReaderSize(conn, 256<<10)}
	var hello struct {
		Generation int    `json:"generation"`
		Version    string `json:"version"`
		Core       struct {
			ID string `json:"id"`
		} `json:"core"`
	}
	if err = c.call("hello", map[string]any{"generation": 1, "framed": false}, &hello); err != nil {
		return fail(err)
	}
	if hello.Generation != 1 || hello.Version != "0.1.21" || hello.Core.ID != b.Core {
		return fail(errors.New("HC core/version changed"))
	}
	// Check the path again after connecting. A replacement is not the pinned core.
	after, err := os.Lstat(b.Socket)
	if err != nil || !os.SameFile(info, after) {
		return fail(errors.New("HC socket replaced"))
	}
	return c, nil
}

func boundHC(ctx context.Context, digest string) (*hcBinding, error) {
	b, _ := ctx.Value(hcBindingKey{}).(*hcBinding)
	if b == nil || !clientDigest.MatchString(digest) || digest != b.digest() {
		return nil, errors.New("explicit reviewed private HC binding required")
	}
	return b, nil
}
