package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"ocq/internal/core"
)

// Explicit user-invoked reads only. Projections still arrive by subscription.
// No native launch, arbitrary executable, global flags or observation wait API.
var pushReads = strings.Fields(`compatibility projects board job recent-inbox inbox mail recent-results results manifest verify-worktree
hc-binding workflow-context workflow-goal
capture-receipt
queue-jobs queue-job queue-log notifications notification
dispatcher-context dispatcher-status dispatcher-read agent-reconcile-status agent-handoff-status agent-handoff-verify
work-list work-status work-recover work-output work-handoff work-preview
main-list main-status main-recover main-preview client-list client-status
compose-context compose-status intake-status notification-ack-status queued-cancel-status
command-resolve-status command-submit-context command-submit-status agent-submit-context agent-submit-status
result-publish-context result-publish-list result-publish-status capture-context capture-list capture-status`)

func pushContains(list []string, name string) bool {
	for _, s := range list {
		if name == s {
			return true
		}
	}
	return false
}

func pushAction(name string) bool {
	if strings.HasPrefix(name, "dispatcher-") && modActions()[name] != "" && name != "dispatcher-helper" {
		return true
	}
	if name == "main-author-v1" || name == "work-confirm" || name == "main-start" || name == "client-prepare" || name == "client-attention-prepare-v1" || name == "client-open" || name == "client-verify" || name == "mail-ack" {
		return true
	}
	for _, family := range []string{"compose-", "intake-", "notification-ack-", "queued-cancel-", "command-resolve-", "agent-reconcile-", "command-submit-", "agent-submit-", "result-publish-", "capture-"} {
		if strings.HasPrefix(name, family) && modActions()[name] != "" {
			return true
		}
	}
	return false
}

// Bounded output is never sent to process logs. CLI diagnostics are replaced
// with a generic failure: input or retained evidence may contain private bytes.
type pushOutput struct{ bytes.Buffer }

func (w *pushOutput) Write(p []byte) (int, error) {
	if w.Len()+len(p) > core.ObservationResponseBytes {
		return 0, errors.New("command response bound exceeded")
	}
	return w.Buffer.Write(p)
}

type pushCommandRequest struct {
	Connection string                 `json:"connection_id"`
	Args       []string               `json:"args"`
	Payload    *string                `json:"payload,omitempty"`
	Context    *runtimeProjectContext `json:"context,omitempty"`
}

