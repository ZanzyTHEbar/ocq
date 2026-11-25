package core

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"syscall"
	"unicode"
)

const GoalProtocol = "ocq-goal-lineage-v1"

type GoalWorkLink struct {
	Protocol    string `json:"protocol"`
	ProjectID   string `json:"project_id"`
	OperationID string `json:"operation_id"`
	PromptID    string `json:"prompt_id"`
	MainIntent  string `json:"main_intent_sha256"`
	RequestID   string `json:"request_id"`
	WorkIntent  string `json:"work_intent_sha256"`
}

// LinkProjectGoalWork records explicit Main-selected lineage, not inferred
// causality from prose, timestamps or latest-job selection. Old receipts stay
// byte-for-byte unchanged. A request can belong to only one exact Main input.
func LinkProjectGoalWork(dir, project, operation, request string) (GoalWorkLink, error) {
	if !isValidProjectID(project) || !workID(operation) || !workID(request) {
		return GoalWorkLink{}, errors.New("exact goal/work IDs required")
	}
	j, err := openLocalJournal(dir, workJournal(project))
	if err != nil {
		return GoalWorkLink{}, err
	}
	defer j.close()
	if err = j.acquire(); err != nil {
		return GoalWorkLink{}, err
	}
	r, err := OpenReader(dir)
	if err != nil {
		return GoalWorkLink{}, err
	}
	defer r.Close()
	rec, in, err := mainLoad(r, j.dir, project, operation)
	if err != nil || in.Mode != "send" || rec.PromptID == "" {
		return GoalWorkLink{}, errors.New("exact retained Main goal required")
	}
	work, _, _, err := workLoad(r, j.dir, project, request)
	if err != nil {
		return GoalWorkLink{}, err
	}
	if _, e := workRead(j.dir, request+".task.json"); !errors.Is(e, syscall.ENOENT) {
		return GoalWorkLink{}, errors.New("standalone tasks do not belong to a Main intake prompt")
	}
	link := GoalWorkLink{GoalProtocol, project, operation, rec.PromptID, rec.Digest, request, work.Digest}
	name := ".goal-" + request + ".json"
	if raw, e := workRead(j.dir, name); e == nil {
		var saved GoalWorkLink
		if json.Unmarshal(raw, &saved) != nil || saved != link {
			return link, errors.New("work already linked to different goal")
		}
		return saved, nil
	} else if !errors.Is(e, syscall.ENOENT) {
		return link, e
	}
	selected, _, _, err := selectedMain(r, j.dir, project)
	if err != nil || selected.SetupID != rec.SetupID {
		return link, errors.New("goal is not owned by selected Main")
	}
	if _, attempted := mainAttempt(j.dir, operation, "prompt"); !attempted {
		return link, errors.New("goal has not been sent")
	}
	if err = nativeJournalRoom(j.dir, 1); err != nil {
		return link, err
	}
	return link, j.create(name, link)
}

type GoalView struct {
	Protocol              string         `json:"protocol"`
	Main                  MainStatus     `json:"main"`
	Links                 []GoalWorkLink `json:"links"`
	Work                  []WorkStatus   `json:"work"`
	IntegrationAuthorized bool           `json:"integration_authorized"`
	Title                 string         `json:"title,omitempty"`
}

func InspectProjectGoal(dir, project, operation string) (GoalView, error) {
	out := GoalView{Protocol: GoalProtocol, Links: []GoalWorkLink{}, Work: []WorkStatus{}}
	main, err := InspectProjectMain(dir, project, operation)
	if err != nil || main.Mode != "send" {
		return out, errors.New("exact retained Main goal required")
	}
	out.Main = main
	r, err := OpenReader(dir)
	if err != nil {
		return out, err
	}
	defer r.Close()
	j, err := workReadDir(r, project)
	if err != nil {
		return out, err
	}
	defer j.Close()
	rec, input, err := mainLoad(r, j, project, operation)
	if err != nil || rec.Digest != main.IntentSHA256 {
		return out, errors.New("goal title input identity changed")
	}
	out.Title = goalTitle(input.Prompt, operation)
	work, err := ListProjectWork(dir, project)
	if err != nil {
		return out, err
	}
	for _, w := range work.Items {
		raw, e := workRead(j, ".goal-"+w.RequestID+".json")
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return out, e
		}
		var link GoalWorkLink
		if json.Unmarshal(raw, &link) != nil || link.Protocol != GoalProtocol || link.ProjectID != project ||
			link.RequestID != w.RequestID || link.WorkIntent != w.IntentSHA256 {
			return out, errors.New("goal lineage integrity mismatch")
		}
		if link.OperationID != operation {
			continue
		}
		if link.MainIntent != main.IntentSHA256 || link.PromptID != main.PromptID {
			return out, errors.New("goal input identity changed")
		}
		out.Links = append(out.Links, link)
		out.Work = append(out.Work, w)
	}
	return out, workOutputBound(out)
}

func goalTitle(prompt, operation string) string {
	// Workflow-authored prompts put a transport preamble before the actual goal.
	if strings.HasPrefix(prompt, "OCQ retained goal operation: "+operation+". ") {
		if _, goal, ok := strings.Cut(prompt, "\n\n"); ok {
			prompt = goal
		} else {
			return ""
		}
	}
	line, _, _ := strings.Cut(strings.TrimSpace(prompt), "\n")
	title := []rune(strings.Join(strings.FieldsFunc(line, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}), " "))
	if len(title) > 72 {
		return string(title[:71]) + "…"
	}
	return string(title)
}
