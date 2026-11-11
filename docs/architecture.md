# Architecture

OCQ has one Go + SQLite queue core with CLI and Herdr interfaces. Business logic
lives in `internal/core`; `internal/cli` parses inputs and renders responses.
The short-lived `ocq-herdr` bridge calls the same core directly, not the CLI.

## Responsibilities

| Component | Owns |
| --- | --- |
| SQLite queue | Immutable receipts, FIFO order, lifecycle state, ownership records, project metadata and completion events |
| Dispatcher | Admission, command launch/wait, agent provisioning and observation |
| OpenCode | Agent execution, tools and permission decisions |
| Workmux | Dedicated agent worktree provisioning |
| Herdr | Stored views and explicitly confirmed project actions; not scheduling or completion authority |

The core is a library plus CLI, not an HTTP service or a generic backend layer.
It uses `dbbootstrap`, pure-Go `modernc.org/sqlite`, Goose migrations and
sqlc-generated queries. Versions are pinned in `go.mod` and `go.sum`.

## Storage and transactions

Each writable connection uses WAL, FULL synchronous mode, foreign keys and a busy
timeout. Effective settings and private ownership/modes are checked on open;
required storage operations must succeed. Persistence is a deployment obligation,
not inferred from filesystem type; see the [storage contract](storage.md).
A short initialization lock serializes setup/migration; a separate singleton
dispatcher lock prevents concurrent dispatchers for one queue.

Receipt creation and job acceptance commit together. Terminal transitions and
completion events also commit together. External processes and APIs are outside
SQLite transactions: durable mutation intent precedes launch, and ambiguous
responses preserve unknown ownership rather than replaying effects.

Current schema **5** includes agent ownership, completion notifications, projects,
lanes and inbox mail. Migration is additive and transactional with dispatch
stopped; immutable request bytes and receipts are preserved. Project projection
reads use non-initializing read-only connections and require existing schema-5
state. They do not create or migrate a queue.

## Admission

Only the earliest queued receipt is considered. Default admission requires fewer
than two launching/running/unknown jobs and:

```text
MemAvailable - memory floor - all inflight declared demands >= head demand
```

The default floor is 6 GiB. Missing memory observations fail closed. Declared
demands are counted conservatively, including unknown jobs; they are not memory
reservations or limits. There are no project-specific slot pools or heavy-phase
tokens. Concurrent execution start/finish order is not guaranteed.

## Integration APIs

`core.OpenQueue` owns normal writable CLI operations and initialization.
`core.OpenReader` exposes existing stored queue/project views without mutation.
`OpenExistingMailSender`, `OpenExistingMailAcknowledger` and
`OpenExistingProjectIntake` provide narrow existing-store write boundaries without
initialization or migration. Direct callers must retain their exact inputs and
handle ambiguous responses; the intake boundary itself does not journal or retry.

Herdr compose/intake add private local journals around those boundaries. A
confirmed intent must be durably armed before one dispatch attempt, with an
attempt marker persisted before the core call. Recovery reads exact stored
receipts; missing receipts do not authorize resend. See [Herdr recovery](herdr.md#mutation-recovery).

## Isolation boundary

A worktree separates files, not users or processes. Commands retain filesystem
and network access. Agent ownership metadata detects accidental mismatches, not
malicious same-user forgery. A client or dispatcher cgroup does not contain tools
executed by a shared OpenCode server. Detached daemons and tools outside supported
runtime tracking are not covered by the completion contract.
