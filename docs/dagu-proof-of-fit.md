# Dagu v2.22.4: bounded proof-of-fit

**Result: shell lifecycle passed; do not adopt for coding-agent work as-is.**
This is an acceptance experiment, not an ocq runtime or a second scheduler.
The [reliability contract](reliability-contract.md) is unchanged. In particular,
strict FIFO admission includes head-of-line blocking; it is not relaxed to
"first runnable job." No replacement queue was built during this checkpoint.
**Approved follow-up:** the [architectural decision](architecture.md) now selects
a narrow Go + SQLite sole authority. This historical Dagu experiment and its
results remain intact; Dagu is not a runtime dependency of the command core.

## Reproduce

Prerequisites: Linux x86_64, Bash, `/proc`, curl, GNU coreutils, grep, sed, tar,
and ordinary shell utilities. No Go build, Python, JS/TS, global installation,
UI, coordinator, or agent runtime is needed. Run from this repository:

```sh
bash -n proof/dagu/run.sh
bash proof/dagu/run.sh
git diff --check
```

The runner downloads only missing release assets to ignored
`runtime/dagu-v2.22.4/`; existing assets are reused and reverified. It checks the
archive against the official `checksums.txt` **and** this observed pinned digest
before extracting/executing the binary:

```text
dagu_2.22.4_linux_amd64.tar.gz
f1b2133d8031580649d7cf31140b59f365fa03fd103da047894a471224657ee2
dagu version: 2.22.4
```

