package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

// Configuration bootstrap only. Never opens a queue, discovers an HC route,
// starts an endpoint or grants action authority. Its stdout is private: only the
// native mod consumes it in memory; callers must not log it.
type pushAssociation struct {
	Version       int     `json:"version"`
	Endpoint      string  `json:"endpoint"`
	Credential    string  `json:"credential_file"`
	Machine       *string `json:"machine"`
	Space         string  `json:"space"`
	Folder        string  `json:"folder"`
	QueueIdentity string  `json:"queue_identity"`
}

func pushAssociationCommand() *cobra.Command {
	return &cobra.Command{Use: "push-association PRIVATE_DIRECTORY MACHINE SPACE FOLDER", Args: cobra.ExactArgs(4), RunE: func(cmd *cobra.Command, args []string) error {
		association, token, err := readPushAssociation(args)
		if err != nil {
			// Do not echo configuration contents, paths, process errors or secrets.
			return errors.New("private push association unavailable, unsafe or does not match selected context")
		}
		defer clear(token)
		return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
			pushAssociation
			Token string `json:"token"`
		}{association, string(token)})
	}}
}

func readPushAssociation(args []string) (pushAssociation, []byte, error) {
	var a pushAssociation
	fail := errors.New("invalid private association")
	dir := args[0]
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return a, nil, fail
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil || canonical != dir {
		return a, nil, fail
	}
	fd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return a, nil, fail
	}
	defer syscall.Close(fd)
	var st syscall.Stat_t
	if syscall.Fstat(fd, &st) != nil || st.Uid != uint32(os.Getuid()) || st.Mode&07777 != 0700 {
		return a, nil, fail
	}
	// All reads are relative to the verified open directory, not a raced pathname.
	read := func(name string, limit int64) ([]byte, error) {
		n, err := syscall.Openat(fd, name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
		if err != nil {
			return nil, fail
		}
		f := os.NewFile(uintptr(n), name)
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return nil, fail
		}
		s, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.Mode().IsRegular() || s.Uid != uint32(os.Getuid()) || s.Mode&07777 != 0600 || info.Size() > limit {
			return nil, fail
		}
		raw, err := io.ReadAll(io.LimitReader(f, limit+1))
		if err != nil || int64(len(raw)) > limit {
			return nil, fail
		}
		return raw, nil
	}
	raw, err := read("association.json", 16384)
	if err != nil {
		return a, nil, fail
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var fields map[string]json.RawMessage
	if decoder.Decode(&a) != nil || decoder.Decode(new(any)) != io.EOF ||
		json.Unmarshal(raw, &fields) != nil || len(fields) != 7 {
		return a, nil, fail
	}
	machine := ""
	if a.Machine != nil {
		machine = *a.Machine
		if machine == "" {
			return a, nil, fail
		}
	}
	queue, err := hex.DecodeString(a.QueueIdentity)
	u, urlErr := url.Parse(a.Endpoint)
	if a.Version != 1 || machine != args[1] || a.Space != args[2] || a.Folder != args[3] ||
		!filepath.IsAbs(a.Folder) || a.Space == "" || err != nil || len(queue) != 32 ||
		a.QueueIdentity != strings.ToLower(a.QueueIdentity) ||
		a.Credential == "" || a.Credential == "." || a.Credential == ".." || strings.ContainsAny(a.Credential, `/\`) ||
		urlErr != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || u.Path != "/ocq" || u.RawPath != "" ||
		(u.Scheme != "wss" && (u.Scheme != "ws" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "::1"))) {
		return a, nil, fail
	}
	token, err := read(a.Credential, 65)
	if err != nil || len(token) != 65 || token[64] != '\n' {
		return a, nil, fail
	}
	secret, err := hex.DecodeString(string(token[:64]))
	if err != nil || len(secret) != 32 {
		clear(token)
		return a, nil, fail
	}
	clear(secret)
	return a, token[:64], nil
}
