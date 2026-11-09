// Real CLI/SQLite/process acceptance scenario; also supplies its harmless probe.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

type identity struct {
	PID   int    `json:"pid"`
	Start uint64 `json:"start_ticks"`
	Boot  string `json:"boot_id"`
}
type job struct {
	ID         string   `json:"id"`
	Sequence   int64    `json:"sequence"`
	Digest     string   `json:"digest"`
	State      string   `json:"state"`
	Reason     string   `json:"reason"`
	Process    identity `json:"process"`
	Exit       *int     `json:"exit_code"`
	Validation string   `json:"validation"`
}
type owned struct {
	identity
	cmd  *exec.Cmd
	done chan error
}
type scenario struct {
	ctx                    context.Context
	bin, self, root, state string
	processes              []*owned
	releases               []string
	log                    *os.File
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func check(ok bool, text string) {
	if !ok {
		panic(text)
	}
}
func save(path string, data []byte) {
	f, err := os.CreateTemp(filepath.Dir(path), ".write-")
	must(err)
	defer os.Remove(f.Name())
	must(errors.Join(func() error { _, err := f.Write(data); return err }(), f.Sync(), f.Close()))
	must(os.Rename(f.Name(), path))
	dir, err := os.Open(filepath.Dir(path))
	must(err)
	must(errors.Join(dir.Sync(), dir.Close()))
}
func who(pid int) identity {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	must(err)
	f := strings.Fields(string(b)[strings.LastIndex(string(b), ") ")+2:])
	start, err := strconv.ParseUint(f[19], 10, 64)
	must(err)
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	must(err)
	return identity{pid, start, strings.TrimSpace(string(boot))}
}
func live(p identity) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", p.PID))
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	must(err)
	f := strings.Fields(string(b)[strings.LastIndex(string(b), ") ")+2:])
	start, err := strconv.ParseUint(f[19], 10, 64)
	must(err)
	return start == p.Start // Zombies still count until reaped; do not hide ownership.
}
func (s *scenario) note(text string) {
	fmt.Fprintln(s.log, time.Now().UTC().Format(time.RFC3339Nano), text)
	must(s.log.Sync())
}
func (s *scenario) cli(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, s.bin, append([]string{"--state-dir", s.state}, args...)...)
	var stderr strings.Builder
	c.Stderr = &stderr
	b, err := c.Output()
	if err != nil {
		return b, fmt.Errorf("%v: %w: %s", args, err, stderr.String())
	}
	return b, nil
}
func (s *scenario) get(id string) job {
	b, err := s.cli("status", id)
	must(err)
	var j job
	must(json.Unmarshal(b, &j))
	return j
}
func (s *scenario) wait(text string, fn func() bool) {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		select {
		case <-s.ctx.Done():
			panic(s.ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	panic("timeout: " + text)
}
func (s *scenario) submit(key, label, memory string) job {
	release := filepath.Join(s.root, "release-"+label)
	s.releases = append(s.releases, release)
	b, err := s.cli("submit", "--key", key, "--cwd", s.root, "--memory-bytes", memory, "--", s.self, "probe", s.root, label, release, "literal ; $HOME")
	must(err)
	var j job
	must(json.Unmarshal(b, &j))
	return j
}
func (s *scenario) start(extra ...string) *owned {
	path := filepath.Join(s.root, fmt.Sprintf("dispatcher-%d.identity", len(s.processes)))
	log, err := os.OpenFile(path+".log", os.O_CREATE|os.O_WRONLY, 0600)
	must(err)
	// Wrapper records identity and fsyncs it BEFORE exec of the owned dispatcher.
	args := append([]string{"owned-exec", path, s.bin, "--state-dir", s.state, "dispatch"}, extra...)
	c := exec.Command(s.self, args...)
	c.Stdout, c.Stderr = log, log
	must(c.Start())
	p := &owned{identity: who(c.Process.Pid), cmd: c, done: make(chan error, 1)}
	s.processes = append(s.processes, p)
	go func() { p.done <- c.Wait(); log.Close() }()
	s.note(fmt.Sprintf("owned dispatcher pid=%d start=%d boot=%s", p.PID, p.Start, p.Boot))
	return p
}
func (s *scenario) signal(p *owned, sig syscall.Signal) {
	if live(p.identity) {
		check(who(p.PID) == p.identity, "ownership changed")
		must(syscall.Kill(p.PID, sig))
	}
}
func (s *scenario) stop(p *owned, sig syscall.Signal) {
	s.signal(p, sig)
	select {
	case err := <-p.done:
		if sig != syscall.SIGKILL {
			must(err)
		}
	case <-time.After(20 * time.Second):
		panic("dispatcher did not exit")
	}
	check(!live(p.identity), "dispatcher still exists")
	s.note(fmt.Sprintf("verified exit pid=%d start=%d", p.PID, p.Start))
}
func (s *scenario) cleanup() error {
	// Release cooperative probes first. No group/name-based or peer process kills.
	for _, path := range s.releases {
		save(path, []byte("release\n"))
	}
	for _, p := range s.processes {
		if live(p.identity) {
			s.signal(p, syscall.SIGTERM)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		// Probes write their own identity before any payload, so cleanup can find
		// them even when a CLI/state assertion failed before registering the PID.
		paths, err := filepath.Glob(filepath.Join(s.root, "probe-*.identity"))
		if err != nil {
			return err
		}
		for _, path := range paths {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var id identity
			if err := json.Unmarshal(b, &id); err != nil {
				return err
			}
			known := false
			for _, p := range s.processes {
				if p.identity == id {
					known = true
				}
			}
			if !known {
				s.processes = append(s.processes, &owned{identity: id})
			}
		}
		all := true
		for _, p := range s.processes {
			if live(p.identity) {
				all = false
			}
		}
		if all {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, p := range s.processes {
		if live(p.identity) {
			s.signal(p, syscall.SIGKILL)
		}
	}
	time.Sleep(200 * time.Millisecond)
	for _, p := range s.processes {
		if live(p.identity) {
			return fmt.Errorf("owned identity remains: %+v", p.identity)
		}
	}
	return nil
}

func (s *scenario) run() {
	// Four independent real submitters race one key. One loses its stdout ack.
	args := []string{"submit", "--key", "receipt", "--cwd", s.root, "--memory-bytes", "1", "--", s.self, "probe", s.root, "duplicate", filepath.Join(s.root, "release-duplicate"), "literal ; $HOME"}
	var wg sync.WaitGroup
	results := make(chan []byte, 3)
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := exec.Command(s.bin, append([]string{"--state-dir", s.state}, args...)...)
			var stderr strings.Builder
			c.Stderr = &stderr
			if i == 0 {
				errs <- c.Run()
				return
			} // acknowledgment deliberately discarded
			b, err := c.Output()
			if err != nil {
				err = fmt.Errorf("submit: %w: %s", err, stderr.String())
			}
			errs <- err
			results <- b
		}(i)
	}
	wg.Wait()
	close(errs)
	close(results)
	for err := range errs {
		must(err)
	}
	b, err := s.cli(args...)
	must(err)
	var receipt job
	must(json.Unmarshal(b, &receipt))
	for b := range results {
		var j job
		must(json.Unmarshal(b, &j))
		check(j.ID == receipt.ID && j.Sequence == receipt.Sequence && j.Digest == receipt.Digest, "duplicate receipt diverged")
	}
	changed := append([]string(nil), args...)
	changed[len(changed)-1] = "different argv"
	_, err = s.cli(changed...)
	check(err != nil && strings.Contains(err.Error(), "different canonical request"), "changed request accepted")
	_, err = s.cli("cancel", receipt.ID)
	must(err)
	b, err = s.cli(args...)
	must(err)
	var replay job
	must(json.Unmarshal(b, &replay))
	check(replay.ID == receipt.ID && replay.State == "cancelled", "cancel deleted receipt")
	s.note("PASS concurrent duplicate/lost-stdout acknowledgment, changed request rejected, receipt retained on cancellation")

	head := s.submit("oversized", "head", "1152921504606846976")
	follower := s.submit("follower", "follower", "1")
	save(filepath.Join(s.root, "release-follower"), []byte("ready\n"))
	d := s.start()
	s.wait("oversized head blocked", func() bool { return strings.Contains(s.get(head.ID).Reason, "memory blocked") })
	check(s.get(follower.ID).State == "queued", "follower backfilled oversized head")
	check(strings.Contains(s.get(follower.ID).Reason, "FIFO behind"), "missing FIFO explanation")
	_, err = os.Stat(filepath.Join(s.root, "events"))
	check(errors.Is(err, os.ErrNotExist), "a blocked job executed")
	_, err = s.cli("dispatch")
	check(err != nil && strings.Contains(err.Error(), "dispatcher lock held"), "competing dispatcher was not refused")
	_, err = s.cli("cancel", head.ID)
	must(err)
	s.wait("follower completion", func() bool { return s.get(follower.ID).State == "finished" })
	j := s.get(follower.ID)
	check(j.Exit != nil && *j.Exit == 0 && j.Validation == "not_run", "exit mistaken for validation")
	b, err = s.cli("logs", follower.ID)
	must(err)
	check(strings.Contains(string(b), "literal ; $HOME"), "argv was interpolated or output missing")
	s.stop(d, syscall.SIGTERM)
	s.note("PASS real MemAvailable oversized-head blocking with default two slots, singleton exclusion, queued cancellation releases follower")

	crash := s.submit("crash", "crash", "1")
	d = s.start()
	s.wait("marked harmless command running", func() bool { return s.get(crash.ID).State == "running" })
	j = s.get(crash.ID)
	check(j.Process.PID > 0 && live(j.Process), "missing running identity")
	probe := &owned{identity: j.Process}
	s.processes = append(s.processes, probe)
	s.note(fmt.Sprintf("owned probe pid=%d start=%d boot=%s", probe.PID, probe.Start, probe.Boot))
	s.wait("probe marker", func() bool {
		b, _ := os.ReadFile(filepath.Join(s.root, "events"))
		return strings.Contains(string(b), "crash\n")
	})
	s.stop(d, syscall.SIGKILL)
	d = s.start("--slots", "1")
	s.wait("restart uncertainty", func() bool { return s.get(crash.ID).State == "unknown" })
	check(s.get(crash.ID).Process == probe.identity && live(probe.identity), "recovery lost original ownership")
	pending, _ := s.pullNotices("100")
	for _, item := range pending {
		check(item.JobID != crash.ID, "unknown job emitted a completion notification")
	}
	capacity := s.submit("capacity", "capacity", "1")
	s.wait("unknown retains capacity", func() bool { return strings.Contains(s.get(capacity.ID).Reason, "capacity blocked") })
	check(s.get(capacity.ID).State == "queued", "unknown released its slot")
	retry := s.submit("crash", "crash", "1")
	check(retry.ID == crash.ID, "restart lost receipt")
	_, err = s.cli("cancel", crash.ID)
	check(err != nil, "unknown command was cancelled as queued")
	s.stop(d, syscall.SIGTERM)
	_, err = s.cli("cancel", capacity.ID)
	must(err)
	_, err = s.cli("resolve", "--evidence", "probe still live; must refuse", crash.ID)
	check(err != nil && strings.Contains(err.Error(), "still exists"), "live-process resolution was not refused")
	save(filepath.Join(s.root, "release-crash"), []byte("release\n"))
	s.wait("orphan probe exit", func() bool { return !live(probe.identity) })
	_, err = s.cli("resolve", "--evidence", "marked foreground probe exited; exact PID/start absent; marker and output inspected", crash.ID)
	must(err)
	check(s.get(crash.ID).State == "resolved" && s.get(crash.ID).Validation == "not_run", "resolution misrepresented validation")
	b, err = s.cli("logs", crash.ID)
	must(err)
	check(strings.Contains(string(b), "hello crash"), "crash output unavailable")
	events, err := os.ReadFile(filepath.Join(s.root, "events"))
	must(err)
	check(string(events) == "follower\ncrash\n", "unexpected duplicate or reordered execution: "+string(events))
	db, err := sql.Open("sqlite", "file:"+filepath.Join(s.state, "queue.db")+"?mode=ro&_pragma=busy_timeout(5000)")
	must(err)
	defer db.Close()
	var count int
	must(db.QueryRow("SELECT count(*) FROM receipts").Scan(&count))
	check(count == 5, "expected exactly five retained receipts")
	var mode string
	must(db.QueryRow("PRAGMA journal_mode").Scan(&mode))
	check(mode == "wal", "database is not WAL")
	var terminalNotifications int
	must(db.QueryRow(`SELECT count(*) FROM jobs j JOIN completion_notifications n ON n.job_id=j.id WHERE j.state IN ('finished','cancelled','resolved') AND n.state=j.state AND n.outcome=j.outcome AND n.validation=j.validation`).Scan(&terminalNotifications))
	check(terminalNotifications == count, "terminal command paths lack atomic completion snapshots")
	var resolvedState, resolvedOutcome, resolvedValidation string
	must(db.QueryRow("SELECT state,outcome,validation FROM completion_notifications WHERE job_id=?", crash.ID).Scan(&resolvedState, &resolvedOutcome, &resolvedValidation))
	check(resolvedState == "resolved" && resolvedOutcome == "operator_reconciled" && resolvedValidation == "not_run", "operator resolution fabricated success")
	s.note("PASS dispatcher SIGKILL/restart -> unknown, no duplicate execution, retained ownership/receipt/output, unsafe resolution refused, explicit dead-process resolution")
}

func main() {
	syscall.Umask(0077)
	if len(os.Args) > 1 && os.Args[1] == "supervised-exec" {
		supervisedExec()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "notification-consumer" {
		notificationConsumer()
		return
	}
	if len(os.Args) > 1 && (os.Args[1] == "probe" || os.Args[1] == "finite-probe") {
		root, label, release := os.Args[2], os.Args[3], os.Args[4]
		id, err := json.Marshal(who(os.Getpid()))
		must(err)
		save(filepath.Join(root, "probe-"+label+".identity"), id)
		f, err := os.OpenFile(filepath.Join(root, "events"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		must(err)
		_, err = fmt.Fprintln(f, label)
		must(err)
		must(f.Sync())
		must(f.Close())
		fmt.Printf("hello %s\n%s\n", label, os.Args[5])
		deadline := time.Now().Add(90 * time.Second)
		for {
			if _, err := os.Stat(release); err == nil {
				return
			}
			if os.Args[1] == "finite-probe" && time.Now().After(deadline) {
				os.Exit(7)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "owned-exec" {
		b, err := json.Marshal(who(os.Getpid()))
		must(err)
		save(os.Args[2], b)
		must(syscall.Exec(os.Args[3], os.Args[3:], []string{"PATH=/usr/bin:/bin", "LANG=C"}))
		return
	}
	notifications := len(os.Args) > 1 && os.Args[1] == "--notifications"
	supervision := len(os.Args) > 1 && os.Args[1] == "--supervision"
	project := len(os.Args) > 1 && os.Args[1] == "--project"
	check((!notifications && !supervision && !project && len(os.Args) == 2) || (notifications && len(os.Args) == 5) || (supervision && len(os.Args) == 3) || (project && len(os.Args) == 5), "usage: ocq-acceptance OCQ | --notifications OCQ LEGACY_SCHEMA1 LEGACY_SCHEMA2 | --supervision OCQ | --project OCQ LEGACY_SCHEMA1 LEGACY_SCHEMA2")
	binArg := os.Args[1]
	if notifications || supervision || project {
		binArg = os.Args[2]
	}
	bin, err := filepath.Abs(binArg)
	must(err)
	self, err := os.Executable()
	must(err)
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		must(err)
		base = filepath.Join(home, ".local", "state")
	}
	must(os.MkdirAll(base, 0700))
	root, err := os.MkdirTemp(base, "ocq-acceptance-")
	must(err)
	f, err := os.OpenFile(filepath.Join(root, "result.log"), os.O_CREATE|os.O_WRONLY, 0600)
	must(err)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	s := &scenario{ctx: ctx, bin: bin, self: self, root: root, state: filepath.Join(root, "state"), log: f}
	fmt.Println("Artifacts:", root)
	code := 0
	func() {
		defer func() {
			if p := recover(); p != nil {
				code = 1
				s.note(fmt.Sprintf("FAIL: %v", p))
			}
		}()
		if supervision {
			s.runSupervision()
		} else if notifications {
			s.runNotifications(os.Args[3], os.Args[4])
		} else if project {
			s.runProjectAcceptance(os.Args[3], os.Args[4])
		} else {
			s.run()
		}
	}()
	if err := s.cleanup(); err != nil {
		code = 1
		s.note("CLEANUP FAILED: " + err.Error())
	} else {
		s.note("verified all owned dispatcher/probe PID/start identities exited")
	}
	if code == 0 {
		label := "PASS command-core acceptance"
		if notifications {
			label = "PASS durable local completion notifications acceptance"
		}
		if supervision {
			label = "PASS isolated systemd user supervision acceptance"
		}
		if project {
			label = "PASS project control plane and terminal board acceptance"
		}
		s.note(label)
		fmt.Println(label)
	}
	must(f.Close())
	os.Exit(code)
}