func (e *pushEndpoint) command(ctx context.Context, connection string, request json.RawMessage) any {
	reply := map[string]any{"code": 1, "stdout": "", "stderr": "Command refused or uncertain. Inspect the exact retained receipt; never resend an uncertain action."}
	var input pushCommandRequest
	d := json.NewDecoder(bytes.NewReader(request))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || input.Connection != connection || len(input.Args) == 0 || len(input.Args) > 64 {
		return reply
	}
	name := input.Args[0]
	if !e.runtime && input.Context != nil {
		return reply
	}
	if e.runtime && input.Context != nil {
		scoped, err := e.projectEndpoint(ctx, *input.Context, name, input.Payload)
		if err != nil {
			return reply
		}
		e = scoped
	} else if e.runtime && (pushAction(name) || name == "main-preview" || name == "work-preview" || name == "hc-binding" || name == "workflow-context") {
		return reply
	}
	if e.runtime && pushAction(name) && !pushContains([]string{"main-author-v1", "main-start", "client-prepare", "client-attention-prepare-v1", "client-open", "client-verify", "work-confirm"}, name) {
		return reply
	}
	if strings.HasPrefix(name, "dispatcher-") && name != "dispatcher-read" && name != "dispatcher-context" && name != "dispatcher-status" && e.hc == nil {
		return reply
	}
	if e.hc != nil {
		ctx = context.WithValue(ctx, hcBindingKey{}, e.hc)
	}
	if e.workflow != nil {
		ctx = context.WithValue(ctx, workflowKey{}, e.workflow)
	}
	if !e.allowActions && (name == "work-preview" || name == "main-preview") {
		return reply
	}
	if !pushContains(pushReads, name) && (!e.allowActions || !pushAction(name)) {
		return reply
	}
	for _, arg := range input.Args {
		if strings.ContainsRune(arg, 0) {
			return reply
		}
	}
	// The caller supplies its original versioned flags. Never invent successful
	// authority on its behalf. Validate their exact endpoint scope before Cobra.
	root := bridgeRoot()
	root.SetArgs(input.Args)
	root.SetContext(ctx)
	root.SetIn(strings.NewReader(""))
	var output pushOutput
	root.SetOut(&output)
	root.SetErr(io.Discard)
	cmd, _, err := root.Find(input.Args)
	if err != nil || cmd.Name() != name {
		return reply
	}
	if err = cmd.ParseFlags(input.Args[1:]); err != nil {
		return reply
	}
	if e.runtime && input.Context != nil && pushContains([]string{"main-author-v1", "main-preview", "main-start", "main-list", "main-status", "main-recover", "client-prepare", "client-attention-prepare-v1", "client-open", "client-verify", "client-list", "client-status", "work-confirm", "work-list", "work-status", "work-output", "work-preview", "work-handoff", "work-recover"}, name) {
		args := cmd.Flags().Args()
		if len(args) < 3 || args[0] != e.space || args[2] != input.Context.Project {
			return reply
		}
		if pushContains([]string{"main-author-v1", "main-preview", "main-start", "client-prepare", "client-attention-prepare-v1", "client-open", "client-verify", "work-confirm"}, name) {
			peer, _ := ctx.Value(runtimePeerKey{}).(string)
			if len(args) < 5 || args[4] != peer {
				return reply
			}
		}
	}
	if e.workflow != nil && e.workflow.peer() != "local" && pushAction(name) &&
		!pushContains([]string{"main-author-v1", "main-start", "client-prepare", "client-attention-prepare-v1", "client-open", "client-verify", "work-confirm"}, name) {
		return reply // Selected-machine workflow is not legacy remote execution.
	}
	if e.workflow != nil && pushContains([]string{"main-author-v1", "main-preview", "main-start", "client-prepare", "client-attention-prepare-v1", "client-open", "client-verify", "work-confirm"}, name) {
		args := cmd.Flags().Args()
		if len(args) < 5 || args[4] != e.workflow.peer() {
			return reply // Compare the existing peer claim; never rewrite it.
		}
	}
	// Live handoff verification is GET-only, but still scoped to this exact
	// endpoint's space/queue. It must not borrow another space's service claim.
	if name == "agent-handoff-verify" {
		args := cmd.Flags().Args()
		binding, err := compatibilityContext(ctx, e.space, e.folder)
		if err != nil || len(args) != 7 || args[0] != e.space || args[1] != binding.Queue {
			return reply
		}
	}
	if name == "capture-receipt" || name == "capture-receipt-preview" {
		args := cmd.Flags().Args()
		binding, err := compatibilityContext(ctx, e.space, e.folder)
		if err != nil || len(args) != 6 || args[0] != e.space || args[1] != binding.Queue {
			return reply
		}
	}
	switch name {
	case "main-author-v1":
		if input.Payload == nil || len(*input.Payload) > 20<<10 {
			return reply
		}
		root.SetIn(strings.NewReader(*input.Payload))
	case "compose-receive", "compose-preview", "intake-receive-text", "intake-review-preview", "intake-registration-draft", "intake-implementation-draft", "intake-review-draft",
		"command-resolve-receive", "command-resolve-preview",
		"command-submit-draft", "command-submit-receive", "command-submit-preview", "agent-submit-draft", "agent-submit-import", "agent-submit-receive", "agent-submit-preview",
		"result-publish-receive", "result-publish-preview", "capture-prepare", "capture-preview":
		// Replace only the transport body. The original Args and inherited
		// PersistentPreRun still enforce the exact image/action/context claim.
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			value, err := e.exchange(name, args, input.Payload)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(value)
		}
	default:
		if input.Payload != nil {
			return reply
		}
	}
	for flag, want := range map[string]string{"mod-space": e.space, "mod-folder": e.folder} {
		got, _ := cmd.Flags().GetString(flag)
		if pushAction(name) && got != want || got != "" && got != want {
			return reply
		}
	}
	// Preserve all existing validation/preview/attempt/receipt-first/CAS guards.
	// Serial session dispatch settles each command before processing another;
	// disconnect never cancels an already accepted durable operation.
	if err = root.Execute(); err != nil {
		return reply
	}
	return map[string]any{"code": 0, "stdout": output.String(), "stderr": ""}
}

