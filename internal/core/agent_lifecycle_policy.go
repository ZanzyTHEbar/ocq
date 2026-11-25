package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"ocq/internal/core/dbquery"
)

// This is an observation contract, not a tool permission or producer attestation.
const CooperativeLifecyclePolicy = "opencode-cooperative-v1"

const lifecycleOperation = "lifecycle-policy"

type lifecycleIntent struct {
	Version  int    `json:"version"`
	Policy   string `json:"policy"`
	Source   string `json:"source"`
	Previous *Job   `json:"previous,omitempty"`
}

func insertLifecycleIntent(c *sql.Conn, id, source string, previous *Job) error {
	b, err := json.Marshal(lifecycleIntent{1, CooperativeLifecyclePolicy, source, previous})
	if err != nil {
		return err
	}
	return dbquery.New(c).InsertAgentOperation(context.Background(), dbquery.InsertAgentOperationParams{
		JobID: id, Kind: lifecycleOperation, Intent: b,
	})
}

func (s *store) lifecycleIntent(id string) (lifecycleIntent, error) {
	var in lifecycleIntent
	var b []byte
	err := s.db.QueryRow("SELECT intent FROM agent_operations WHERE job_id=? AND kind=?", id, lifecycleOperation).Scan(&b)
	if err != nil {
		return in, err
	}
	if json.Unmarshal(b, &in) != nil || in.Version != 1 || in.Policy != CooperativeLifecyclePolicy ||
		(in.Source != "admission" && in.Source != "explicit_reevaluation") {
		return in, errors.New("unsupported retained lifecycle observation policy")
	}
	if in.Source == "admission" && in.Previous != nil ||
		in.Source == "explicit_reevaluation" && (in.Previous == nil || in.Previous.ID != id || in.Previous.State != "unknown" || in.Previous.Agent == nil) {
		return in, errors.New("retained lifecycle policy has no coherent admission/previous ownership snapshot")
	}
	return in, nil
}

// An old job is not opted into a new observer by replacing a binary or polling.
// Re-evaluation gets one immutable, dispatcher-lock-exclusive intent before GETs.
func (s *store) prepareLifecycleReevaluation(j Job, policy string) error {
	if policy != CooperativeLifecyclePolicy || j.State != "unknown" {
		return errors.New("explicit lifecycle re-evaluation requires the supported policy and an exact unknown agent job")
	}
	if _, err := s.lifecycleIntent(j.ID); !errors.Is(err, sql.ErrNoRows) {
		return errors.New("lifecycle policy already retained; inspect the prior attempt rather than repeat it")
	}
	return s.write(func(c *sql.Conn) error {
		return insertLifecycleIntent(c, j.ID, "explicit_reevaluation", &j)
	})
}

type lifecycleRuntime struct {
	Version string `json:"version"`
	PID     int    `json:"pid"`
}

func (s *store) lifecycleRuntime(j Job, evidence string) (lifecycleRuntime, error) {
	var runtime lifecycleRuntime
	b, err := apiCall(j.Agent.Worktree, "get", "/api/info", nil)
	if err != nil {
		return runtime, err
	}
	// /api/info is a raw ServerInfo, unlike session/resource data envelopes.
	if json.Unmarshal(b, &runtime) != nil || !isOpenCodeV2(runtime.Version) || runtime.PID <= 0 {
		return runtime, fmt.Errorf("%s requires identifiable OpenCode V2 /api/info; observation unavailable", CooperativeLifecyclePolicy)
	}
	if _, err := s.agentEvidence(j.ID, evidence, b); err != nil {
		return runtime, err
	}
	return runtime, nil
}
