# Durable local completion notifications

ocq's first notification interface is **local CLI pull**, not proactive chat
delivery. The existing SQLite queue stores one frozen completion event per job.
There is no network sink, hook, subscriber service, or delivery into agent sessions.

## Pull and acknowledge

```sh
runtime/ocq --state-dir STATE_DIR notifications --limit 20
# Process/persist the event using its stable ID, then acknowledge it:
runtime/ocq --state-dir STATE_DIR ack completion-JOB_ID
```

`notifications` returns a JSON array of the oldest unacknowledged events, ordered
by their durable sequence. Default limit **20**, allowed range **1–100**; no
positional arguments. An empty queue returns `[]`. Events contain fixed lifecycle
fields, not prompts, receipts, reasoning, output contents or arbitrary error text.
Use `status JOB_ID` and `logs JOB_ID` for current job details/output.

Pull is read-only after any required schema migration: it does not claim events,
acknowledge them, change capacity, or run a job. Repeated pulls return the same
event until acknowledged, including after client or dispatcher restart. A
dispatcher need not be running to pull or acknowledge. Multiple local consumers
may see the same event; there are no leases or per-subscriber cursors. Acknowledge
older events to advance through a bounded pending batch.

`ack` requires the exact **event ID**, not the job ID. It commits an acknowledgement
and returns the event with `acknowledged_at`. Repeating it returns the same event
and original acknowledgement timestamp. An unknown ID fails; there is no blanket
acknowledgement command. If the acknowledgement response is lost, retrying `ack`
is safe. Acknowledgement is permanent and does not delete the event.

This is **at least once until acknowledgement**, provided a consumer continues
pulling. A consumer should durably handle/deduplicate the event by ID before
acknowledging; ocq cannot make external consumer side effects exactly once. No
automatic execution retry or proactive delivery is implied.

## Frozen event contract

Each event has `id`, `sequence`, `job_id`, `state`, `outcome`, `exit_code`,
`validation`, `origin`, `recorded_at`, and nullable `acknowledged_at`.

- `id` is deterministically `completion-` plus the immutable job ID. Sequence
  orders events within this queue; gaps are harmless.
- `state`, `outcome`, exit code and validation are a **first-terminal observation**,
  not a live projection of the job. Event content cannot be changed or deleted;
  only its first acknowledgement can be recorded. Both event and ack are retained
  indefinitely initially; there is no pruning/reset command.
- `origin=transition` means the event was inserted in the same SQLite transaction
  as a change to `finished`, `cancelled`, or `resolved`. An AFTER UPDATE trigger
  covers command exit (including nonzero exit), launch failure, queued command or
  agent cancellation, command operator resolution, and agent terminal observation.
  Failure to persist an event rolls back the terminal state change too.
- `origin=migration_snapshot` means migration observed an already-terminal stored
  job. `recorded_at` is the migration observation time, **not an invented historical
  completion time**. Historical output/runtime resources are not revalidated by
  migration.
- `validation=not_run` stays separate from server success or exit zero. Queued
  cancellation is `cancelled_before_launch`; resolution is `operator_reconciled`,
  never fabricated success. Launch failure has no invented command exit code.
- No new event is created for `queued`, `launching`, `running` or `unknown`.
  Reinspection cannot duplicate/reset an existing event or its acknowledgement.

A job can later become unknown during agent reinspection (for example, a known
shell becomes unobservable). Its historical event remains frozen, labelled as
the prior observation; **it does not assert current completion**. Read current
status before making ownership decisions. Acknowledging that historical event
does not release unknown capacity, resolve ownership, resume work, or retry it.

## Schema migration and safety

Schema **3** adds only the outbox, index and triggers. Existing schema 1 upgrades
transactionally through schema 2 to 3 under the initialization and dispatcher
locks; stop dispatch before migrating. Schema 2 adds just the outbox. Future
versions are rejected. Receipt bytes/digests/keys/sequence/timestamps, v1/v2
requests, agent mutation journals, admission and uncertainty semantics are
unchanged. No delivery fields are inserted into immutable receipts.

Already-terminal jobs receive exactly one labelled migration snapshot, ordered
by receipt sequence. Nonterminal/unknown jobs receive none. Backfill and schema
version change commit together, so reopening does not repeat backfill. Existing
WAL/FULL/private persistent storage requirements still apply. The first command
opening old state performs migration, even a pull; do not use retained evidence
queues as migration experiments.

## Real acceptance and limits

```sh
GOMAXPROCS=2 go build -p 1 -o runtime/ocq-acceptance ./proof/command-core
# Saved real pre-migration binaries, not fabricated PRAGMA versions:
GOMAXPROCS=2 runtime/ocq-acceptance --notifications "$PWD/runtime/ocq" LEGACY_SCHEMA1 LEGACY_SCHEMA2
GOMAXPROCS=2 runtime/ocq-acceptance "$PWD/runtime/ocq"
```

The real scenario proves schema1→2→3 receipt preservation/backfill, bounded pull,
consumer SIGKILL before ack, dispatcher crash/restarts, byte-identical redelivery,
lost-ack-response/idempotent acknowledgement, durable acknowledgement after
restart, one command execution, cancellation and genuine OS launch failure.
Existing command acceptance also verifies no event while a crashed command is
unknown, then an honest operator-resolved event rather than success.

Agent migration/reinspection used isolated SQLite online-backup copies only. The
retained no-tool check passed with no duplicate event or execution replay. The
old tool check failed closed because its known shell ID was no longer observable;
that copy stayed unknown, and ack did not release its slot. Original schema-2
queues, receipts and outputs were unchanged. Details are in private
`RESULT-NOTIFICATIONS.md`; no new model jobs or runtime mutation requests ran.

Network/chat delivery, subscriber fan-out, retention/pruning,
power-loss/storage-failure injection and consumer-side exactly-once effects are
not implemented/validated. This slice does not install or deploy a service.
An isolated [command-only supervision proof](supervision.md) now also preserves
pending notifications and acknowledgements across supervised lifecycle changes;
it does not add proactive delivery or install a persistent service.
