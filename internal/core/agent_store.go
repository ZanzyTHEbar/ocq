package core

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"ocq/internal/core/dbquery"
)

type AgentOwnership struct {
	SessionID        string `json:"session_id"`
	PromptID         string `json:"prompt_id"`
	Token            string `json:"owner_token"`
	Phase            string `json:"phase"`
	Worktree         string `json:"worktree"`
	ProjectID        string `json:"project_id"`
	ServerOutcome    string `json:"server_outcome"`
	TerminalEvidence string `json:"terminal_evidence"`
}

func randomID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

func insertAgentOwnership(c *sql.Conn, id string) error {
	session, err := randomID("ses_ocq_")
	if err != nil {
		return err
	}
	prompt, err := randomID("msg_ocq_")
	if err != nil {
		return err
	}
	token, err := randomID("owner_")
	if err != nil {
		return err
	}
	return dbquery.New(c).InsertAgentOwnership(context.Background(), dbquery.InsertAgentOwnershipParams{JobID: id, SessionID: session, PromptID: prompt, Token: token})
}

func (s *store) agentOwnership(id string) (*AgentOwnership, error) {
	a, err := dbquery.New(s.db).AgentOwnershipByJob(context.Background(), id)
	return &AgentOwnership{SessionID: a.SessionID, PromptID: a.PromptID, Token: a.Token, Phase: a.Phase, Worktree: a.Worktree, ProjectID: a.ProjectID, ServerOutcome: a.ServerOutcome, TerminalEvidence: a.TerminalEvidence}, err
}

func (s *store) agentIntent(id, kind string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return s.write(func(c *sql.Conn) error {
		q := dbquery.New(c)
		if err := q.InsertAgentOperation(context.Background(), dbquery.InsertAgentOperationParams{JobID: id, Kind: kind, Intent: b}); err != nil {
			return err
		}
		return q.SetAgentPhase(context.Background(), dbquery.SetAgentPhaseParams{Phase: kind + "_intent", JobID: id})
	})
}

// Atomic private evidence writes precede dependent state changes/mutations.
func persistEvidence(path string, b []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".evidence-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(b)
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDir(dir)
}

func (s *store) agentEvidence(id, name string, b []byte) (string, error) {
	path := filepath.Join(s.dir, "jobs", id, name+".json")
	return path, persistEvidence(path, b)
}

func (s *store) agentAck(id, kind, path, phase string) error {
	return s.write(func(c *sql.Conn) error {
		q := dbquery.New(c)
		if err := q.SetAgentOperationResponse(context.Background(), dbquery.SetAgentOperationResponseParams{ResponsePath: path, JobID: id, Kind: kind}); err != nil {
			return err
		}
		return q.SetAgentPhase(context.Background(), dbquery.SetAgentPhaseParams{Phase: phase, JobID: id})
	})
}

func (s *store) agentUnknown(id, reason string, blocked bool) error {
	outcome := "uncertain"
	if blocked {
		outcome = "blocked"
	}
	return s.write(func(c *sql.Conn) error {
		return dbquery.New(c).MarkAgentUnknown(context.Background(), dbquery.MarkAgentUnknownParams{Outcome: outcome, Reason: reason, ID: id})
	})
}

func (s *store) pollAgents() error {
	ids, err := dbquery.New(s.db).ObservableAgentJobs(context.Background())
	if err != nil {
		return err
	}
	for _, id := range ids {
		j, err := s.get(id)
		if err != nil {
			return err
		}
		if err := s.observeAgent(j); err != nil {
			return err
		}
	}
	return nil
}
