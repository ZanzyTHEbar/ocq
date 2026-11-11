# Command queue

OCQ runs cooperative foreground commands on Linux. Commands execute directly as
argument vectors; a shell runs only when explicitly submitted as the executable.
Use the [agent interface](agent-integration.md) for OpenCode jobs, not a command
wrapper around an agent launcher.

## Commands

Place global `--state-dir DIR` before the command and command flags before
positional arguments. Successful output is JSON except log contents and the terminal
project board.

```text
ocq [--state-dir DIR] submit --key KEY --cwd DIR [--memory-bytes N] -- COMMAND [ARG...]
ocq [--state-dir DIR] list
ocq [--state-dir DIR] status JOB_ID
ocq [--state-dir DIR] logs [--stderr] JOB_ID
ocq [--state-dir DIR] dispatch [--commands-only] [--slots N] [--memory-floor-bytes N]
ocq [--state-dir DIR] cancel JOB_ID
ocq [--state-dir DIR] resolve --evidence TEXT JOB_ID
ocq [--state-dir DIR] notifications [--limit N]
ocq [--state-dir DIR] ack EVENT_ID
```

`cancel` accepts only queued work. `resolve` accepts only unknown command jobs;
agent jobs use `reconcile` instead. See [notifications](notifications.md) for
pull/ack semantics and [projects](project-control-plane.md) for project views.

