package core

import "encoding/json"

const WorkProtocol = "ocq-work-v1"
const WorkRequestBytes = 128 << 10
const WorkResponseBytes = 1 << 20

// Work is parent-driven admission/publication, not a dependent-job scheduler.
// Content is authored locally on the execution server, never in remote argv.
type WorkPrepareRequest struct {
	Protocol        string `json:"protocol"`
	ProjectID       string `json:"project_id"`
	RequestID       string `json:"request_id"`
	Kind            string `json:"kind"`
	BindingRevision string `json:"binding_revision"`
	ParentRequestID string `json:"parent_request_id,omitempty"`
	Profile         string `json:"profile,omitempty"`
	Brief           string `json:"brief,omitempty"`
	Criteria        string `json:"criteria,omitempty"`
	Rules           string `json:"rules,omitempty"`
	ReviewResult    string `json:"review_result,omitempty"`
}

type WorkProfile struct {
	Agent       string          `json:"agent"`
	ProviderID  string          `json:"provider_id"`
	ModelID     string          `json:"model_id"`
	Variant     string          `json:"variant"`
	MemoryBytes string          `json:"memory_bytes"`
	Permissions json.RawMessage `json:"permissions"`
}

// Image is the actual caller (CLI or bridge), not a substitute binary's hash.
// The bridge must independently validate peer/space and its own executable.
type WorkActionContext struct {
	Protocol      string `json:"protocol"`
	Peer          string `json:"peer"`
	Space         string `json:"space"`
	Folder        string `json:"folder"`
	QueueIdentity string `json:"queue_identity"`
	StoreContext  string `json:"store_context"`
	ImageSHA256   string `json:"image_sha256"`
	ImageRole     string `json:"image_role"`
	Action        string `json:"action"`
}

type WorkJob struct {
	JobID          string `json:"job_id"`
	Sequence       string `json:"sequence"`
	State          string `json:"state"`
	Outcome        string `json:"outcome"`
	Reason         string `json:"reason"`
	SessionID      string `json:"session_id"`
	Worktree       string `json:"worktree"`
	TerminalProven bool   `json:"terminal_proven"`
}

type WorkAssociation struct {
	AssociationID  string `json:"association_id"`
	Sequence       string `json:"sequence"`
	JobID          string `json:"job_id"`
	ManifestDigest string `json:"manifest_digest"`
	Source         string `json:"source"`
	Verification   string `json:"verification"`
	Verdict        string `json:"verdict"`
}

type WorkStatus struct {
	Protocol        string                    `json:"protocol"`
	ProjectID       string                    `json:"project_id"`
	RequestID       string                    `json:"request_id"`
	Kind            string                    `json:"kind"`
	State           string                    `json:"state"`
	IntentSHA256    string                    `json:"intent_sha256"`
	CreatedAt       string                    `json:"created_at"`
	BindingRevision string                    `json:"binding_revision"`
	ParentRequestID string                    `json:"parent_request_id,omitempty"`
	Receipt         *ProjectSubmissionReceipt `json:"receipt,omitempty"`
	Job             *WorkJob                  `json:"job,omitempty"`
	Result          *WorkAssociation          `json:"result,omitempty"`
}

type WorkPreview struct {
	Protocol      string            `json:"protocol"`
	Status        WorkStatus        `json:"status"`
	Context       WorkActionContext `json:"context"`
	PreviewSHA256 string            `json:"preview_sha256"`
	Binding       ProjectBinding    `json:"binding"`
	InputJSON     string            `json:"input_json"`
	Payload       string            `json:"payload"`
	PayloadSHA256 string            `json:"payload_sha256"`
}

type WorkObservationCursor struct {
	ProjectID   string `json:"project_id"`
	Fingerprint string `json:"fingerprint"`
}

type WorkJournalObservation struct {
	WorkObservationCursor
	Consistency string `json:"consistency"`
	ReadAt      string `json:"read_at"`
}

type WorkList struct {
	Protocol  string                 `json:"protocol"`
	ProjectID string                 `json:"project_id"`
	Items     []WorkStatus           `json:"items"`
	Journal   WorkJournalObservation `json:"journal"`
}

type WorkOutput struct {
	Protocol   string     `json:"protocol"`
	Status     WorkStatus `json:"status"`
	Text       string     `json:"text"`
	SHA256     string     `json:"sha256"`
	Provenance string     `json:"provenance"`
}

type workReviewReport struct {
	Verdict     string          `json:"verdict"`
	Findings    []reviewFinding `json:"findings"`
	Limitations string          `json:"limitations"`
}

type WorkHandoff struct {
	Protocol               string                      `json:"protocol"`
	Implementation         WorkStatus                  `json:"implementation"`
	Capture                WorkStatus                  `json:"capture"`
	Review                 WorkStatus                  `json:"review"`
	ReviewResult           WorkStatus                  `json:"review_result"`
	ImplementationManifest string                      `json:"implementation_manifest"`
	ReviewManifest         string                      `json:"review_manifest"`
	Worktree               *ProjectWorktreeCheckOutput `json:"worktree"`
	IntegrationAuthorized  bool                        `json:"integration_authorized"`
	Validation             string                      `json:"validation"`
	Readiness              string                      `json:"readiness"`
	ReviewOutput           *WorkOutput                 `json:"review_output,omitempty"`
}
