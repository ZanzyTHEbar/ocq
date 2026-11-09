package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type supervisedIdentity struct {
	identity
	Unit       string `json:"unit"`
	Invocation string `json:"invocation"`
	Cgroup     string `json:"cgroup"`
}

// Proof launcher only: record the exact systemd invocation before exec. Systemd,
// not this helper, owns restarts and process lifecycle. No production supervisor.
func supervisedExec() {
	root, bin, state, unit := os.Args[2], os.Args[3], os.Args[4], os.Args[5]
	invocation := os.Getenv("INVOCATION_ID")
	check(len(invocation) == 32 && !strings.ContainsAny(invocation, "/\\"), "missing systemd invocation ID")
	cgroup, err := os.ReadFile("/proc/self/cgroup")
	must(err)
	group := strings.TrimSpace(strings.TrimPrefix(string(cgroup), "0::"))
	check(strings.HasSuffix(group, "/"+unit), "dispatcher not in exact owned unit cgroup")
	b, err := json.Marshal(supervisedIdentity{who(os.Getpid()), unit, invocation, group})
	must(err)
	save(filepath.Join(root, "supervised-"+invocation+".json"), b)
	must(syscall.Exec(bin, []string{bin, "--state-dir", state, "dispatch", "--slots", "1", "--commands-only"}, os.Environ()))
}

type supervisionProof struct {
	s      *scenario
	units  []string
	groups map[string]string
	marker string
}

func osCommand(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
	if err != nil {
		return b, fmt.Errorf("%s failed: %w: %s", args[0], err, b)
	}
	return b, nil
}

func unitProperties(unit string) (map[string]string, error) {
	b, err := osCommand("systemctl", "--user", "show", unit, "--property=LoadState,Description,Transient,FragmentPath,ExecStart,ExecStartPre,EnvironmentFiles,CollectMode,MainPID,ControlGroup,InvocationID,NRestarts,ActiveState,SubState,Result,KillMode,Restart,RestartUSec,TimeoutStopUSec,SendSIGKILL")
	if err != nil {
		return nil, err
	}
	properties := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			properties[key] = value
		}
	}
	return properties, nil
}

func (p *supervisionProof) inspect(unit string) map[string]string {
	properties, err := unitProperties(unit)
	must(err)
	if properties["LoadState"] == "not-found" {
		return properties
	}
	check(properties["Description"] == p.marker && properties["Transient"] == "yes" && strings.Contains(properties["ExecStart"], "supervised-exec") && strings.Contains(properties["ExecStart"], p.s.bin) && strings.Contains(properties["ExecStart"], p.s.root), "transient unit ownership mismatch; never stop/adopt by name alone")
	if group := properties["ControlGroup"]; group != "" {
		check(strings.HasSuffix(group, "/"+unit), "unit cgroup mismatch")
		p.groups[unit] = group
	}
	b, err := json.Marshal(properties)
	must(err)
	save(filepath.Join(p.s.root, unit+".receipt.json"), b)
	return properties
}

