package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"ocq/internal/core/dbquery"
)

func (s *store) checkAutomaticTaskHandoff(ctx context.Context, project string) error {
	ready, err := mainHandledSchemaReady(s.db)
	if err != nil || !ready {
		return errors.New("automatic task handoff requires explicitly installed schema 8")
	}
	if err = validateMainHandledSchema(ctx, s.db); err != nil {
		return err
	}
	if _, err = s.currentProjectBinding(ctx, s.db, project); err != nil {
		return errors.New("automatic task handoff requires an existing exact project binding")
	}
	return nil
}

// A single bounded adapter pass uses the existing dispatcher lifetime. The
// cursor is only fair traversal of the SQLite projection, not persisted queue
// state. No transaction remains open while the adapter talks to OpenCode.
func (s *store) advanceTaskHandoffs(ctx context.Context, project string, slots int, floor int64) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	r := &Reader{store: s}
	var cursor int64
	reported := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		var request, worker string
		var h taskHandoffRow
		err := s.readSchema5(func(q sqlQueryer) error {
			err := q.QueryRowContext(ctx, `SELECT h.request_id,o.session_id FROM project_task_handoff_status h
 JOIN agent_ownership o ON o.job_id=h.job_id
 WHERE h.project_id=? AND h.inbox_sequence>? AND h.state IN ('pending_main','handled','dead_letter')
 ORDER BY h.inbox_sequence LIMIT 1`, project, cursor).Scan(&request, &worker)
			if err != nil {
				return err
			}
			h, err = taskHandoffFrom(ctx, q, project, request)
			return err
		})
		if errors.Is(err, sql.ErrNoRows) {
			cursor = 0
			continue
		}
		if err != nil {
			if !reported && ctx.Err() == nil {
				fmt.Fprintln(os.Stderr, "automatic task handoff could not read the stored projection; no native effects")
				reported = true
			}
			continue
		}
		reported = false
		cursor = h.sequence
		pass, cancel := context.WithTimeout(ctx, 25*time.Second)
		advanceTaskWake(pass, r, project, h, h.State == "pending_main" && s.taskWakeFits(pass, slots, floor))
		cancel()
		if h.State == "handled" && ctx.Err() == nil && validDeletionTarget(project, worker, h.RequestID) {
			archive, cancelArchive := context.WithTimeout(ctx, 25*time.Second)
			_, _ = runTaskArchive(archive, s.dir, archiveDisplay(project, h.RequestID, worker), "", false, true)
			cancelArchive()
		}
	}
}

// Main is not an OCQ job and we do not reserve or release a slot for it. Do
// not initiate a new turn when the observed job budget is already exhausted.
// This observation is not a reservation or an execution limit.
func (s *store) taskWakeFits(ctx context.Context, slots int, floor int64) bool {
	left, err := memAvailable()
	if err != nil || left <= floor {
		return false
	}
	left -= floor
	inflight, err := dbquery.New(s.db).InflightRequests(ctx)
	if err != nil || len(inflight) >= slots {
		return false
	}
	for _, raw := range inflight {
		command, agent, err := storedRequest(raw)
		if err != nil {
			return false
		}
		if agent != nil {
			left -= agent.MemoryBytes
		} else {
			left -= command.MemoryBytes
		}
		if left <= 0 {
			return false
		}
	}
	return true
}
