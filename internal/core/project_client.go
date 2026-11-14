package core

const ClientProtocol = "ocq-client-v1"

type ClientActionContext WorkActionContext

type ClientPrepareRequest struct {
	Protocol      string `json:"protocol"`
	ProjectID     string `json:"project_id"`
	NavigationID  string `json:"navigation_id"`
	TargetKind    string `json:"target_kind"`
	WorkRequestID string `json:"work_request_id,omitempty"`
	Space         string `json:"space"`
}

// Digests and nonsecret pointers only: never metadata tokens or permissions.
type ClientOptions struct {
	Protocol          string `json:"protocol"`
	TargetKind        string `json:"target_kind"`
	ProjectID         string `json:"project_id"`
	MainID            string `json:"main_id,omitempty"`
	OwnershipMode     string `json:"ownership_mode,omitempty"`
	QueueIdentity     string `json:"queue_identity"`
	JobID             string `json:"job_id,omitempty"`
	SessionID         string `json:"session_id"`
	Worktree          string `json:"worktree"`
	OpenCodeProjectID string `json:"opencode_project_id"`
	OwnershipSHA256   string `json:"ownership_sha256"`
	ProfileSHA256     string `json:"profile_sha256"`
}

type ClientPointer struct {
	Protocol          string        `json:"protocol"`
	PointerSHA256     string        `json:"pointer_sha256"`
	ProjectID         string        `json:"project_id"`
	NavigationID      string        `json:"navigation_id"`
	WorkRequestID     string        `json:"work_request_id,omitempty"`
	TargetKind        string        `json:"target_kind"`
	QueueIdentity     string        `json:"queue_identity"`
	StoreContext      string        `json:"store_context"`
	Space             string        `json:"space"`
	BindingRevision   string        `json:"binding_revision"`
	BindingSHA256     string        `json:"binding_sha256"`
	ServerOrigin      string        `json:"server_origin"`
	ServiceIdentity   string        `json:"service_identity"`
	SessionID         string        `json:"session_id"`
	Cwd               string        `json:"cwd"`
	OpenCodeProjectID string        `json:"opencode_project_id"`
	OwnershipSHA256   string        `json:"ownership_sha256"`
	ProfileSHA256     string        `json:"profile_sha256"`
	OwnershipMode     string        `json:"ownership_mode,omitempty"`
	SetupID           string        `json:"setup_id,omitempty"`
	JobID             string        `json:"job_id,omitempty"`
	Sequence          string        `json:"sequence,omitempty"`
	LauncherArgv      []string      `json:"launcher_argv"`
	LauncherSHA256    string        `json:"launcher_sha256"`
	OpenCodeSHA256    string        `json:"opencode_sha256"`
	AssetSHA256       string        `json:"asset_sha256"`
	OverlaySHA256     string        `json:"overlay_sha256"`
	Options           ClientOptions `json:"options"`
}

type ClientTerminalReceipt struct {
	ReturnedTerminalID string            `json:"returned_terminal_id"`
	ObservedTerminalID string            `json:"observed_terminal_id"`
	Peer               string            `json:"peer"`
	Cwd                string            `json:"cwd"`
	Argv               []string          `json:"argv"`
	Labels             map[string]string `json:"labels"`
}

type ClientStatus struct {
	Protocol         string                 `json:"protocol"`
	ProjectID        string                 `json:"project_id"`
	PointerSHA256    string                 `json:"pointer_sha256"`
	State            string                 `json:"state"`
	Pointer          ClientPointer          `json:"pointer"`
	AttemptPermitted bool                   `json:"attempt_permitted"`
	Terminal         *ClientTerminalReceipt `json:"terminal,omitempty"`
	CurrentRoute     string                 `json:"current_route"`
}
