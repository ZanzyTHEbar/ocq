package core

import (
	"errors"
	"os"
	"strings"
)

const TUIOpenProtocol = "ocq-tui-open-v1"

type TUIOpenPointer struct {
	Protocol string `json:"protocol"`
	Queue    string `json:"queue"`
	Store    string `json:"store"`
	Project  string `json:"project"`
	Kind     string `json:"kind"`
	Work     string `json:"work"`
	Session  string `json:"session"`
}

type TUIOpenTarget struct {
	Pointer           TUIOpenPointer `json:"pointer"`
	Directory         string         `json:"directory"`
	OpenCodeProjectID string         `json:"opencode_project_id"`
	OwnershipSHA256   string         `json:"ownership_sha256"`
	ProfileSHA256     string         `json:"profile_sha256,omitempty"`
	ServerOrigin      string         `json:"server_origin"`
	ServerPID         int            `json:"server_pid"`
	ServerVersion     string         `json:"server_version"`
	ServerHostname    string         `json:"server_hostname"`
}

// ResolveTUIOpen is a local read only. The already-running TUI uses its own
// authenticated client to validate the server and target; this helper never
// connects to OpenCode, initializes the queue, starts a viewer, writes a
// journal, or supplies credentials.
func ResolveTUIOpen(dir, openCodeState string, raw []byte) (TUIOpenTarget, error) {
	var out TUIOpenTarget
	var pointer TUIOpenPointer
	if _, err := decodeStrictJSONObject(raw, 2048, &pointer); err != nil {
		return out, err
	}
	if pointer.Protocol != TUIOpenProtocol || !isValidProjectID(pointer.Project) ||
		!isValidHexLen(pointer.Queue, 64) || !isValidHexLen(pointer.Store, 64) ||
		!strings.HasPrefix(pointer.Session, "ses_") || !isValidSessionID(pointer.Session) ||
		!(pointer.Kind == "integrator" && pointer.Work == "" || pointer.Kind == "worker" && workID(pointer.Work)) {
		return out, errors.New("invalid exact OCQ TUI pointer")
	}
	r, err := OpenReader(dir)
	if err != nil {
		return out, err
	}
	defer r.Close()
	queue, store, err := r.WorkQueueContext()
	if err != nil || pointer.Queue != queue || pointer.Store != store {
		return out, errors.New("OCQ queue or store changed")
	}
	journal, err := workReadDir(r, pointer.Project)
	if err != nil {
		return out, err
	}
	defer journal.Close()
	target, err := storedClientTarget(r, journal, ClientPrepareRequest{
		ProjectID: pointer.Project, TargetKind: pointer.Kind, WorkRequestID: pointer.Work,
	})
	if err != nil {
		return out, err
	}
	if target.SessionID != pointer.Session || target.OpenCodeProjectID == "" ||
		!isValidHexLen(target.OwnershipSHA256, 64) ||
		pointer.Kind == "worker" && !isValidHexLen(target.ProfileSHA256, 64) {
		return out, errors.New("exact OCQ TUI target changed or is unavailable")
	}
	service, err := readNativeRegistrationAt(openCodeState)
	if err != nil {
		return out, err
	}
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		return out, errors.New("local OpenCode hostname unavailable")
	}
	return TUIOpenTarget{
		Pointer: pointer, Directory: target.Cwd, OpenCodeProjectID: target.OpenCodeProjectID,
		OwnershipSHA256: target.OwnershipSHA256, ProfileSHA256: target.ProfileSHA256,
		ServerOrigin: service.URL, ServerPID: service.PID, ServerVersion: service.Version, ServerHostname: hostname,
	}, nil
}
