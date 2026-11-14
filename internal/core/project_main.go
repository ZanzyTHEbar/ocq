package core

import "encoding/json"

const MainProtocol = "ocq-main-v1"

type MainProfile struct {
	Role        string          `json:"role"`
	Agent       string          `json:"agent"`
	ProviderID  string          `json:"provider_id"`
	ModelID     string          `json:"model_id"`
	Variant     string          `json:"variant"`
	Permissions json.RawMessage `json:"permissions"`
}

// Main is a persistent human conversation, never a job or capacity owner.
type MainPrepareRequest struct {
	Protocol         string `json:"protocol"`
	ProjectID        string `json:"project_id"`
	OperationID      string `json:"operation_id"`
	Mode             string `json:"mode"`
	Repo             string `json:"repo,omitempty"`
	MainBranch       string `json:"main_branch,omitempty"`
	ExpectedRevision string `json:"expected_revision,omitempty"`
	Label            string `json:"label,omitempty"`
	Profile          string `json:"profile,omitempty"`
	Prompt           string `json:"prompt,omitempty"`
	Role             string `json:"role,omitempty"`
}

type MainActionContext WorkActionContext

type MainStatus struct {
	Protocol          string `json:"protocol"`
	ProjectID         string `json:"project_id"`
	OperationID       string `json:"operation_id"`
	Mode              string `json:"mode"`
	State             string `json:"state"`
	Reason            string `json:"reason"`
	IntentSHA256      string `json:"intent_sha256"`
	SetupID           string `json:"setup_id"`
	SessionID         string `json:"session_id"`
	PromptID          string `json:"prompt_id,omitempty"`
	Repo              string `json:"repo"`
	MainBranch        string `json:"main_branch"`
	Head              string `json:"head"`
	CommitAnchor      string `json:"commit_anchor"`
	HeadAdvanced      bool   `json:"head_advanced"`
	Dirty             bool   `json:"dirty"`
	BindingRevision   string `json:"binding_revision"`
	OpenCodeProjectID string `json:"opencode_project_id"`
	OwnershipSHA256   string `json:"ownership_sha256"`
	OwnershipMode     string `json:"ownership_mode"`
	ProfileSHA256     string `json:"profile_sha256"`
	ReadAt            string `json:"read_at"`
}

type MainList struct {
	Protocol  string       `json:"protocol"`
	ProjectID string       `json:"project_id"`
	Items     []MainStatus `json:"items"`
	Selected  *MainStatus  `json:"selected,omitempty"`
}

type MainPreview struct {
	Protocol         string            `json:"protocol"`
	Status           MainStatus        `json:"status"`
	Context          MainActionContext `json:"context"`
	InputJSON        string            `json:"input_json"`
	SessionJSON      string            `json:"session_json"`
	RegistrationJSON string            `json:"registration_json"`
	PromptJSON       string            `json:"prompt_json"`
	PreviewSHA256    string            `json:"preview_sha256"`
	Binding          ProjectBinding    `json:"binding"`
	Role             MainRoleContract  `json:"role"`
}

type mainRecord struct {
	Protocol         string           `json:"protocol"`
	Input            string           `json:"input_json"`
	Digest           string           `json:"input_sha256"`
	Queue            string           `json:"queue_path"`
	Device           string           `json:"queue_device"`
	Inode            string           `json:"queue_inode"`
	SetupID          string           `json:"setup_id"`
	Repo             string           `json:"repo"`
	Common           string           `json:"git_common_dir"`
	Branch           string           `json:"main_branch"`
	Head             string           `json:"head"`
	SessionID        string           `json:"session_id"`
	PromptID         string           `json:"prompt_id"`
	ProjectID        string           `json:"opencode_project_id"`
	OwnershipMode    string           `json:"ownership_mode"`
	Metadata         json.RawMessage  `json:"metadata"`
	ProfileSHA       string           `json:"profile_sha256"`
	Server           nativeService    `json:"server"`
	SessionJSON      string           `json:"session_json"`
	RegistrationJSON string           `json:"registration_json"`
	PromptJSON       string           `json:"prompt_json"`
	Created          string           `json:"created_at"`
	Binding          ProjectBinding   `json:"binding"`
	Role             MainRoleContract `json:"role"`
}
