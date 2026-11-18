package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"ocq/internal/core"
)

const workflowProtocol = "ocq-operator-workflow-v1"

type workflowSelection struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Role    string `json:"role"`
	Profile string `json:"profile"`
}

// Owner input, never an arbitrary path/profile upload API for panel users.
type workflowRegistry struct {
	Protocol   string              `json:"protocol"`
	Space      string              `json:"space"`
	Folder     string              `json:"folder"`
	Queue      string              `json:"queue_identity"`
	Dispatcher core.Process        `json:"dispatcher"`
	CLISHA256  string              `json:"cli_sha256"`
	Selections []workflowSelection `json:"selections"`
	Peer       string              `json:"peer,omitempty"`
}
type workflowKey struct{}

func (r *workflowRegistry) peer() string {
	if r == nil || r.Peer == "" {
		return "local"
	}
	return r.Peer
}

func loadWorkflowRegistry(file, space, folder, queue string) (*workflowRegistry, error) {
	f, err := os.OpenFile(file, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !filepath.IsAbs(file) || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || st.Uid != uint32(os.Getuid()) || info.Size() > 128<<10 {
		return nil, errors.New("workflow registry must be an owned private0600 bounded file")
	}
	var r workflowRegistry
	d := json.NewDecoder(io.LimitReader(f, 128<<10+1))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF || r.Protocol != workflowProtocol ||
		r.Space != space || r.Folder != folder || r.Queue != queue || !clientDigest.MatchString(r.CLISHA256) ||
		len(r.Selections) < 3 || len(r.Selections) > 32 {
		return nil, errors.New("workflow registry scope/contract mismatch")
	}
	if len(r.peer()) > 128 || strings.ContainsAny(r.peer(), "/\r\n\x00") {
		return nil, errors.New("invalid selected-machine peer")
	}
	seen := map[string]bool{}
	roles := map[string]bool{}
	for _, s := range r.Selections {
		if !workPointer.MatchString(s.ID) || seen[s.ID] || s.Label == "" || len(s.Label) > 100 || strings.ContainsAny(s.Label, "\r\n\x00") || len(s.Profile) > 16<<10 ||
			(s.Role != "main" && s.Role != "implementation" && s.Role != "review") {
			return nil, errors.New("invalid owner workflow selection")
		}
		seen[s.ID] = true
		roles[s.Role] = true
	}
	if len(roles) != 3 {
		return nil, errors.New("Main, implementation and review selections are all required")
	}
	return &r, nil
}

func (r *workflowRegistry) ready(ctx context.Context, state string) (string, error) {
	if r == nil {
		return "", errors.New("owner workflow registry not provisioned")
	}
	hc, _ := ctx.Value(hcBindingKey{}).(*hcBinding)
	if hc == nil || hc.Space != r.Space || hc.Folder != r.Folder || hc.Queue != r.Queue {
		return "", errors.New("exact private native HC capability missing")
	}
	conn, err := hc.connect(ctx)
	if err != nil {
		return "", err
	}
	conn.Close()
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	cli := filepath.Join(filepath.Dir(exe), "ocq")
	raw, err := os.ReadFile(cli)
	if err != nil || compatHash(raw) != r.CLISHA256 {
		return "", errors.New("matched owner CLI image unavailable")
	}
	if err = core.VerifyOperatorDispatcher(state, cli, r.Dispatcher); err != nil {
		return "", err
	}
	if err = core.VerifyOperatorNative(); err != nil {
		return "", err
	}
	selections := []core.OperatorProfileSelection{}
	for _, s := range r.Selections {
		selections = append(selections, core.OperatorProfileSelection{Role: s.Role, Profile: s.Profile})
	}
	if err = core.VerifyOperatorSelections(r.Folder, selections); err != nil {
		return "", err
	}
	return core.OperatorRepository(r.Folder)
}

type workflowAuthor struct {
	Protocol         string `json:"protocol"`
	Mode             string `json:"mode"`
	Label            string `json:"label,omitempty"`
	ExpectedRevision string `json:"expected_revision,omitempty"`
	Main             string `json:"main_selection,omitempty"`
	Implementation   string `json:"implementation_selection,omitempty"`
	Review           string `json:"review_selection,omitempty"`
	Goal             string `json:"goal,omitempty"`
}

func workflowCommands() []*cobra.Command {
	contextCommand := &cobra.Command{Use: "workflow-context SPACE QUEUE", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, a []string) error {
		r, _ := cmd.Context().Value(workflowKey{}).(*workflowRegistry)
		dir, err := existingBridgeStateDir()
		if err != nil {
			return err
		}
		out := map[string]any{"protocol": workflowProtocol, "ready": false, "reason": "Owner must provision workflow execution and native navigation.", "selections": []any{}}
		if r == nil || r.Space != a[0] || r.Queue != a[1] {
			return writeWork(cmd, out)
		}
		branch, err := r.ready(cmd.Context(), dir)
		if err != nil {
			return writeWork(cmd, out)
		}
		choices := []map[string]string{}
		for _, s := range r.Selections {
			choices = append(choices, map[string]string{"id": s.ID, "label": s.Label, "role": s.Role})
		}
		out["ready"], out["reason"], out["repo"], out["branch"], out["selections"] = true, "", r.Folder, branch, choices
		return writeWork(cmd, out)
	}}
	author := &cobra.Command{Use: "main-author-v1 SPACE QUEUE PROJECT OPERATION PEER FOLDER STORE IMAGE", Args: cobra.ExactArgs(8), RunE: func(cmd *cobra.Command, a []string) error {
		r, _ := cmd.Context().Value(workflowKey{}).(*workflowRegistry)
		dir, err := existingBridgeStateDir()
		if err != nil {
			return err
		}
		if r == nil || a[0] != r.Space || a[1] != r.Queue || a[5] != r.Folder || a[4] != r.peer() || !workPointer.MatchString(a[3]) {
			return errors.New("selected-machine owner workflow scope required")
		}
		binding, err := compatibility(a[0], a[5])
		if err != nil || binding.ObservationStoreContext != a[6] || binding.Queue != a[1] || binding.Image != a[7] {
			return errors.New("workflow queue/store/image changed")
		}
		raw, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 20<<10+1))
		if err != nil || len(raw) > 20<<10 || !utf8.Valid(raw) {
			return errors.New("bounded goal request required")
		}
		var in workflowAuthor
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF || in.Protocol != workflowProtocol {
			return errors.New("invalid workflow request")
		}
		req := core.MainPrepareRequest{Protocol: core.MainProtocol, ProjectID: a[2], OperationID: a[3], Mode: in.Mode, Prompt: in.Goal, ExpectedRevision: in.ExpectedRevision}
		saved, savedErr := core.InspectProjectMain(dir, a[2], a[3])
		switch in.Mode {
		case "create", "adopt":
			if in.Goal != "" {
				return errors.New("setup never starts a goal")
			}
			profiles := map[string]string{}
			for _, s := range r.Selections {
				if s.Role == "main" && s.ID == in.Main || s.Role == "implementation" && s.ID == in.Implementation || s.Role == "review" && s.ID == in.Review {
					profiles[s.Role] = s.Profile
				}
			}
			if len(profiles) != 3 {
				return errors.New("three explicit owner selections required")
			}
			branch := saved.MainBranch
			if savedErr != nil {
				var e error
				branch, e = core.OperatorRepository(r.Folder)
				if e != nil {
					return e
				}
			}
			req.Repo, req.MainBranch, req.Label, req.Profile = r.Folder, branch, in.Label, profiles["main"]
			role, _ := json.Marshal(core.MainRoleSelection{Protocol: core.MainRoleProtocol, ImplementationProfile: profiles["implementation"], ReviewProfile: profiles["review"]})
			req.Role = string(role)
		case "send", "reopen":
			if in.Label != "" || in.Main != "" || in.Implementation != "" || in.Review != "" {
				return errors.New("reuse exact SQL-selected Main")
			}
			if in.Mode == "send" {
				if strings.TrimSpace(in.Goal) == "" || len(in.Goal) > 14<<10 {
					return errors.New("bounded nonempty goal required")
				}
				req.Prompt = "OCQ retained goal operation: " + a[3] + ". integration_authorized=false: deliver a handoff, never integrate under this send. For EACH prepared Work request, before confirming it, record exact goal lineage using project work link-goal " + a[2] + " REQUEST " + a[3] + ". Keep all stage transitions under this goal; do not infer completion from this instruction.\n\n" + in.Goal
			}
		default:
			return errors.New("unsupported operator operation")
		}
		body, _ := json.Marshal(req)
		// Receipt-first: retained operations recover before live readiness checks.
		if savedErr == nil {
			out, e := core.PrepareProjectMain(dir, body)
			if e != nil {
				return e
			}
			return writeWork(cmd, out)
		}
		if _, err = r.ready(cmd.Context(), dir); err != nil {
			return err
		}
		if req.Profile != "" {
			var selection core.MainRoleSelection
			_ = json.Unmarshal([]byte(req.Role), &selection)
			if err = core.VerifyOperatorProfiles(r.Folder, req.Profile, selection.ImplementationProfile, selection.ReviewProfile); err != nil {
				return err
			}
		}
		out, err := core.PrepareProjectMain(dir, body)
		if err != nil {
			return err
		}
		return writeWork(cmd, out)
	}}
	goal := &cobra.Command{Use: "workflow-goal SPACE QUEUE PROJECT OPERATION", Args: cobra.ExactArgs(4), RunE: func(cmd *cobra.Command, a []string) error {
		dir, err := existingBridgeStateDir()
		if err != nil {
			return err
		}
		r, err := core.OpenReader(dir)
		if err != nil {
			return err
		}
		queue, _, err := r.WorkQueueContext()
		r.Close()
		if err != nil || queue != a[1] {
			return errors.New("goal queue changed")
		}
		out, err := core.InspectProjectGoal(dir, a[2], a[3])
		if err != nil {
			return err
		}
		return writeWork(cmd, out)
	}}
	return []*cobra.Command{contextCommand, author, goal}
}