func (p *supervisionProof) start(phase string) string {
	unit := "ocq-proof-" + strings.TrimPrefix(filepath.Base(p.s.root), "ocq-acceptance-") + "-" + phase + ".service"
	before, err := unitProperties(unit)
	must(err)
	check(before["LoadState"] == "not-found", "proposed proof unit already exists; refuse reuse")
	envPath := filepath.Join(p.s.root, unit+".env")
	save(envPath, []byte("OCQ_BIN="+strconv.Quote(p.s.self)+"\nOCQ_STATE_DIR="+strconv.Quote(p.s.state)+"\n"))
	args := []string{"systemd-run", "--user", "--no-ask-password", "--collect", "--expand-environment=yes", "--unit=" + unit, "--description=" + p.marker,
		"--property=Type=exec", "--property=Restart=on-failure", "--property=RestartSec=5s",
		"--property=StartLimitIntervalSec=60s", "--property=StartLimitBurst=3",
		"--property=KillMode=mixed", "--property=KillSignal=SIGTERM", "--property=SendSIGKILL=yes",
		"--property=TimeoutStopSec=10s", "--property=UMask=0077", "--property=Environment=GOMAXPROCS=2",
		"--property=EnvironmentFile=" + envPath,
		"--property=ExecStartPre=/usr/bin/test -n ${OCQ_STATE_DIR}",
		"--working-directory=" + p.s.root, "--", "/usr/bin/env", "--", "${OCQ_BIN}", "supervised-exec", p.s.root, p.s.bin, "${OCQ_STATE_DIR}", unit}
	b, err := json.Marshal(args)
	must(err)
	save(filepath.Join(p.s.root, unit+".intent.json"), b)
	p.units = append(p.units, unit) // Recovery is exact-context even if CLI ack is lost.
	b, err = osCommand(args...)
	save(filepath.Join(p.s.root, unit+".creation.txt"), b)
	must(err) // Never retry an ambiguous transient-unit creation.
	p.s.wait("owned transient dispatcher ready", func() bool {
		properties := p.inspect(unit)
		id := properties["InvocationID"]
		_, err := os.Stat(filepath.Join(p.s.root, "supervised-"+id+".json"))
		return properties["ActiveState"] == "active" && properties["MainPID"] != "0" && err == nil
	})
	return unit
}

func (p *supervisionProof) mainIdentity(unit string) identity {
	properties := p.inspect(unit)
	pid, err := strconv.Atoi(properties["MainPID"])
	must(err)
	check(pid > 0, "unit has no main process")
	b, err := os.ReadFile(filepath.Join(p.s.root, "supervised-"+properties["InvocationID"]+".json"))
	must(err)
	var recorded supervisedIdentity
	must(json.Unmarshal(b, &recorded))
	check(recorded.Unit == unit && recorded.Cgroup == properties["ControlGroup"] && recorded.PID == pid && recorded.identity == who(pid), "unit/invocation/PID/start receipt mismatch")
	return recorded.identity
}

func (p *supervisionProof) stop(unit string, asynchronous bool) {
	properties := p.inspect(unit)
	if properties["LoadState"] == "not-found" {
		if !asynchronous {
			p.waitStopped(unit)
		}
		return
	}
	args := []string{"systemctl", "--user", "--no-ask-password"}
	if asynchronous {
		args = append(args, "--no-block")
	}
	_, err := osCommand(append(args, "stop", unit)...)
	must(err)
	if !asynchronous {
		p.waitStopped(unit)
	}
}

func (p *supervisionProof) waitStopped(unit string) {
	p.s.wait("exact unit stopped and cgroup empty", func() bool {
		properties := p.inspect(unit)
		if properties["LoadState"] != "not-found" && properties["ActiveState"] != "inactive" && properties["ActiveState"] != "failed" {
			return false
		}
		if group := p.groups[unit]; group != "" {
			b, err := os.ReadFile(filepath.Join("/sys/fs/cgroup", group, "cgroup.events"))
			if err == nil {
				return strings.Contains(string(b), "populated 0")
			}
			if !errors.Is(err, os.ErrNotExist) {
				must(err)
			}
		}
		return true
	})
}

func (p *supervisionProof) cleanup() {
	for _, release := range p.s.releases {
		save(release, []byte("owned cleanup release\n"))
	}
	var cleanupErr error
	for _, unit := range p.units {
		func() {
			defer func() {
				if failure := recover(); failure != nil {
					cleanupErr = errors.Join(cleanupErr, fmt.Errorf("owned unit %s cleanup: %v", unit, failure))
				}
			}()
			p.stop(unit, false)
		}()
	}
	paths, err := filepath.Glob(filepath.Join(p.s.root, "supervised-*.json"))
	must(err)
	for _, path := range paths {
		b, err := os.ReadFile(path)
		must(err)
		var recorded supervisedIdentity
		must(json.Unmarshal(b, &recorded))
		if live(recorded.identity) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("owned supervised dispatcher remains: %s %d/%d", recorded.Unit, recorded.PID, recorded.Start))
		}
	}
	must(cleanupErr)
	p.s.note("verified exact owned transient units stopped, cgroups empty/removed and all recorded dispatcher identities exited")
}