Assets: [official release](https://github.com/dagucloud/dagu/releases/tag/v2.22.4),
[manifest](https://github.com/dagucloud/dagu/releases/download/v2.22.4/checksums.txt),
[archive](https://github.com/dagucloud/dagu/releases/download/v2.22.4/dagu_2.22.4_linux_amd64.tar.gz).
Checksums were verified over HTTPS; separate Sigstore verification was not run.
The tag resolved to source commit `fd5b67913edc5349b92e7174ea92a112a236abbb`.
The official tag source archive was downloaded once for read-only inspection;
it is private/ignored, not vendored into this MIT project. Dagu has its own license.

Each invocation prints a unique persistent `runtime/proof.<random>/` evidence
directory. The binary is owner-executable (0700); evidence files are 0600 and
directories 0700. A clean environment and private HOME/DAGU_HOME prevent use of
global Dagu configuration or remote contexts. The copied config enables one
local queue at concurrency **1**, disables updates/coordinator, and sets scheduler
health port **0** (disabled, not an ephemeral public listener). Only `scheduler`
is started, never `server` or `start-all`.

The runner saves the pinned CLI help before using its flags. Its actual commands
are these (private paths and a clean environment are supplied by the runner):

```sh
dagu --context local scheduler --config "$run/config.yaml" --dags "$run/dags"
dagu --context local enqueue --config "$run/config.yaml" --run-id proof-1 \
  "$run/dags/job.yaml" -- NUMBER=1
dagu --context local status --config "$run/config.yaml" --run-id proof-1 proof-job
dagu --context local history --config "$run/config.yaml" --format json proof-job
```

Numbers 2 and 3 use corresponding IDs/parameters. Replay probes reuse `proof-1`
with `NUMBER=1` and `NUMBER=99`. This is deliberate harmless duplicate-submission
testing, not a retry of coding-agent side effects.

## Executed checks (2026-10-26, UTC)

The final full run ran from 11:21:41 through 11:22:23:

1. Started an empty owned scheduler; recorded PID/start ticks **before exec**.
   Waited for `Scheduler started`, then SIGSTOPed and verified its stopped state.
2. Enqueued three numbered harmless shell jobs using separate CLI processes.
   Each submitter exited zero; all three statuses were queued and no job ran.
3. SIGKILLed only that scheduler after validating its identity. Verified exit.
   Restarted with the same private state. Recovery acquired the stale scheduler
   lock after about **30 seconds**, without deleting or repairing runtime files.
4. Observed startup heartbeats for IDs 1, 2, 3 at 11:22:14, 11:22:17, 11:22:20.
   The exact shell event assertion was:

   ```text
   start 1
   end 1
   start 2
   end 2
   start 3
   end 3
   ```

5. All three statuses were `Succeeded`; real retained stdout contained
   `hello from job N`. Gracefully stopped/restarted the scheduler again. Status,
   output files, and history remained readable. The replay probes both failed
   with `DAG "proof-job" with ID "proof-1" already exists`; no extra job ran.
6. Gracefully retired the last scheduler. Final private-marker process scan was
   empty. The runner printed `PASS: three queued submitters, scheduler
   crash/restart, serial order, outcomes, output`.

`bash -n` passed. Three full lifecycle invocations passed during harness refinement;
the first lacked the additional terminal-restart/replay probes. A separate
interruption check launched the same runner, waited for its first scheduler to
start, then sent SIGTERM to the exact owned runner PID after checking start ticks.
Runner exit was **143**; its EXIT trap retired the scheduler and its final scan
was empty. This checks interruption cleanup, not an active agent's cancellation.
`git diff --check` passed. ShellCheck was unavailable; no broad upstream suite,
build, resource stress test, or live integration was run.

Evidence stays private: `version`, `help-*`, `scheduler-*.log`,
`scheduler-*.identity`, `identities`, `queued-*`, `outcome-*`, `retained-*`,
`history.json`, `output-*.paths`, `events`, `replay-*.log`, `cleanup`, `remaining`,
and `result`. No raw host diagnostics, process identities, or absolute host paths
are tracked. Cleanup validates PID/start identity and the unique run environment
marker before signaling; it never uses broad process-name kills. Catchable
failure/interrupt paths share that cleanup. SIGKILL of the harness itself or a
host crash cannot execute a shell trap; retained identities support inspection.

## Contract findings from the pinned source

All source links below refer to the exact release commit, not current main.

| Requirement | Evidence and limit |
| --- | --- |
| Durable acceptance before acknowledgment | [Intake](https://github.com/dagucloud/dagu/blob/fd5b67913edc5349b92e7174ea92a112a236abbb/internal/intake/queue.go#L68-L131) writes/closes the queued attempt before publishing the queue item. [Collection writes](https://github.com/dagucloud/dagu/blob/fd5b67913edc5349b92e7174ea92a112a236abbb/internal/persis/file/collection.go#L465-L485) use atomic files; [fileutil](https://github.com/dagucloud/dagu/blob/fd5b67913edc5349b92e7174ea92a112a236abbb/internal/cmn/fileutil/fileutil.go#L311-L402) syncs the temporary file and containing directory. [Status writer](https://github.com/dagucloud/dagu/blob/fd5b67913edc5349b92e7174ea92a112a236abbb/internal/persis/file/dagrun/writer.go#L147-L163) flushes and fsyncs. This is real fsync, not just buffered close. |
| Process crash versus power loss | Only scheduler process crash **after completed submissions** was exercised. No submitter crash mid-intake, disk-full/fsync failure, machine crash, or power loss was induced. Intake spans multiple records, not one transaction; interruption between attempt creation and queue publication needs reconciliation. New directory ancestry is created with [MkdirAll](https://github.com/dagucloud/dagu/blob/fd5b67913edc5349b92e7174ea92a112a236abbb/internal/persis/file/dagrun/dagrun.go#L140-L157); per-file/containing-directory fsync alone does not establish durability of every newly created ancestor. The local [CLI](https://github.com/dagucloud/dagu/blob/fd5b67913edc5349b92e7174ea92a112a236abbb/internal/cmd/enqueue.go#L127-L151) also permits queue publication after a status-close error, warning rather than failing. End-to-end durable acceptance remains unproven. |
| Lost acknowledgment / idempotency | The tested local CLI's `--run-id` protects against the observed sequential duplicate while the DAG/run history exists, including after restart. It rejects identical and changed requests alike; it does not return the original accepted result or compare a request digest. The lookup is scoped to DAG name plus run ID. A true lost acknowledgment was not induced, concurrent duplicate writers were not tested, and no atomic key/request association was established. The guard depends on retained history, not an independent permanent tombstone; default history retention is [30 days](https://github.com/dagucloud/dagu/blob/fd5b67913edc5349b92e7174ea92a112a236abbb/internal/ir/dag.go#L560-L583), and deletion can end this protection. No cleanup/retention expiry was tested. |
| Strict FIFO with blocked head | This sample proves serial dispatch/start/finish order only for three ready jobs. [Queue sorting](https://github.com/dagucloud/dagu/blob/fd5b67913edc5349b92e7174ea92a112a236abbb/internal/persis/store/queue_item.go#L130-L143) uses priority then wall-clock enqueue time/ID, not an observed global admission sequence. [Runnable selection](https://github.com/dagucloud/dagu/blob/fd5b67913edc5349b92e7174ea92a112a236abbb/internal/service/scheduler/queue_dispatcher.go#L1324-L1423) explicitly **continues past worker-ineligible items**. This is a source-level conflict with treating Dagu's general queue as strict head-blocking FIFO, not a claim that a local memory gate was tested. There is no memory/resource admission implementation in this proof. Do not emulate admission by waiting inside a job while retaining its slot. |
| Active-job restart reconciliation | Not exercised: scheduler crash occurred before any execution. [Dispatch](https://github.com/dagucloud/dagu/blob/fd5b67913edc5349b92e7174ea92a112a236abbb/internal/service/scheduler/queue_dispatcher.go#L714-L751) checks run liveness and queued status. [Zombie detection](https://github.com/dagucloud/dagu/blob/fd5b67913edc5349b92e7174ea92a112a236abbb/internal/service/scheduler/zombie_detector.go#L187-L292) waits for stale thresholds and checks PID/start identity. [Repair](https://github.com/dagucloud/dagu/blob/fd5b67913edc5349b92e7174ea92a112a236abbb/internal/runtime/stale_run.go#L20-L52) marks a stale active local run failed. This does not prove that external tools stopped or reconcile an OpenCode session's side effects. Never automatically retry uncertain agent execution. |
| Scheduler without listeners | [Health server](https://github.com/dagucloud/dagu/blob/fd5b67913edc5349b92e7174ea92a112a236abbb/internal/service/healthcheck/server.go#L72-L80) disables the listener at port 0; all tested scheduler logs confirmed this. No UI/coordinator/worker was started. |

## Decision and remaining boundary

Follow-up clarification: worker-ineligible skipping concerns distributed runs;
local runs pass that eligibility check. The approved [architectural decision](architecture.md)
does not rely on distributed skipping to reject a local adapter. It relies on
the missing external prelaunch resource-head hook and the combined authority
needed for ordering, admission, receipts, and reconciliation.

**Do not adopt as-is.** Duplicate rejection is not the required idempotent
acknowledgment, and runnable-item skipping is not strict head-blocking FIFO.
An adoption decision requires a demonstrated solution within the single queue
authority, including partial-intake recovery, durable key retention, uncertain
execution reconciliation, and resource admission. This proof alone did not establish
whether a thin adapter could close every gap. The subsequent approved source review
resolved that question for the combined contract: local mode avoids distributed-worker
skipping, but offers no external resource-head admission hook; an external wrapper
would become a scheduler. See the [decision and pinned references](architecture.md).
The approved command core is a separate follow-up, not a change to these observed
Dagu results.

Parallel dispatch/execution ordering, concurrency 2, memory floor/MemAvailable
admission, 4-light/1-heavy capacity, separate heavy-phase tokens, notification
redelivery, failed-job output, missing-output reporting, and active-job recovery
remain unproven. Successful process exit is not change validation.

**OpenCode permission handling is untested until actual later integration.**
No session/worktree ownership or permission-block behavior was exercised. No
shared helper, service, limit, global config, or production orchestrator changed.
The temporary host reprieve is not a production migration. A shared-server client
cgroup does not contain its server-executed tools. Heavy validation remains a
separate later phase; this assignment stops at these bounded results.
