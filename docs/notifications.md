# Completion notifications

OCQ stores one durable completion event per job. CLI pull/ack provides delivery;
the Herdr Queue view offers non-consuming pending/exact reads and explicitly
confirmed local acknowledgement. There are no proactive chat/network hooks or
delivery into agent sessions.

## Pull and acknowledge

```sh
ocq --state-dir STATE_DIR notifications --limit 20
# Durably handle/deduplicate the event, then acknowledge its exact ID:
ocq --state-dir STATE_DIR ack completion-JOB_ID
```

Pull returns the oldest unacknowledged events by sequence. Default limit is 20,
range 1–100; an empty result is `[]`. It does not claim, acknowledge, dispatch or
release capacity. Dispatch need not be running. Multiple consumers can see the
same event; there are no subscriber leases or independent cursors.

`ack` takes the event ID, not the job ID. It sets `acknowledged_at` once and
returns the event. Repeating it returns the same timestamp, so retrying after a
lost ack response is safe. Unknown IDs fail. There is no blanket acknowledgement
or event deletion command.

Consumers must persist handling and deduplicate by event ID before acknowledging.
Events are redelivered until acknowledgement; OCQ cannot make external consumer
effects exactly once. Acknowledgement never retries execution or resolves unknown
ownership.

## Herdr acknowledgement

Open an event in the Queue view, review the global acknowledgement, then confirm
it separately. **Acknowledgement is local-only**; remote selections retain reads
but disable this action. It removes the event from the pending global list, not
from retained history. It is distinct from project inbox read/archive and never
changes the job, releases capacity, retries work or answers permissions.

Preparation persists an intent for the exact local machine, originating Herdr
space, queue identity, event ID, job ID and decimal sequence. Sequences remain
decimal strings, not JavaScript numbers. Confirmation must receive an acknowledged
durable arm before launching one attempt; the attempt marker is synced before
the acknowledgement call. Only a separate read matching queue/event/job/sequence
and `acknowledged_at` establishes an observed acknowledgement, not client return.
That observation does not prove which caller acknowledged it.

After a lost response or panel reload, reopen the exact event in the original
local space and use **Check exact event and journal**. A job's retained global
event link and exact event-ID lookup also find events no longer in the pending
list. Prepared intents can be reviewed again; armed or uncertain intents offer
checks only, never automatic resend. A confirmed call may finish after leaving
the view. Another space cannot replace the original intent. Missing, mismatched
or unavailable read-back remains uncertain and is not permission to retry.

Queue identity detects accidental replacement/retargeting, not in-place restores
or hostile forgery. Do not replace or retarget queue paths while in use.

## Event contract

Fields are `id`, `sequence`, `job_id`, `state`, `outcome`, `exit_code`,
`validation`, `origin`, `recorded_at` and nullable `acknowledged_at`. Payloads omit
prompts, reasoning, receipts, arbitrary errors and output contents. Use job status
and logs for details.

- `id` is `completion-` plus the immutable job ID. Sequence orders events inside
  one queue; gaps are valid.
- Content is a frozen **first-terminal observation**, not a live job projection.
  Only its first acknowledgement can change. Events and acknowledgements are
  retained; there is no pruning command.
- `origin=transition` means event creation and the transition to `finished`,
  `cancelled` or `resolved` committed together. A failed event write rolls back
  the terminal transition.
- `origin=migration_snapshot` means migration observed an already-terminal job.
  Its recorded time is migration time, not an invented historical finish time.
  Backfill is ordered by receipt sequence and committed with migration.
- Exit zero, `validation=not_run`, `cancelled_before_launch`, launch failure and
  `operator_reconciled` remain distinct. Unknown/nonterminal states create no event.

An agent may later become unknown when reinspection loses a known resource. The
historical event remains frozen and does not assert current completion. Inspect
current stored state and reconcile ownership before acting on that history.

## Projects and upgrades

`project results` reads all retained project completion events, including globally
acknowledged events, without consuming them. Its cursor is not a result-publication
change feed; see [project observations](project-control-plane.md#stored-observations).
Project inbox read/archive is separate from global completion acknowledgement.

Writable notification commands can initialize or migrate a queue. Stop dispatch
before upgrading old state; current schema is **5**. Migration preserves immutable
receipts and backfills only already-terminal jobs. See [storage and upgrades](command-core.md#state-and-upgrades).
