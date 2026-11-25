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
	// A valid attempt is required to associate the explicit lineage, but
	// neither the attempt nor the sidecar proves send or handling.
	switch state, err := readMainPromptAttempt(j.dir, operation, rec); state {
	case mainAttemptAbsent:
		return link, errors.New("goal has no retained input attempt")
	case mainAttemptUntrusted:
		return link, errors.Join(err, errors.New("goal input attempt untrusted; outcome unknown"))
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
	out := newGoalView(MainStatus{}, MainPrepareRequest{})
	notGoal := errors.New("exact retained Main goal required")
	if !isValidProjectID(project) || !workID(operation) {
		return out, notGoal
	}
	r, err := OpenReader(dir)
	if err != nil {
		return out, notGoal
	}
	defer r.Close()
	j, err := workReadDir(r, project)
	if err != nil {
		return out, notGoal
	}
	defer j.Close()
	rec, input, err := mainLoad(r, j, project, operation)
	if err != nil || input.Mode != "send" {
		return out, notGoal
	}
	main, err := inspectMainIntent(r, j, rec, input, false, nil)
	if err != nil {
		return out, notGoal
	}
	out = newGoalView(main, input)
	work, err := ListProjectWork(dir, project)
	if err != nil {
		return out, err
	}
	lineage, err := readGoalLineage(j, project, work.Items)
	if err != nil {
		return out, err
	}
	err = out.addLineage(lineage[operation])
	return out, err
}

func newGoalView(main MainStatus, in MainPrepareRequest) GoalView {
	return GoalView{Protocol: GoalProtocol, Main: main, Links: []GoalWorkLink{}, Work: []WorkStatus{}, Title: goalTitle(in.Prompt, in.OperationID)}
}

type linkedGoalWork struct {
	link GoalWorkLink
	work *WorkStatus
}

// Each Work row supplies the exact request/intent identity and ordering. Read
// its journal link once, then group by the explicitly retained operation ID.
func readGoalLineage(j *os.File, project string, work []WorkStatus) (map[string][]linkedGoalWork, error) {
	lineage := make(map[string][]linkedGoalWork)
	for i := range work {
		w := &work[i]
		raw, e := workRead(j, ".goal-"+w.RequestID+".json")
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return nil, e
		}
		var link GoalWorkLink
		if json.Unmarshal(raw, &link) != nil || link.Protocol != GoalProtocol || link.ProjectID != project ||
			link.RequestID != w.RequestID || link.WorkIntent != w.IntentSHA256 {
			return nil, errors.New("goal lineage integrity mismatch")
		}
		lineage[link.OperationID] = append(lineage[link.OperationID], linkedGoalWork{link, w})
	}
	return lineage, nil
}

func (out *GoalView) addLineage(lineage []linkedGoalWork) error {
	for _, row := range lineage {
		if row.link.MainIntent != out.Main.IntentSHA256 || row.link.PromptID != out.Main.PromptID {
			return errors.New("goal input identity changed")
		}
		out.Links = append(out.Links, row.link)
		out.Work = append(out.Work, *row.work)
	}
	return workOutputBound(out)
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
