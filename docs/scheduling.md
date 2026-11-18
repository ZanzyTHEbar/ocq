# Machine-local scheduling cutover

OCQ keeps one SQLite authority per machine. Schema 6 retains legacy strict FIFO.
Schema 7 adds an explicitly activated `project_rr_v1` policy; observation and
ordinary queue opens do not activate it. Replace CLI, bridge, dispatcher and mod
as a matched release before upgrading. Old writers reject schema 7.

## Activate deliberately

Quiesce submitters and clients, stop dispatch, and retain a coherent SQLite
backup according to [storage](storage.md). Do not replace or move a live queue.

```text
ocq --state-dir STATE_DIR scheduling-policy
ocq --state-dir STATE_DIR activate-scheduling
ocq --state-dir STATE_DIR scheduling-policy
```

The first/last commands are non-initializing reads. Activation holds the schema
and dispatcher locks, applies transactional Goose migration 3 (6→7), then commits
the policy cutoff at the largest accepted receipt sequence. Migrations 1 and 2
are unchanged. Structural migration and policy activation are separate commits
under those locks: interruption between them leaves legacy FIFO active; retrying
activation is safe. An already activated cutoff is immutable.

Queued receipts at or below the cutoff drain in their original FIFO order.
An oversized legacy head therefore still blocks newer jobs until it runs or is
explicitly cancelled. Existing launching/running/unknown reservations are neither
recreated nor released. Legacy unknown consumes capacity, not a queue-wide gate.
The policy is bound to the queue's local file identity; copying or retargeting
an activated store is not a supported recovery shortcut.

## Subsequent admissions

For each project, choose its oldest **runnable** queued receipt. Skip jobs with
unmet exact-artifact dependencies before choosing that head. Then apply slot and
memory fit. A smaller later job cannot bypass a runnable oversized head in the
same project. Other fitting project heads remain eligible.

Projects form a deterministic lexicographic project-ID ring. Selection advances
past the persisted last-selected project, wrapping at the end. The cursor and
reservation/launch intent commit atomically. Unattributed general commands and
standalone agents share the `unattributed` lane. Project submissions use their
actual bound project ID automatically, including submissions from Main/Work.

All launching/running/unknown jobs consume a slot and their declared memory.
Admission observes actual Linux `MemAvailable`, subtracts the configured floor
and held declared demand, and admits only a fitting head. This is conservative
admission accounting, **not** OS-enforced memory containment. Fairness is
conditional on continuously resource-eligible heads; a never-fitting job has
no progress guarantee. Command-only dispatch does not execute or poll agents;
an agent head blocks its project, not other projects under the new policy.

There is no global scheduler, cross-machine transaction, broker, priority,
weighted fairness, work stealing or permission automation. Unknown work on one
machine cannot gate eligible work on another machine.

## Exact artifact intake

Canonical command-v1 and agent-v2 receipt bytes are unchanged. An independent
version-1 scheduling envelope is accepted by:

```text
ocq --state-dir STATE_DIR submit --key KEY --scheduling-file SCHEDULE.json -- COMMAND
ocq --state-dir STATE_DIR project submit --envelope-file SUBMISSION.json --scheduling-file SCHEDULE.json
```

Use `queue_identity` from `scheduling-policy`, an existing local project, and
exact retained producer/association/digest values:

```json
{
  "version": 1,
  "queue_identity": "QUEUE_IDENTITY",
  "project_id": "proj-example",
  "dependencies": [{
    "project_id": "proj-example",
    "producer_job": "job-EXACT_PRODUCER",
    "association_id": "assoc-EXACT_ASSOCIATION",
    "manifest_digest": "EXACT_SHA256",
    "criterion": "captured_snapshot",
    "verification": "machine_verified_snapshot"
  }]
}
```

The illustrative IDs/digests above must be replaced with valid exact identifiers.
An empty dependency list is allowed. Intake caps the envelope at 16 KiB and 32
dependencies. Unknown fields, another queue/project, changed scheduling metadata
under an existing key, and unsupported criteria are refused. Dependencies are
immutable, local, and reference an already admitted producer; the exact
association may be published later. There is no generic workflow-DAG API.

Readiness requires that exact project, producer, association and manifest digest,
with a `machine_capture` result qualified as `machine_verified_snapshot` and
`validation=not_run`. A process exit, same-looking ID, different association,
wrong digest or imported success assertion cannot satisfy it. Machine capture
qualification is not test validation.

Ordinary project review intake automatically retains its exact published capture
target as a scheduling prerequisite after activation. Explicit review scheduling
must retain that target too. Existing review target linkage remains enforced.
Capture remains a cooperative publication step, not a model job. Work's capture,
review-result and handoff gates remain separate from execution admission:
**a successful terminal agent is not a completed workflow**.

## Launch and recovery

Only a fresh successful committed reservation authorizes the immediately
following external launch. Reading `launching` or `unknown`, reopening a UI or
restarting dispatch cannot recreate that authorization. Restart converts
interrupted launching/running work to unknown while retaining capacity. Exact
PID/start/boot receipt CAS and terminal evidence guard execution transitions;
terminal snapshots and completion outbox publication retain their existing
atomic boundary. No SQLite transaction spans spawn, OpenCode/network calls,
permission waits or command execution.

## Revision-bound pages

Projects and lanes use indexed immutable `(created_at, id)` keysets in their
historical order, with 20 rows plus one lookahead. Their opaque `k1` cursor binds
the selector/project, schema/projection version, queue identity and observation
revision. Start at `"0"`; old nonzero numeric offsets are not reinterpreted.
An intervening observable write produces `reset_required`, never a silently
skipped or mixed-revision continuation. The panel offers an explicit restart.
Existing receipt, event and mail sequence cursors are unchanged.

Observation remains non-initializing and read-only; selected database views share
one SQLite read transaction. Filesystem logs are still separate observations.
The inode fence does not detect an in-place database restore.

## Validation and limits

Run the existing legacy acceptance unchanged, then the explicit cutover scenario:

```text
ocq-acceptance OCQ_BINARY
ocq-acceptance --scheduling OCQ_BINARY
```

The latter uses real CLI admissions, private SQLite stores and harmless command
lifecycles, with clearly identified historical project/artifact fixtures. It does
not execute agents or prove capture authenticity. It checks legacy drain,
unknown-held capacity, project rotation/restart, exact dependency rejection,
terminal/outbox publication, keyset pages/revision rejection and independent
stores. It records bounded query plans and write-lock availability, not a
general performance benchmark.

The prerequisite bounded TLA+ models cover crash handoff, retained reservations,
artifact linkage and pinned routing, including expected failing controls.
Finite model-checker results are design evidence, not implementation proof or
an unbounded fairness guarantee.