Queued cancellation records `cancelled_before_launch` and its completion event
atomically. It never stops launching/running commands or agent processes, or
resolves unknown ownership. If dispatch admits the job first, cancellation is
refused and its actual state is preserved. The Herdr panel exposes a separately
confirmed local-only cancellation flow with durable single-attempt intent and
exact job/completion read-back; see [panel recovery](herdr.md#cancel-queued-work).

## State and upgrades

State defaults to `$XDG_STATE_HOME/ocq`, or `$HOME/.local/state/ocq`. The directory
must satisfy the [storage and runtime contract](storage.md): private storage with
coherent locking, SQLite WAL/shared memory and sync operations, retained for the
required recovery lifetime. There is no filesystem-name allowlist. Directories are owned,
nonsymlink mode 0700; files are mode 0600. Do not unlink locks or copy/move live
state. Use SQLite's online backup mechanism when backing up a running database;
OCQ has no backup or pruning command.

Writable queue opens can initialize or migrate state, including ordinary
`list`, `status` and notification commands. Stop dispatch before upgrading.
Current schema **5** is reached through additive transactional migrations:
agent ownership (2), completion events (3), projects (4), lanes/inbox (5).
Versions above 5 are rejected. Receipt/request bytes and existing evidence remain
unchanged. Project projection reads instead require an existing schema-5 queue
and never initialize or migrate it.

Connections use WAL, FULL synchronous mode, foreign keys and a 5000 ms busy
timeout. Effective settings are checked on open; required operations fail closed
when they report errors. These checks cannot prove persistence, correct locking
or power-loss behavior. Ephemeral state loses receipts and ownership at its
retention boundary; it is not durable queue storage.

## Requests and receipts

Version-1 command requests store `version`, `argv`, `cwd` and `memory_bytes` in
that order. Cwd is an existing absolute real path; the command is resolved to an
absolute, symlink-resolved path at submission. Bare names use the submitting
process's PATH; relative paths containing `/` resolve against cwd. Explicit paths
are not checked for executable-file suitability at intake, so launch can still
fail. Arguments and paths must be UTF-8; arguments cannot contain NUL.

An IMMEDIATE transaction accepts the immutable key, canonical bytes, SHA-256
digest, job ID, timestamp and monotonically increasing sequence before returning
a receipt. Matching retries recover the same job even after cancellation or
restart. Different bytes under the key fail; sequence gaps are harmless.
Executable/file contents are not snapshotted. Paths must still resolve for
ordinary command-submission retries, so retain inputs and do not mutate them
between matching retries. Project intake has a separate receipt-first contract.

The local Herdr [Queue command form](herdr.md#submit-a-general-command) is
independent of projects. It requires an explicit key, executable, JSON argument
array, cwd and decimal memory demand. Go resolves paths and canonicalizes v1 bytes
before private draft review. Preparation retains those exact bytes for a separate
sequential review and pointer confirmation, durable claim and one admission
attempt. Admission and exact receipt read-back use the retained key/bytes without
path re-resolution, including after a lost acknowledgement or later path changes.
Missing/conflicting receipts remain uncertain, never an automatic admission retry.

The form only queues work; it neither inserts a shell nor starts a dispatcher.
An already-running dispatcher may execute newly admitted work immediately. Paths
remain mutable: canonicalization is not executable suitability, content integrity
or safety verification. Receipt matching proves admission, not execution success.
Remote command submission is disabled. The Queue's separate local
[standalone agent form](agent-integration.md#local-queue-intake) uses canonical v2
requests and its own retained admission journal, not the v1 command route.

Execution inherits no submitting-shell secrets. Stdin is closed/default null;
the fixed environment is `PATH=/usr/bin:/bin`, `LANG=C`, `OCQ_JOB_ID` and
`OCQ_JOB_DIR`. Filesystem and network privileges are still those of the user.
Do not submit daemonizing commands or unmanaged child trees.

## Admission and shutdown

Defaults are two slots, a 6 GiB Linux MemAvailable floor and 64 MiB declared
memory per request. `--slots` and `--memory-floor-bytes` change admission only.
All launching/running/unknown demand counts conservatively. The earliest queued
job alone is considered; a blocked head prevents backfill. Missing memory data
closes admission. See [admission architecture](architecture.md#admission).

`dispatch --commands-only` neither admits agents nor polls their APIs. A queued
agent at the FIFO head still blocks later commands, and existing unknown agent
ownership still counts. Plain `dispatch` also handles supported agent jobs.

SIGINT/SIGTERM stops new admission and waits for this dispatcher's direct
commands. It does not kill them or wait for previously unknown work. A launch
handoff already in progress may finish before shutdown observes the signal. A
stuck command can delay shutdown indefinitely; [systemd supervision](supervision.md)
provides an optional bounded stop policy.

## Output and completion

Output files are prepared and synced before durable launch intent. PID/start
ticks are recorded after spawn; a crash between these steps can leave uncertain
ownership. Each command has its own process group, not guaranteed process-tree
containment.

Normal completion waits for the direct command and syncs output. An extant
process group or output-sync failure retains unknown ownership. `logs` reports
missing/not-created output explicitly; growing active output is not a durable
stream guarantee. Exit code, outcome and `validation=not_run` are separate.

Herdr Queue also provides [local commands-only Start/status/Drain](herdr.md#start-inspect-and-drain-a-command-dispatcher),
fixed at 2 slots and a 6 GiB floor. A separately confirmed one-shot native launch
runs the Go helper directly in a right split from an explicitly selected local
model pane; preparation launches nothing. Exact born/pane/ready evidence gates
dispatch. Existing commands may execute immediately; FIFO agent-head blocking and
unknown capacity accounting still apply. Status/reopen never relaunches or adopts
an unowned process. Explicit owned pidfd TERM Drain keeps the terminal open until
actual exit. Native terminal/pane/space closure may force-kill rapidly, not drain;
HUP stops admission but guarantees no grace, restart supervision or escaped-process
containment. Dispatcher exit/uncertainty is not job completion or release of
capacity. Remote and agent-enabled controls remain disabled.

## Recover unknown commands

After dispatcher restart, unresolved launching/running jobs become **unknown**,
retain capacity and are not rerun, even if the process is absent.

1. Stop dispatch for this queue.
2. Inspect the exact stored PID/start/boot identity, process group, output and
   external effects. Account for escaped descendants yourself.
3. If the command is known to have stopped, record the inspection:

```sh
ocq --state-dir STATE_DIR resolve --evidence 'process and side-effect inspection' JOB_ID
```

Resolution takes the dispatcher lock and refuses a live matching leader, extant
group or unobservable liveness. A launch with no recorded PID cannot be resolved
on the same boot; after a reboot, explicit evidence is still required. Resolution
records `operator_reconciled`, not success or validation, and does not rerun work.

The Herdr Queue view also offers [local unknown-command resolution](herdr.md#resolve-unknown-commands)
with private evidence entry, exact retained review and separate confirmation.
It uses these same dispatcher/process checks and a durable one-attempt journal;
recovery checks the exact stored evidence digest, job and completion, never
automatically retries. Eligible resolution may release unknown capacity, but the
evidence remains an operator assertion, not proof of successful execution or tests.

Do not use command process/boot checks to resolve an agent. Use API-only
`reconcile` or the separately confirmed local Herdr reconciliation action;
[agent recovery](agent-integration.md#recovery) explains the completion checks
and stored-only panel recovery.

## Development

Requirements and dependencies are pinned in `go.mod`/`go.sum`. SQL schemas and
queries live in `internal/core/sql`; generated queries live in
`internal/core/dbquery`. After SQL changes, run `sqlc generate` from the repository
root using **sqlc v1.30.0**. Goose migration DDL, connection safety checks and
transaction boundaries remain explicit core operations.

Build and validate serially:

```sh
GOMAXPROCS=2 go build -p 1 -o runtime/ocq .
GOMAXPROCS=2 go vet -p 1 ./...
GOMAXPROCS=2 go build -p 1 -o runtime/ocq-acceptance ./proof/command-core
GOMAXPROCS=2 runtime/ocq-acceptance "$PWD/runtime/ocq"
```

The command acceptance runner uses real CLI processes, private persistent SQLite,
host memory observations and bounded foreground probes. It exercises idempotency,
FIFO/head blocking, singleton dispatch, output and crash-to-unknown recovery.

Project acceptance uses a fresh current-schema queue and an existing explicit
OpenCode reviewer, without starting dispatch or executing an agent:

```sh
OCQ_PROOF_REVIEW_AGENT=REVIEW_AGENT GOMAXPROCS=2 runtime/ocq-acceptance --project "$PWD/runtime/ocq"
```

Use an isolated intended OpenCode discovery context and a reviewer available at
the fixture Location. The review gate requires server **2.2.6**. Migration checks
are optional separate modes, not prerequisites for current-schema acceptance:

```sh
OCQ_PROOF_REVIEW_AGENT=REVIEW_AGENT GOMAXPROCS=2 runtime/ocq-acceptance --project-legacy "$PWD/runtime/ocq" LEGACY_SCHEMA1_BINARY LEGACY_SCHEMA2_BINARY
GOMAXPROCS=2 runtime/ocq-acceptance --notifications "$PWD/runtime/ocq" LEGACY_SCHEMA1_BINARY LEGACY_SCHEMA2_BINARY
```

Supply authentic historical executables, not fabricated database version numbers.
Runners create retained private fixtures; they are not production migrations.
