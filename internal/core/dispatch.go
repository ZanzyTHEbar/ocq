package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ZanzyTHEbar/faults-go"

	"ocq/internal/core/dbquery"
)

type completion struct {
	id        string
	process   Process
	code      int
	err       error
	outputErr error
}

func (s *store) dispatch(ctx context.Context, slots int, floor int64, commandsOnly bool, onReady func()) error {
	lock, err := s.lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	boot, err := bootID()
	if err != nil {
		return err
	}
	start, err := startTicks(os.Getpid())
	if err != nil {
		return err
	}
	identity, err := json.Marshal(Process{PID: os.Getpid(), Start: start, Boot: boot})
	if err != nil {
		return err
	}
	if err := lock.Truncate(0); err != nil {
		return err
	}
	if _, err := lock.Write(identity); err != nil {
		return err
	}
	if err := errors.Join(lock.Sync(), syncDir(s.dir)); err != nil {
		return err
	}
	if err := s.write(func(c *sql.Conn) error {
		return dbquery.New(c).RecoverInterruptedJobs(context.Background())
	}); err != nil {
		return err
	}
	stop := ctx.Done()
	done := make(chan completion, slots)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	active := 0 // Wait-owned direct children of THIS dispatcher, not recovered jobs.
	stopping := false
	var fatal error
	lastAgentPoll := time.Time{}
	if onReady != nil {
		onReady()
	}
	for {
		if !stopping {
			select {
			case <-stop:
				stopping = true
			default:
			}
		}
		if stopping && active == 0 {
			return fatal
		}
		if !stopping {
			if !commandsOnly && time.Since(lastAgentPoll) >= 2*time.Second {
				lastAgentPoll = time.Now()
				if err := s.pollAgents(); err != nil {
					fatal = err
					stopping = true
				}
			}
		}
		if !stopping {
			started, err := s.admit(boot, slots, floor, done, commandsOnly)
			if started {
				active++
			}
			if err != nil {
				fatal = err
				stopping = true // No additional launches after a persistence error.
			}
		}
		select {
		case <-stop:
			stopping = true
		case result := <-done:
			active--
			if err := s.finish(result); err != nil {
				fatal = errors.Join(fatal, err)
				stopping = true
			}
		case <-tick.C:
		}
	}
}

func (s *store) prepareOutput(id string) (*os.File, *os.File, error) {
	base := filepath.Join(s.dir, "jobs")
	if err := privateDir(base); err != nil {
		return nil, nil, err
	}
	dir := filepath.Join(base, id)
	if err := privateDir(dir); err != nil {
		return nil, nil, err
	}
	out, err := privateFile(filepath.Join(dir, "stdout"), os.O_CREATE|os.O_WRONLY|os.O_APPEND)
	if err != nil {
		return nil, nil, err
	}
	errout, err := privateFile(filepath.Join(dir, "stderr"), os.O_CREATE|os.O_WRONLY|os.O_APPEND)
	if err == nil {
		err = errors.Join(out.Sync(), errout.Sync(), syncDir(dir))
	}
	if err != nil {
		out.Close()
		if errout != nil {
			errout.Close()
		}
		return nil, nil, err
	}
	return out, errout, nil
}

func (s *store) reason(id, reason string) error {
	return s.write(func(c *sql.Conn) error {
		return dbquery.New(c).SetQueuedReason(context.Background(), dbquery.SetQueuedReasonParams{Reason: reason, ID: id, Reason_2: reason})
	})
}

