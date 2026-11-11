package core

import (
	"encoding/json"
	"errors"
	"unicode/utf8"
)

// ProjectRegistrationDraft is private form input, not a registration or receipt.
// All identities and the commit are supplied explicitly; building does no live
// lookup, session creation, queue mutation, or inferred rebind.
type ProjectRegistrationDraft struct {
	Mode                      string   `json:"mode"`
	Key                       string   `json:"key"`
	ProjectID                 string   `json:"project_id"`
	Label                     string   `json:"label"`
	ExpectedRevision          *int     `json:"expected_revision"`
	Repo                      string   `json:"repo"`
	CommitAnchor              string   `json:"commit_anchor"`
	ElephantSessionID         string   `json:"elephant_session_id"`
	ExpectedOpenCodeProjectID string   `json:"expected_opencode_project_id"`
	ExpectedOpenCodeDirectory string   `json:"expected_opencode_directory"`
	AuthRef                   string   `json:"auth_ref"`
	Intent                    string   `json:"intent"`
	Decisions                 []string `json:"decisions"`
	Risks                     []string `json:"risks"`
}

// BuildProjectRegistrationDraft generates NEW exact bytes using the same Go
// serializer and decoder as the core manifests. It never rewrites imported JSON.
// The caller must privately review and retain these bytes through normal intake.
func BuildProjectRegistrationDraft(d ProjectRegistrationDraft) ([]byte, error) {
	for _, values := range [][]string{{d.Mode, d.Key, d.ProjectID, d.Label, d.Repo, d.CommitAnchor, d.ElephantSessionID, d.ExpectedOpenCodeProjectID, d.ExpectedOpenCodeDirectory, d.AuthRef, d.Intent}, d.Decisions, d.Risks} {
		for _, value := range values {
			if !utf8.ValidString(value) {
				return nil, errors.New("registration draft requires valid UTF-8")
			}
		}
	}
	if d.ExpectedRevision == nil || (d.Mode != "new" && d.Mode != "rebind") ||
		(d.Mode == "new" && *d.ExpectedRevision != 0) || (d.Mode == "rebind" && *d.ExpectedRevision <= 0) ||
		!isValidProjectID(d.ProjectID) || d.Repo != d.ExpectedOpenCodeDirectory {
		return nil, errors.New("explicit registration mode, project, revision and matching directory required")
	}
	handoff, err := marshalCanonicalManifestJSON(handoffManifest{Version: 1, Kind: "handoff", ProjectID: d.ProjectID, Source: "operator_import", Intent: d.Intent, Decisions: d.Decisions, Risks: d.Risks})
	if err != nil {
		return nil, err
	}
	// String embedding preserves the exact handoff bytes across envelope encoding.
	embedded, err := json.Marshal(string(handoff))
	if err != nil {
		return nil, err
	}
	raw, err := marshalCanonicalManifestJSON(ProjectRegistrationRequest{Version: 1, Key: d.Key, ProjectID: d.ProjectID, Label: d.Label, ExpectedRevision: d.ExpectedRevision, Repo: d.Repo, CommitAnchor: d.CommitAnchor, ElephantSessionID: d.ElephantSessionID, ExpectedOpenCodeProjectID: d.ExpectedOpenCodeProjectID, ExpectedOpenCodeDirectory: d.ExpectedOpenCodeDirectory, AuthRef: d.AuthRef, HandoffManifestDigest: sha256Hex(handoff), HandoffManifest: embedded})
	if err != nil {
		return nil, err
	}
	if _, _, _, err := parseRegistrationPayload(raw); err != nil {
		return nil, err
	}
	return raw, nil
}
