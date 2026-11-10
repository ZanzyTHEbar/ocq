# Alpha command core

This is a local Linux foreground-command queue, **not a production coding-agent
system**. SQLite is the only queue authority. The [architecture](architecture.md)
explains the core's ownership boundaries. The
[full reliability contract](reliability-contract.md) still includes integration
and deployment work. The subsequent [experimental agent slice](agent-integration.md)
adds provisioning and session-scoped cooperative tool completion; the v1
command contract and serialized request bytes remain unchanged.

## Build and use

Go 1.26.5+; database connections use
[`dbbootstrap` v0.1.1](https://github.com/ZanzyTHEbar/dbbootstrap), with
[`modernc.org/sqlite` v1.61.0](https://pkg.go.dev/modernc.org/sqlite@v1.61.0).
OCQ uses Goose v3.28.0 for a single locked, transactional Go migration and
sqlc v1.30.0 for generated application queries; SQLite safety checks remain
owned by OCQ.
Its current-release metadata and module Go requirement were checked on 2026-10-26;
`go.mod`/`go.sum` pin the driver and transitive dependencies. Pure Go, no CGO or
external runtime required. Builds/validation are deliberately serialized/bounded:

```sh
GOMAXPROCS=2 go build -p 1 -o runtime/ocq .
GOMAXPROCS=2 go vet -p 1 ./...
runtime/ocq --state-dir "$HOME/.local/state/ocq-demo" submit \
  --key hello-v1 --cwd "$HOME" --memory-bytes 67108864 -- /usr/bin/printf '%s\n' hello
runtime/ocq --state-dir "$HOME/.local/state/ocq-demo" dispatch
# In another terminal, using the returned job ID:
runtime/ocq --state-dir "$HOME/.local/state/ocq-demo" status JOB_ID
runtime/ocq --state-dir "$HOME/.local/state/ocq-demo" list
runtime/ocq --state-dir "$HOME/.local/state/ocq-demo" logs JOB_ID
runtime/ocq --state-dir "$HOME/.local/state/ocq-demo" logs --stderr JOB_ID
runtime/ocq --state-dir "$HOME/.local/state/ocq-demo" cancel QUEUED_JOB_ID
```

Global `--state-dir` precedes the command; command flags precede positional
arguments. Defaults: `$XDG_STATE_HOME/ocq` or `~/.local/state/ocq`, two slots,
6 GiB memory floor, 64 MiB declared request demand. Dispatch flags `--slots` and
`--memory-floor-bytes` configure admission only; they change no host limits.
`dispatch --commands-only` blocks agent admission at the FIFO head and skips
agent API polling without releasing existing unknown demand. This opt-in mode
is used by the [systemd user-unit template and supervision proof](supervision.md);
manual dispatch without it is unchanged.

State must be persistent local ext4-family, XFS, or Btrfs. Filesystem checking
deliberately rejects tmpfs, network filesystems, overlay, and virtiofs/shared VM
mounts. The repository's current shared mount is unsuitable; acceptance evidence
uses private local XDG state on ext4 instead. The check is not proof of physical
disk flush guarantees or off-host durability; operators must verify the storage.
Directories must be owned nonsymlink 0700; files are 0600. Do not move/copy live
state or unlink lock files. Additive transactional migration installs schema 2
agent ownership/operations, schema 3 completion notifications, schema 4 project
control plane, and schema 5 owned lanes/integrator inbox, under the
initialization lock with dispatch stopped. Future schema versions are rejected;
old receipt bytes and triggers are retained. There is no backup, pruning, or
deployment automation. Never put live database backups in tmpfs.

The single Goose migration reads the existing SQLite `user_version` and applies
all pending historical steps in one write transaction. Its version record is
committed in the same transaction. Goose may create its version table and version
zero record in a separate transaction first; if the migration fails, queue data
and `user_version` roll back and a later open retries using that metadata. The
schema lock is held throughout initialization, and an upgrade below schema 5
also requires the dispatcher lock. A schema-5 queue needs no schema steps.

`internal/core/sql/schema*.sql` are both the embedded migration SQL and sqlc's
schema input; `internal/core/sql/{agents,dispatch,inbox,notifications,projects,receipts}.sql`
contain the application queries. Rebuild the generated Go package with pinned
`sqlc v1.30.0` using `sqlc generate` from the repository root. Generated methods
run on the caller's existing database, connection, or transaction. SQLite
connection PRAGMAs, `BEGIN IMMEDIATE`/commit/rollback, and Goose's migration DDL
remain explicit runtime operations, not application-query wrappers. The driver
DSN sets `_txlock=immediate` for every replacement writable connection; read-only
transactions and non-initializing projection opens remain read-only.

## Request and receipt semantics

Version 1 canonical requests are JSON structs containing `version`, `argv`, `cwd`,
and `memory_bytes`, in that field order. Cwd is an existing absolute real path;
argv[0] is resolved at submission to an absolute real executable path. Bare names
use the submitter's PATH; relative paths containing `/` use the requested cwd.
Arguments and cwd must be UTF-8; arguments cannot contain NUL. Symlink spelling
does not change the canonical request; argument ordering/bytes and memory demand do.
Executable/file contents are **not** snapshotted or hashed; do not mutate them
between matching retries. Paths must still resolve when retrying a submission.

Execution uses argv directly, never shell interpolation. A shell only runs if
explicitly requested as the executable. Stdin is closed/default null input. The
environment is fixed to `PATH=/usr/bin:/bin`, `LANG=C`, plus per-job `OCQ_JOB_ID`
and `OCQ_JOB_DIR`. Submitter secrets/environment are not inherited. This policy is
part of request version 1. Commands retain the user's filesystem/network powers;
this is **not sandboxing**. Only cooperative harmless foreground probes were tested.

An IMMEDIATE transaction inserts both the queued job and its immutable receipt
before any acknowledgment is printed. The receipt stores global key, canonical
bytes, SHA256 digest, stable random job ID, immutable acceptance timestamp, and
monotonically increasing AUTOINCREMENT sequence. Matching retries return that same
identity even after cancellation/restart; changed requests fail. Sequence gaps
are harmless. SQL triggers reject receipt updates/deletes. Receipts are retained
**indefinitely initially**, independent of cancellation/output; no deletion command
exists. There is no automatic retry of execution, including resolved jobs.

SQLite uses WAL, FULL synchronous, foreign keys, and a 5000 ms busy timeout via
driver DSN pragmas on every connection. Effective settings are checked on open;
each process uses at most one connection. A short schema-initialization flock
serializes first-time WAL setup (not receipt ordering). IMMEDIATE write
transactions serialize receipts and lifecycle updates. Spawn, output setup,
process waits, and liveness inspection stay outside write transactions. These
mechanisms rely on the filesystem honoring SQLite/fsync; power-loss testing has
not been performed.

## Admission, uncertainty, and outputs

The singleton dispatcher holds `dispatch.lock`; the file records its PID/start
ticks/boot identity. Lock ownership, not a stale PID file, determines exclusivity.
Only the earliest queued sequence is examined. A capacity/memory/artifact-blocked
head prevents backfill; status explains the head's reason and identifies FIFO
blocking for followers. Admission requires:

```text
active launching/running/unknown count < slots
MemAvailable - floor - all inflight declared demands >= head declared demand
```

Arithmetic fails closed/saturates on extreme demands. MemAvailable includes
unmanaged host workloads; unavailable observations close admission. Subtracting
all inflight declarations is deliberately conservative and may double-count
memory already reflected in MemAvailable. Intent/spawn handoffs are serialized;
concurrent **execution start/finish order is not guaranteed**. Declared demands
and a memory floor are not reservations, cgroups, or enforcement. Heavy phases
are not implemented; no token waits occur inside the slot pool.

Before committing launch intent, output files/directories are created and synced.
A crash before intent leaves an unlaunched queued job with reusable empty files.
Intent stores paths and boot identity **before spawn**; exact PID/start ticks are
stored afterward. SQLite cannot atomically spawn: a crash in between may leave no
PID or an already-running command. On restart, unresolved launching/running jobs
become **unknown**, keep capacity/demand/ownership, and are never automatically
rerun, even if their process is absent. This is deliberate at-most-one launch
attempt rather than an exactly-once side-effect guarantee.

Normal completion waits for the direct command and syncs output before updating
exit status. An extant process group or output-sync failure retains unknown
ownership. Output availability is separate from state; `logs` fails explicitly
on missing/not-created output. Growing active output is not a power-loss-durable
log stream. Exit code, outcome, and `validation=not_run` remain separate: exit 0
does not mean a validated deliverable. Operator resolution does not change that.

SIGINT/SIGTERM stops new admission and waits for this dispatcher's direct commands
to finish; it does not kill them, infer tool cancellation, or wait for previously
unknown processes. This can wait indefinitely for a stuck command. Each command
gets its own process group, but descendants may escape groups: no process-tree
containment is claimed. Do not launch daemonizing/unmanaged child trees.

Only queued jobs can be cancelled. To reconcile unknown work, first stop dispatch,
inspect exact process/group identity, outputs and external side effects, then:

```sh
runtime/ocq --state-dir STATE_DIR resolve --evidence 'operator inspection details' JOB_ID
```

`resolve` is restricted to v1 command jobs. Agent jobs must use lock-exclusive,
API-only `reconcile`; a client PID/boot identity is never proof an agent stopped.

Resolution takes the dispatcher lock and refuses a live matching leader, extant
group, unobservable liveness, or missing identity on the same boot. The narrow
unrecorded-PID launch gap cannot be resolved on that boot by this alpha CLI;
after a reboot, old-boot processes are known absent, but operator reconciliation
evidence is still required. A known-dead process can be resolved with evidence;
receipt/output remain and nothing is rerun. Evidence is an operator assertion,
not proof that escaped descendants/external effects stopped or validation passed.

## Durable completion notifications

`notifications [--limit N]` pulls the oldest unacknowledged completion events
(default 20, maximum 100). `ack EVENT_ID` acknowledges one event idempotently.
Terminal transition and frozen event commit atomically in the same SQLite queue;
unacknowledged events survive consumer/dispatcher restart without execution replay.
Cancellation, launch failure and operator resolution remain distinct from success
and `validation=not_run`. Schema 3 backfills explicitly labelled snapshots for
already-terminal jobs without rewriting receipts or inventing finish times.
See [local pull/ack contract and acceptance](notifications.md). This is not
proactive chat/network delivery and does not change admission or unknown ownership.

## Real acceptance evidence

Build product first, then this single focused real-system scenario:

```sh
GOMAXPROCS=2 go build -p 1 -o runtime/ocq .
GOMAXPROCS=2 go build -p 1 -o runtime/ocq-acceptance ./proof/command-core
GOMAXPROCS=2 runtime/ocq-acceptance "$PWD/runtime/ocq"
```

Project registration/submission, lanes, the integrator inbox, Goldfish review,
capture/verification, and the terminal board use a fresh schema-5 queue with
`--project`; this does **not** test migration. It creates and cancels two v1
command jobs in that current-schema queue to check refusal of unattributed-key
adoption. No dispatcher is started in this project scenario.

```sh
OCQ_PROOF_REVIEW_AGENT=YOUR_EXISTING_REVIEW_AGENT GOMAXPROCS=2 runtime/ocq-acceptance --project "$PWD/runtime/ocq"
```

To run the same project assertions after real schema-1 → 2 → 3 → 4 → 5 migration,
including receipt preservation, non-initializing old-schema reads, migration
notifications, and unattributed legacy jobs, invoke the separate legacy mode:

```sh
OCQ_PROOF_REVIEW_AGENT=YOUR_EXISTING_REVIEW_AGENT GOMAXPROCS=2 runtime/ocq-acceptance --project-legacy "$PWD/runtime/ocq" /path/to/ocq-schema1 /path/to/ocq-schema2
```

The last two arguments must be authentic historical OCQ executables that create
or upgrade schema-1 and schema-2 queues, not database files. The former
`--project OCQ LEGACY_SCHEMA1 LEGACY_SCHEMA2` form remains a legacy-mode alias.
Both project modes discover an existing OpenCode session and verify an explicit
review agent; isolate their discovery environment before running them when a
shared service must not be inspected. See [project control plane](project-control-plane.md).

The runner prints a persistent private XDG evidence directory. It uses real CLI
processes/SQLite/host MemAvailable, not mocks, and supplies its own harmless
foreground probe executable. It records dispatcher identity before exec and
probe identity before crash testing; cleanup releases only owned probes, signals
only exact owned PID/start identities, and verifies exit on success/failure.
`result.log` is synced throughout for recovery.

Observed on 2026-10-26: the first concurrent first-open scenario exposed a real
WAL-initialization SQLITE_BUSY race, fixed by the short initialization flock.
The subsequent scenario passed concurrent same-key submissions with one discarded
stdout acknowledgment, changed request rejection under the same key, receipt retention after
cancellation, oversized-head blocking with default two slots and real MemAvailable,
singleton exclusion, follower admission after queued head cancellation, literal
argv/output, and dispatcher SIGKILL/restart to unknown without duplicate execution.
The final run (12:08:44–12:08:45 UTC) also verified unknown capacity retention
using a one-slot restarted dispatcher and a fitting queued follower, unsafe
live-process resolution rejection, and explicit resolution after the cooperative
probe exits. A separate run was intentionally interrupted while its harmless
probe was active: it exited 1 on context cancellation, released the owned probe,
retired the dispatcher, and verified all recorded PID/start identities exited.
Gofmt, bounded `go vet`, both bounded builds, and `git diff --check` passed.
Exact run identities/raw evidence
remain private, not in tracked documentation.

## Deployment gates

Experimental OpenCode/Workmux provisioning, retained no-tool completion, real
read/patch/tool permissions and cooperative runtime-shell completion are evidenced.
Arbitrary agent-tool/resource handling, proactive chat/network notification delivery,
heavy-phase tokens, production deployment,
hostile/daemonizing descendants, storage-failure injection and power loss remain
unvalidated. Memory defaults need profiling, not a capacity claim. No
permission block may be represented as successful completion in later integration.
A shared-server client cgroup does not contain server-executed tools. The temporary
host reprieve is only an experimental launch gate, not a production migration.
These remaining gates require separate work; no deployment is authorized.
