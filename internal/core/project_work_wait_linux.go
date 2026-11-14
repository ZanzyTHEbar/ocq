package core

import (
	"context"
	"errors"
	"time"
)

const WorkWaitProtocol = "ocq-work-wait-v1"

type WorkWaitOutput struct {
	Protocol      string     `json:"protocol"`
	State         string     `json:"state"`
	Reason        string     `json:"reason"`
	QueueIdentity string     `json:"queue_identity"`
	StoreContext  string     `json:"store_context"`
	Work          WorkStatus `json:"work"`
	Consistency   string     `json:"consistency"`
}

// WaitProjectWork is read-only, existing-store only. One ordinary bounded tool
// call reuses ObserveWait's watch-before-check/expiry verification, never a
// dispatcher or an API observer. An unchanged lease does not reread projections.
func WaitProjectWork(ctx context.Context, dir, project, id string, seconds int) (WorkWaitOutput, error) {
	out := WorkWaitOutput{Protocol: WorkWaitProtocol, State: "pending", Consistency: "stored_sql_and_filesystem_separate_nonatomic"}
	if !isValidProjectID(project) || !workID(id) || seconds < 1 || seconds > 20 {
		return out, errors.New("exact work pointer and 1..20 second timeout required")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	defer cancel()
	var pinned WorkStatus
	for {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) && pinned.IntentSHA256 != "" {
			return out, nil
		}
		head, err := ObserveHead(ctx, dir)
		if err != nil {
			return out, err
		}
		if head.Status != "ready" {
			return out, errors.New("work wait requires available existing schema6 observation store")
		}
		if out.QueueIdentity != "" && (head.QueueIdentity != out.QueueIdentity || head.StoreContext != out.StoreContext) {
			return out, errors.New("work wait queue/store changed")
		}
		out.QueueIdentity, out.StoreContext = head.QueueIdentity, head.StoreContext
		journal, err := ProjectWorkJournalObservation(dir, project)
		if err != nil {
			return out, err
		}
		status, err := InspectProjectWork(dir, project, id)
		if err != nil {
			return out, err
		}
		if pinned.IntentSHA256 != "" && (pinned.IntentSHA256 != status.IntentSHA256 || pinned.ParentRequestID != status.ParentRequestID || pinned.Kind != status.Kind || pinned.BindingRevision != status.BindingRevision || pinned.Receipt != nil && (status.Receipt == nil || !jsonEqual(pinned.Receipt, status.Receipt)) || pinned.Job != nil && (status.Job == nil || pinned.Job.JobID != status.Job.JobID || pinned.Job.Sequence != status.Job.Sequence)) {
			return out, errors.New("work wait exact intent/receipt/lineage changed")
		}
		pinned = status
		out.Work = status
		verified, err := ObserveHead(ctx, dir)
		if err != nil {
			return out, err
		}
		if verified.Status != "ready" || verified.QueueIdentity != out.QueueIdentity || verified.StoreContext != out.StoreContext {
			return out, errors.New("work wait store changed across exact projection")
		}
		out.State, out.Reason = workWaitState(status)
		if out.State != "pending" {
			return out, nil
		}
		remaining := seconds
		if deadline, ok := ctx.Deadline(); ok {
			remaining = int((time.Until(deadline) + time.Second - 1) / time.Second)
		}
		if remaining < 1 {
			return out, nil
		}
		wait, err := ObserveWait(ctx, dir, ObservationWaitRequest{Protocol: ObservationProtocol, QueueIdentity: head.QueueIdentity, StoreContext: head.StoreContext, Revision: head.Revision, WaitSeconds: remaining, Work: &journal.WorkObservationCursor})
		if err != nil {
			if ctx.Err() != nil && errors.Is(err, context.DeadlineExceeded) {
				return out, nil
			}
			return out, err
		}
		if wait.Status == "reset" || wait.Status == "unavailable" || wait.Status == "upgrade_required" {
			return out, errors.New("work wait observation unavailable/reset")
		}
		if !wait.DatabaseChanged && !wait.WorkChanged {
			return out, nil
		}
		// Only a changed lease causes another exact projection. No poll timer.
	}
}

func workWaitState(s WorkStatus) (string, string) {
	if s.State != "recorded_observed" {
		return "attention", "exact admission/publication not proven; never retry uncertain attempts"
	}
	if s.Kind == "capture" || s.Kind == "review_result" {
		if s.Result != nil {
			return "published", "exact association recorded; not execution/test/integration proof"
		}
		return "attention", "publication association unavailable"
	}
	if s.Job == nil {
		return "attention", "exact admitted job unavailable"
	}
	if s.Job.TerminalProven {
		return "terminal", "stored cooperative terminal execution proof; inspect outcome, not test validation"
	}
	switch s.Job.State {
	case "queued", "launching", "running":
		return "pending", "awaiting stored terminal proof; no dispatch or live API lookup"
	default:
		return "attention", "stored execution requires attention; unknown is not completion"
	}
}