func (s *scenario) runSupervision() {
	_, err := osCommand("systemctl", "--user", "show", "--property=Version")
	must(err)
	p := &supervisionProof{s: s, groups: map[string]string{}, marker: "ocq isolated proof " + filepath.Base(s.root)}
	defer p.cleanup()
	submit := func(label string) job {
		release := filepath.Join(s.root, "release-"+label)
		s.releases = append(s.releases, release)
		b, err := s.cli("submit", "--key", label, "--cwd", s.root, "--memory-bytes", "1", "--", s.self, "finite-probe", s.root, label, release, "literal ; $HOME")
		must(err)
		var j job
		must(json.Unmarshal(b, &j))
		return j
	}
	crash := submit("supervised-crash")
	unit := p.start("restart")
	s.wait("owned foreground command running", func() bool { return s.get(crash.ID).State == "running" })
	s.wait("crash probe durable execution marker", func() bool {
		b, _ := os.ReadFile(filepath.Join(s.root, "events"))
		return string(b) == "supervised-crash\n"
	})
	jobBefore := s.get(crash.ID)
	mainBefore := p.mainIdentity(unit)
	check(live(jobBefore.Process), "known command disappeared before crash")
	childGroup, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", jobBefore.Process.PID))
	must(err)
	check(strings.Contains(string(childGroup), p.groups[unit]), "command is outside the exclusively owned unit cgroup")
	follower := submit("supervised-follower")
	save(filepath.Join(s.root, "release-supervised-follower"), []byte("ready\n"))
	s.wait("one-slot FIFO blocked", func() bool { return strings.Contains(s.get(follower.ID).Reason, "capacity blocked") })
	db, err := sql.Open("sqlite", "file:"+filepath.Join(s.state, "queue.db")+"?mode=ro&_pragma=busy_timeout(5000)")
	must(err)
	defer db.Close()
	snapshot := func() string {
		var value string
		must(db.QueryRow(`SELECT json_group_array(json_object('sequence',sequence,'key',key,'job',job_id,'canonical',hex(canonical),'digest',digest,'accepted',accepted_at)) FROM (SELECT * FROM receipts ORDER BY sequence)`).Scan(&value))
		return value
	}
	receipts := snapshot()
	save(filepath.Join(s.root, "receipts-before.json"), []byte(receipts))
	runningRecord, err := json.Marshal(jobBefore)
	must(err)
	save(filepath.Join(s.root, "crash-running.json"), runningRecord)
	check(p.mainIdentity(unit) == mainBefore, "dispatcher changed before deliberate crash")
	_, err = osCommand("systemctl", "--user", "kill", "--kill-whom=main", "--signal=SIGKILL", unit)
	must(err)
	s.wait("OS-supervised restart without rerun", func() bool {
		properties := p.inspect(unit)
		pid, _ := strconv.Atoi(properties["MainPID"])
		restarts, _ := strconv.Atoi(properties["NRestarts"])
		return pid > 0 && pid != mainBefore.PID && restarts >= 1 && s.get(crash.ID).State == "unknown"
	})
	mainAfter := p.mainIdentity(unit)
	check(mainAfter != mainBefore && snapshot() == receipts, "supervision changed identity/receipts incorrectly")
	s.wait("crashed invocation command exit independently established", func() bool { return !live(jobBefore.Process) })
	check(syscall.Kill(-jobBefore.Process.PID, 0) == syscall.ESRCH, "owned command process group remains")
	check(s.get(crash.ID).Process == jobBefore.Process && s.get(follower.ID).State == "queued", "restart released unknown ownership/FIFO head")
	s.wait("unknown still holds one-slot capacity", func() bool { return strings.Contains(s.get(follower.ID).Reason, "capacity blocked") })
	unknownRecord, err := json.Marshal(s.get(crash.ID))
	must(err)
	save(filepath.Join(s.root, "crash-unknown.json"), unknownRecord)
	blockedRecord, err := json.Marshal(s.get(follower.ID))
	must(err)
	save(filepath.Join(s.root, "follower-blocked.json"), blockedRecord)
	notices, _ := s.pullNotices("100")
	check(len(notices) == 0, "unknown command emitted completion")
	s.note("PASS real systemd automatic restart; exact in-flight command became unknown with held capacity/FIFO and no notification or rerun; owned prior child/group independently absent")
	p.stop(unit, false) // Operator resolution requires dispatcher lock exclusivity.
	output, err := s.cli("logs", crash.ID)
	must(err)
	check(strings.Contains(string(output), "hello supervised-crash"), "owned crashed command output unavailable for operator inspection")
	save(filepath.Join(s.root, "crash-output.txt"), output)
	resolved, err := s.cli("resolve", "--evidence", "owned unit invocation stopped; exact PID/start and process group absent; fixture marker/output inspected", crash.ID)
	must(err)
	save(filepath.Join(s.root, "operator-resolved.json"), resolved)
	check(s.get(crash.ID).State == "resolved", "safe explicit reconciliation did not resolve command")
	notices, initial := s.pullNotices("1")
	check(len(notices) == 1 && notices[0].JobID == crash.ID && notices[0].State == "resolved" && notices[0].Outcome == "operator_reconciled" && notices[0].Validation == "not_run", "operator resolution fabricated success")
	eventID := notices[0].ID
	save(filepath.Join(s.root, "pending-resolution-event.json"), initial)
	unit = p.start("drain")
	s.wait("queued follower executes once after operator reconciliation", func() bool { return s.get(follower.ID).State == "finished" })
	_, redelivered := s.pullNotices("1")
	check(string(initial) == string(redelivered), "supervised restart lost/changed pending notification")
	save(filepath.Join(s.root, "event-after-supervised-start.json"), redelivered)
	ack, err := s.cli("ack", eventID)
	must(err)
	save(filepath.Join(s.root, "ack.json"), ack)
	check(snapshot() == receipts, "restart/delivery changed immutable receipts")
	clean := submit("supervised-clean")
	s.wait("clean-stop foreground probe active", func() bool { return s.get(clean.ID).State == "running" })
	cleanIdentity := s.get(clean.ID).Process
	tail := submit("supervised-tail")
	beforeStop := p.mainIdentity(unit)
	p.stop(unit, true)
	s.wait("unit graceful stop pending", func() bool { return p.inspect(unit)["ActiveState"] == "deactivating" })
	time.Sleep(300 * time.Millisecond) // Let dispatcher consume TERM while its child remains held.
	check(live(beforeStop) && live(cleanIdentity) && s.get(tail.ID).State == "queued", "mixed stop killed foreground child early or admitted a follower")
	stopObservation, err := json.Marshal(map[string]any{"main": beforeStop, "child": cleanIdentity, "unit": p.inspect(unit), "tail": s.get(tail.ID)})
	must(err)
	save(filepath.Join(s.root, "graceful-stop-pending.json"), stopObservation)
	save(filepath.Join(s.root, "release-supervised-clean"), []byte("cooperative clean stop release\n"))
	p.waitStopped(unit)
	s.wait("graceful foreground result durable", func() bool { return s.get(clean.ID).State == "finished" && !live(cleanIdentity) })
	check(s.get(tail.ID).State == "queued", "clean stop admitted queued work")
	notices, _ = s.pullNotices("100")
	for _, n := range notices {
		check(n.ID != eventID, "acknowledged event redelivered after clean stop")
	}
	marker, err := os.ReadFile(filepath.Join(s.root, "events"))
	must(err)
	check(string(marker) == "supervised-crash\nsupervised-follower\nsupervised-clean\n", "supervision duplicated, skipped or reordered fixture execution")
	_, err = s.cli("cancel", tail.ID)
	must(err)
	s.note("PASS follower executes once only after safe resolution; outbox redelivery/ack preserved; mixed SIGTERM drains child without new admission, clean stop durable; all fixture commands accounted")
}
