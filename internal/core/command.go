// Package core contains OCQ's transport-independent operations and data types.
package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"unicode/utf8"

	"github.com/ZanzyTHEbar/faults-go"
)

// commandRequest is the immutable version-1 command receipt payload. Its field
// order and JSON tags are part of the persisted receipt contract.
type commandRequest struct {
	Version     int      `json:"version"`
	Argv        []string `json:"argv"`
	Cwd         string   `json:"cwd"`
	MemoryBytes int64    `json:"memory_bytes"`
}

// newCommandRequest resolves a command and its working directory before intake.
func newCommandRequest(argv []string, cwd string, memory int64) (commandRequest, error) {
	var request commandRequest
	if len(argv) == 0 || memory < 0 {
		return request, faults.New(faults.CodeValidationFailed, "command requires argv and nonnegative memory")
	}
	path, err := filepath.Abs(cwd)
	if err == nil {
		path, err = filepath.EvalSymlinks(path)
	}
	if err != nil {
		return request, faults.Wrap(faults.CodeStorageFailed, "resolve command working directory", err)
	}
	if !utf8.ValidString(path) {
		return request, faults.New(faults.CodeValidationFailed, "cwd must be UTF-8")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return request, faults.New(faults.CodeValidationFailed, fmt.Sprintf("cwd must be an existing directory: %s", path))
	}
	command := argv[0]
	if filepath.IsAbs(command) || filepath.Base(command) != command {
		if !filepath.IsAbs(command) {
			command = filepath.Join(path, command)
		}
	} else {
		command, err = exec.LookPath(command)
		if err != nil {
			return request, faults.Wrap(faults.CodeDependencyMissing, "find command", err)
		}
	}
	command, err = filepath.Abs(command)
	if err == nil {
		command, err = filepath.EvalSymlinks(command)
	}
	if err != nil {
		return request, faults.Wrap(faults.CodeStorageFailed, "resolve command path", err)
	}
	args := append([]string{command}, argv[1:]...)
	for _, arg := range args {
		if !utf8.ValidString(arg) {
			return request, faults.New(faults.CodeValidationFailed, "argv must be UTF-8")
		}
		for _, b := range []byte(arg) {
			if b == 0 {
				return request, faults.New(faults.CodeValidationFailed, "argv cannot contain NUL")
			}
		}
	}
	return commandRequest{Version: 1, Argv: args, Cwd: path, MemoryBytes: memory}, nil
}

// encodeCommandRequest retains the original version-1 receipt encoding.
func encodeCommandRequest(request commandRequest) ([]byte, string, error) {
	data, err := json.Marshal(request)
	digest := sha256.Sum256(data)
	return data, hex.EncodeToString(digest[:]), err
}

// decodeCommandRequest reads persisted bytes without reserializing them.
func decodeCommandRequest(data []byte) (commandRequest, error) {
	var request commandRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return request, classifyDiagnostic(faults.CodeInvariantFailed, err)
	}
	if request.Version != 1 || request.MemoryBytes < 0 || len(request.Argv) == 0 {
		return request, faults.New(faults.CodeInvariantFailed, "invalid or unsupported stored request")
	}
	return request, nil
}