// Only this singleton goroutine admits and spawns. The next queued job is never
// considered until this head's durable intent and spawn handoff have completed.
func (s *store) admit(boot string, slots int, floor int64, done chan<- completion, commandsOnly bool) (bool, error) {
	row, err := dbquery.New(s.db).QueuedHeadJob(context.Background())
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	head, err := jobFromRow(dbquery.JobByIDRow(row))
	if err != nil {
		return false, err
	}
	if commandsOnly && head.agentRequest != nil {
		return false, s.reason(head.ID, "command-only dispatcher: agent at FIFO head; no admission or API polling; stop dispatch for explicit agent reconciliation")
	}
	available, memoryErr := memAvailable()
	if memoryErr != nil {
		return false, s.reason(head.ID, "memory observation unavailable; admission closed: "+memoryErr.Error())
	}
	// Artifact creation is idempotent and precedes the transaction. A crash here
	// leaves a queued job with recoverable empty files, never a launched command.
	out, errout, err := s.prepareOutput(head.ID)
	if err != nil {
		return false, s.reason(head.ID, "artifact setup blocked: "+err.Error())
	}
	handedOff := false
	defer func() {
		if !handedOff {
			out.Close()
			errout.Close()
		}
	}()
	admitted := false
	err = s.write(func(c *sql.Conn) error {
		first, err := dbquery.New(c).QueuedHead(context.Background())
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		if first != head.ID {
			return nil // Concurrent queued cancellation; examine the new head next tick.
		}
		requests, err := dbquery.New(c).InflightRequests(context.Background())
		if err != nil {
			return err
		}
		count, demand := 0, int64(0)
		for _, data := range requests {
			command, agent, err := storedRequest(data)
			if err != nil {
				return err
			}
			need := int64(0)
			if agent != nil {
				need = agent.MemoryBytes
			} else {
				need = command.MemoryBytes
			}
			count++
			if need > (1<<63-1)-demand {
				demand = 1<<63 - 1 // Saturate: corrupted/extreme demands cannot open admission.
			} else {
				demand += need
			}
		}
		reason := ""
		if count >= slots {
			reason = fmt.Sprintf("capacity blocked: %d launching/running/unknown, %d slots", count, slots)
		} else if available < floor || available-floor < demand || available-floor-demand < head.memoryDemand() {
			reason = fmt.Sprintf("memory blocked: MemAvailable=%d floor=%d inflight_declared=%d head_need=%d", available, floor, demand, head.memoryDemand())
		}
		if reason != "" {
			return dbquery.New(c).SetJobReason(context.Background(), dbquery.SetJobReasonParams{Reason: reason, ID: head.ID})
		}
		err = dbquery.New(c).RecordLaunchIntent(context.Background(), dbquery.RecordLaunchIntentParams{Boot: boot, Stdout: out.Name(), Stderr: errout.Name(), ID: head.ID})
		admitted = err == nil
		return err
	})
	if err != nil || !admitted {
		return false, err
	}
	if head.agentRequest != nil {
		if err := s.launchAgent(head); err != nil {
			unknownErr := s.agentUnknown(head.ID, "agent provisioning/persistence interrupted; inspect durable operation intents; never retry external effects", false)
			return false, errors.Join(err, unknownErr)
		}
		return false, nil // API/CLI process lifetime is not the agent's lifetime.
	}
	// There is deliberately NO retry across the commit/spawn gap.
	cmd := exec.Command(head.commandRequest.Argv[0], head.commandRequest.Argv[1:]...)
	cmd.Dir = head.commandRequest.Cwd
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "OCQ_JOB_ID=" + head.ID, "OCQ_JOB_DIR=" + filepath.Dir(out.Name())}
	cmd.Stdout, cmd.Stderr = out, errout
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return false, s.write(func(c *sql.Conn) error {
			return dbquery.New(c).MarkLaunchFailure(context.Background(), dbquery.MarkLaunchFailureParams{Reason: err.Error(), ID: head.ID})
		})
	}
	p := Process{PID: cmd.Process.Pid, Boot: boot}
	p.Start, err = startTicks(p.PID)
	identityErr := err
	handedOff = true
	go func() {
		waitError := cmd.Wait()
		outputErr := errors.Join(out.Sync(), errout.Sync(), out.Close(), errout.Close())
		done <- completion{id: head.ID, process: p, code: cmd.ProcessState.ExitCode(), err: waitError, outputErr: outputErr}
	}()
	err = s.write(func(c *sql.Conn) error {
		state, reason := "running", "direct command running; validation not run"
		if identityErr != nil {
			state, reason = "unknown", "process identity unavailable; ownership retained: "+identityErr.Error()
		}
		if p.Start > 1<<63-1 {
			return diagnostic(faults.CodeInvariantFailed, "process start ticks exceed SQLite integer range")
		}
		return dbquery.New(c).RecordProcessIdentity(context.Background(), dbquery.RecordProcessIdentityParams{State: state, Reason: reason, Pid: int64(p.PID), StartTicks: int64(p.Start), Boot: p.Boot, ID: head.ID})
	})
	return true, errors.Join(err, identityErr)
}

func (s *store) finish(result completion) error {
	state, outcome := "finished", "exited"
	reason := "direct command exited; not validation or proof that escaped descendants stopped"
	if err := safeToResolve(result.process); err != nil {
		state, outcome, reason = "unknown", "uncertain", err.Error()
	}
	if result.outputErr != nil {
		state, outcome, reason = "unknown", "uncertain", "output durability failed: "+result.outputErr.Error()
	}
	if result.err != nil {
		reason += "; " + result.err.Error()
	}
	return s.write(func(c *sql.Conn) error {
		return dbquery.New(c).FinishRunningJob(context.Background(), dbquery.FinishRunningJobParams{State: state, Outcome: outcome, Reason: reason, ExitCode: sql.NullInt64{Int64: int64(result.code), Valid: true}, ID: result.id})
	})
}
