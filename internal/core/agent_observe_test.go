package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ocq/internal/core/dbquery"
)

// Install a committed terminal snapshot in a private queue without running an
// agent, then test the public reconciliation path against an oversized audit.
func TestReconcileKeepsCommittedAgentFinishWithOversizedAudit(t *testing.T) {
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	q, err := OpenQueue(filepath.Join(root, "queue"))
	must(err)
	defer q.Close()
	request, err := json.Marshal(agentRequest{
		Version: 2, Kind: "agent", Repo: root, Base: strings.Repeat("a", 40), Prompt: "fixture",
		Agent: "fixture", Model: agentModel{ID: "fixture", ProviderID: "fixture"},
		Permissions:     []permissionRule{{Action: "*", Resource: "*", Effect: "deny"}},
		ProvisionPolicy: provisionPolicy, MemoryBytes: 1 << 20,
	})
	must(err)
	job, err := q.store.acceptCanonical("terminal-audit-fixture", request, true)
	must(err)
	audit, err := json.Marshal(map[string]any{
		"completion_policy": agentCompletionPolicy, "prompt_id": job.Agent.PromptID,
		"request_digest": job.Digest, "stable_transcript": true, "active": false,
		"messages": []string{strings.Repeat("x", 8<<20)},
	})
	must(err)
	path := filepath.Join(root, "terminal-evidence-cooperative-v1.json")
	must(os.WriteFile(path, audit, 0600))
	ctx := context.Background()
	must(q.store.write(func(c *sql.Conn) error {
		// No Workmux path: a regression must not reach any external service.
		if _, err := c.ExecContext(ctx, "UPDATE jobs SET state='unknown' WHERE id=?", job.ID); err != nil {
			return err
		}
		queries := dbquery.New(c)
		if err := queries.CompleteAgentOwnership(ctx, dbquery.CompleteAgentOwnershipParams{JobID: job.ID, ServerOutcome: "succeeded", TerminalEvidence: path}); err != nil {
			return err
		}
		_, err := queries.CompleteAgentJob(ctx, dbquery.CompleteAgentJobParams{ID: job.ID, Outcome: "agent_succeeded", Reason: "committed fixture"})
		return err
	}))
	before, err := q.Job(job.ID)
	must(err)
	events, err := q.Notifications(10)
	must(err)
	if before.State != "finished" || workTerminal(before) || len(events) != 1 {
		t.Fatal("expected a committed finish and event, but no downstream proof from the oversized audit")
	}

	after, err := q.Reconcile(job.ID)
	must(err)
	afterEvents, err := q.Notifications(10)
	must(err)
	afterAudit, err := os.ReadFile(path)
	must(err)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(events, afterEvents) || !bytes.Equal(audit, afterAudit) {
		t.Fatal("reconciliation changed the terminal job, completion event or private audit")
	}
}