func (e *pushEndpoint) exchange(name string, args []string, payload *string) (any, error) {
	if !e.allowActions {
		return nil, errors.New("actions not enabled")
	}
	switch name {
	case "command-resolve-receive", "command-resolve-preview":
		t := core.CommandResolutionTarget{Machine: "local", Space: args[0], Queue: args[1], Job: args[2], Sequence: args[3]}
		if name == "command-resolve-receive" {
			if payload == nil {
				return nil, errors.New("exact resolution required")
			}
			status, err := core.PrepareCommandResolutionBytes(e.state, t, []byte(*payload))
			return map[string]any{"status": status}, err
		}
		status, exact, err := core.ReadCommandResolutionBytes(e.state, t, args[4])
		return map[string]any{"status": status, "exact": exact}, err
	case "result-publish-receive", "result-publish-preview", "capture-prepare", "capture-preview":
		t := core.ResultPublicationTarget{CommandResolutionTarget: core.CommandResolutionTarget{Machine: "local", Space: args[0], Queue: args[1], Job: args[3], Sequence: args[4]}, Project: args[2]}
		var status any
		var exact string
		var err error
		switch name {
		case "result-publish-receive":
			if payload == nil {
				return nil, errors.New("exact publication required")
			}
			status, err = core.PrepareResultPublicationBytes(e.state, t, []byte(*payload))
		case "result-publish-preview":
			status, exact, err = core.ReadResultPublicationBytes(e.state, t, args[5], args[6])
		case "capture-prepare":
			if payload == nil {
				return nil, errors.New("exact capture options required")
			}
			status, err = core.PrepareCapturePublicationBytes(e.state, t, args[5], []byte(*payload))
		case "capture-preview":
			status, exact, err = core.ReadCapturePublicationBytes(e.state, t, args[5], args[6])
		}
		return map[string]any{"status": status, "exact": exact}, err
	case "command-submit-draft", "command-submit-receive", "command-submit-preview", "agent-submit-draft", "agent-submit-import", "agent-submit-receive", "agent-submit-preview":
		scope := core.CommandSubmissionScope{Machine: "local", Space: args[0], Queue: args[1]}
		mode := name[strings.LastIndex(name, "-")+1:]
		id := ""
		if mode == "preview" {
			id = args[2]
		} else if payload == nil {
			return nil, errors.New("exact payload required")
		}
		var raw []byte
		if payload != nil {
			raw = []byte(*payload)
		}
		var status core.CommandSubmissionStatus
		var review json.RawMessage
		var err error
		if strings.HasPrefix(name, "agent-") {
			status, review, err = core.AgentSubmissionBytes(e.state, scope, mode, id, raw)
		} else {
			status, review, err = core.CommandSubmissionBytes(e.state, scope, mode, id, raw)
		}
		return map[string]any{"status": status, "review": review}, err
	case "compose-receive":
		if payload == nil {
			return nil, errors.New("exact bytes required")
		}
		status, err := core.PrepareLocalComposeBytes(e.state, []byte(*payload))
		return map[string]any{"response": "PREPARED " + status.ID}, err
	case "compose-preview":
		intent, err := core.ReadLocalComposeIntent(e.state, args[0])
		if err != nil {
			return nil, err
		}
		if intent.Status.State != "prepared" && intent.Status.State != "armed" {
			return nil, errors.New("unattempted compose intent required")
		}
		return map[string]any{"response": string(intent.Request), "status": intent.Status}, nil
	case "intake-receive-text":
		if payload == nil {
			return nil, errors.New("exact bytes required")
		}
		status, err := core.PrepareLocalProjectIntake(e.state, args[0], []byte(*payload))
		return map[string]any{"response": "PREPARED " + status.ID}, err
	case "intake-review-preview":
		status, kind, raw, err := core.ReadLocalProjectIntakeReview(e.state, args[0])
		return map[string]any{"kind": kind, "exact": json.RawMessage(raw), "state": status.State, "id": status.ID}, err
	default:
		if payload == nil {
			return nil, errors.New("exact draft bytes required")
		}
		kind, raw, err := core.DraftProjectIntakeBytes(e.state, strings.TrimSuffix(strings.TrimPrefix(name, "intake-"), "-draft"), []byte(*payload))
		return map[string]any{"kind": kind, "exact": json.RawMessage(raw), "state": "draft"}, err
	}
}
