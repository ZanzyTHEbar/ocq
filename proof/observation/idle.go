package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"ocq/internal/core"
)

// Real foreground leases, including harmless filesystem hints near expiry.
// Touching this private DB's timestamps does not commit a SQLite revision. No
// collaborator is mocked and no timeout is converted to an accepted unchanged.
func (f *fixture) runIdleLeases() {
	f.cli(f.current, "list")
	prefix := []string{"--mod-space", "spObserve", "--mod-folder", f.root}
	b, e := f.command(f.bridge, append(prefix, "view-snapshot", string(raw(core.ObservationRequest{Protocol: core.ObservationProtocol, Selectors: []core.ObservationSelector{{Slot: "queue", Kind: "queue-jobs", Cursor: "0"}}})))...)
	must(e)
	var discovery struct {
		Status   string `json:"status"`
		Queue    string `json:"queue_identity"`
		Store    string `json:"store_context"`
		Bridge   string `json:"bridge_context"`
		Revision string `json:"revision"`
	}
	must(json.Unmarshal(b, &discovery))
	check(discovery.Status == "ready", "idle lease discovery failed")
	req := core.ObservationWaitRequest{Protocol: core.ObservationProtocol, QueueIdentity: discovery.Queue, StoreContext: discovery.Store, BridgeContext: discovery.Bridge, Revision: discovery.Revision}
	failed := 0
	for i := 0; i < 11; i++ {
		req.WaitSeconds = 1
		hints := i < 6
		cancelRead := i == 10
		dbPause := i == 8 || i == 9
		pastBudget := i == 9
		if i == 6 || i == 7 || cancelRead {
			req.WaitSeconds = 20
		}
		ctx, cancel := context.WithTimeout(f.ctx, 24*time.Second)
		cmd := exec.CommandContext(ctx, f.bridge, append(prefix, "watch-wait", string(raw(req)))...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "HOME=" + os.Getenv("HOME"), "OCQ_STATE_DIR=" + f.state}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		started := time.Now()
		must(cmd.Start())
		stat, statErr := os.ReadFile(fmt.Sprintf("/proc/%d/stat", cmd.Process.Pid))
		must(statErr)
		fields := strings.Fields(string(stat)[strings.LastIndex(string(stat), ") ")+2:])
		birth := map[string]any{"pid": cmd.Process.Pid, "start_ticks": fields[19], "lease_seconds": req.WaitSeconds, "hints": hints, "cancel": cancelRead, "database_pause": dbPause, "past_budget": pastBudget}
		must(os.WriteFile(filepath.Join(f.root, fmt.Sprintf("lease-%02d-birth.json", i)), raw(birth), 0600))
		actorCtx, stopActor := context.WithCancel(ctx)
		var actor sync.WaitGroup
		hintCount := 0
		var hintErr error
		lockAcquired := false
		if hints || cancelRead || dbPause {
			actor.Add(1)
			go func() {
				defer actor.Done()
				if cancelRead {
					select {
					case <-actorCtx.Done():
						return
					case <-time.After(200 * time.Millisecond):
					}
					_ = cmd.Process.Signal(syscall.SIGTERM)
					return
				}
				if dbPause {
					select {
					case <-actorCtx.Done():
						return
					case <-time.After(600 * time.Millisecond):
					}
					db, err := sql.Open("sqlite", "file:"+filepath.Join(f.state, "queue.db")+"?mode=rw&_pragma=busy_timeout(1000)&_pragma=synchronous(FULL)")
					if err != nil {
						hintErr = err
						return
					}
					defer db.Close()
					conn, err := db.Conn(actorCtx)
					if err != nil {
						hintErr = err
						return
					}
					defer conn.Close()
					if _, err = conn.ExecContext(actorCtx, "PRAGMA locking_mode=EXCLUSIVE"); err != nil {
						hintErr = err
						return
					}
					if _, err = conn.ExecContext(actorCtx, "BEGIN EXCLUSIVE"); err != nil {
						hintErr = err
						return
					}
					lockAcquired = true
					defer func() {
						cleanup, cancel := context.WithTimeout(f.ctx, time.Second)
						defer cancel()
						if _, err := conn.ExecContext(cleanup, "ROLLBACK"); err != nil {
							hintErr = err
						}
					}()
					now := time.Now()
					if hintErr = os.Chtimes(filepath.Join(f.state, "queue.db"), now, now); hintErr != nil {
						return
					}
					hintCount++
					hold := 250 * time.Millisecond
					if pastBudget {
						hold = time.Second
					}
					select {
					case <-actorCtx.Done():
					case <-time.After(hold):
					}
					return
				}
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-actorCtx.Done():
						return
					case now := <-ticker.C:
						if hintErr = os.Chtimes(filepath.Join(f.state, "queue.db"), now, now); hintErr != nil {
							return
						}
						hintCount++
					}
				}
			}()
		}
		err := cmd.Wait()
		elapsed := time.Since(started)
		stopActor()
		actor.Wait()
		cancel()
		must(os.WriteFile(filepath.Join(f.root, fmt.Sprintf("lease-%02d.stdout", i)), stdout.Bytes(), 0600))
		must(os.WriteFile(filepath.Join(f.root, fmt.Sprintf("lease-%02d.stderr", i)), stderr.Bytes(), 0600))
		var result struct {
			Status   string `json:"status"`
			Revision string `json:"revision"`
		}
		decodeErr := json.Unmarshal(stdout.Bytes(), &result)
		// Include only a small subprocess startup/scheduling margin, not the
		// native transport's larger timeout, in the foreground success assertion.
		bound := time.Duration(req.WaitSeconds)*time.Second + 250*time.Millisecond
		ok := err == nil && decodeErr == nil && result.Status == "unchanged" && result.Revision == req.Revision && elapsed < bound
		if cancelRead {
			ok = err != nil && result.Status != "unchanged" && elapsed < 5*time.Second
		}
		if pastBudget {
			ok = err != nil && result.Status != "unchanged" && elapsed < 5*time.Second
		}
		if dbPause && (!lockAcquired || hintErr != nil) {
			ok = false
		}
		if hints && (hintCount == 0 || hintErr != nil) {
			ok = false
		}
		if !ok {
			failed++
		}
		record := map[string]any{"birth": birth, "wait_returned": true, "exit_code": cmd.ProcessState.ExitCode(), "elapsed_ns": elapsed.Nanoseconds(), "status": result.Status, "revision": result.Revision, "stderr": stderr.String(), "accepted": ok, "successful_hints": hintCount, "database_lock_acquired": lockAcquired}
		must(os.WriteFile(filepath.Join(f.root, fmt.Sprintf("lease-%02d-result.json", i)), raw(record), 0600))
		fmt.Printf("lease=%d seconds=%d hints=%t cancel=%t elapsed=%s exit=%d status=%s stderr=%q\n", i, req.WaitSeconds, hints, cancelRead, elapsed, cmd.ProcessState.ExitCode(), result.Status, stderr.String())
		check(f.revision() == req.Revision, "filesystem hints changed the database revision")
	}
	check(failed == 0, fmt.Sprintf("%d real foreground leases failed", failed))
	fmt.Println("PASS real idle foreground leases, hint/expiry boundary, cancellation; every exact child reaped")
}
