# Goals and sessions in Herdr

Choose a machine and a project, write a goal, and send it to Main. Open Main to
discuss or redirect the work. Goal sessions appear when Main links an admitted
worker or reviewer to the exact goal and an owned session is recorded. Older
jobs without that link remain stored; OCQ does not infer lineage from their
titles or timestamps. Older bridges can show the generic “Project goal” title.
An existing unsent goal remains available as retained text. The panel neither
submits it on navigation nor retries a send whose outcome is uncertain.

To set up a project, expand its Herdr space, choose the tab, and, for a split
tab, choose the terminal in the primary Git checkout. Setup uses the live
terminal folder, not the space name or launch directory. A terminal that
moves, closes, or changes checkout must be selected again. Setup uses one
machine-owner configured choice per role; see
[owner setup prerequisites](operator-workflow.md#owner-capability-boundary).
**Open** navigates to the exact conversation in your
[opted-in OpenCode tab](herdr.md#opt-in-to-exact-navigation-in-your-normal-opencode-tui).
If more than one local tab matches, choose it in Herdr. If none match,
start OpenCode normally in that space; OCQ never starts a replacement.
Herdr notifications use committed project
result event IDs, only for newly observed transitions in the selected project
after the initial history read. A notification action can open only the same
exact session in the current project space. A stale action never chooses
another target. This is a foreground signal, not a durable notification
subscription. No event is acknowledged by the panel.

## Conversation and deletion

- **Open is the conversation.** Use the existing OpenCode tab for ordinary updates, including
  while a project worker is running. OCQ verifies the exact original prompt
  first, then treats later native text as context from the configured local
  harness, not evidence of a particular author. The history, agent/model,
  permissions and owned-resource checks still apply. Structured inputs outside
  that bounded worker contract still require attention. Native permissions
  and forms remain human-only. Standalone queue jobs keep their single-input
  contract.
- **A finished job stays finished.** Further native conversation after OCQ
  freezes the first terminal observation is outside that job and its queue
  slot. It does not rewrite its history, reopen capacity accounting, or cover
  new edits with the prior result. New changes need a new reviewed snapshot;
  reviews and worktree verification still refer to exact snapshot digests.
- **Delete** is available for exactly goal-owned workers with proven terminal
  jobs, and the currently selected OCQ-created Main (not adopted legacy
  sessions). OCQ checks the live service, ownership, full transcript, idle
  state, requests, children, and cooperative resources. Stop an active session
  in its native OpenCode tab, then check again. OCQ sends no separate interrupt.
  Busy, unsupported, missing, or unobservable evidence cannot
  authorize deletion.
- Confirmation rechecks the frozen target and preserves a bounded private
  transcript audit before one DELETE. Success requires native 204 and a typed
  `SessionNotFoundError` with the exact session ID on the same service.
  Upstream deletion can interrupt execution and recurse into children; OCQ
  refuses either when observed and the person must leave the session idle
  during confirmation. Ambiguous attempts stay recorded and
  blocked; never repeat them. These are cooperative checks, not a server-atomic
  compare-and-delete. No result, worktree, branch or capacity state is changed.
- Main retirement is recorded against the SQL-selected setup in its existing
  journal. The original binding remains historical and no Main is selected
  after confirmed retirement. Only an explicit **Create Main** may create the
  successor and advance the existing SQL binding with its revision check.

The existing agent-side and core protocols are unchanged. This panel does not
add queue or mail update/delete primitives.

Native contract: the OpenCode 2.3.2 release
(`c0ad18b402eb2827fc1ee71c171b1177b3fc02d1`) defines session remove and
interrupt in `packages/protocol/src/groups/session.ts`; their handlers live in
`packages/server/src/handlers/session.ts`. Recursive removal is in
`packages/core/src/session.ts`. The exact not-found error is defined by
`packages/protocol/src/errors.ts` in both 2.3.0 and 2.3.2.

Historical `proof/herdr-*` runs that instrument the old queue/action controllers
need a matching historical mod tree. They are not interaction checks for this
replacement.

## Standalone goal intake

A matching bridge advertising `work-task-submit-v1` can queue an explicitly
submitted New goal as implementation work, without sending its text to Main.
It uses the owner registry's single implementation selection and the project's
saved binding revision and commit anchor. The bridge accepts bounded text over
the authenticated project connection. Standalone admission checks the owner’s
implementation agent/model and shared execution readiness. Neither Main setup
nor ordinary goals need or launch a reviewer. Explicit restricted review
intake and execution still
require that separate release-specific policy check. The panel accepts schema 8 from a matching
bridge and requires the selected project and snapshots to agree with that
connection’s advertised schema. The UI stores the request ID and text
digest before submitting and checks the exact Work receipt if the reply is
lost. An incomplete preparation or ambiguous submission does not permit a
second attempt. No session is opened by submission.

The standalone marker binds the Work intent, goal digest, title and base. It
does not replace a queue receipt or change legacy Main goal lineage. Queued
tasks and their exact owned sessions use the existing project session list.
They have no Delete action in that list.

`ocq project work status PROJECT REQUEST` and the existing session-list read
project three separate facts for a standalone task:

- **Work finished:** the exact Work intent and `.task.json` marker identify the
  task. The prepared envelope and matching immutable SQLite receipt identify the
  exact job; its owned lane must match the envelope in the same database read
  transaction as the job state. `task_lifecycle.lane_id` identifies the existing
  inbox route (`lane:<lane_id>` and `integrator:<project_id>`). Until the receipt
  is observed, `work_finished` is null and no lane is projected. Then
  `work_finished` reflects the **current stored** job state (`finished`,
  `cancelled`, or `resolved`), with the exact outcome in `job.outcome`. It is
  independent of `job.terminal_proven`, which separately checks retained agent
  evidence on disk and still gates actions that require that proof. A stored
  `unknown` is not finished, regardless of historical events. The journal and
  evidence files are separate from the SQLite snapshot; this is not a live
  session check or validation of a deliverable.
- **Main handled:** `recorded` only after the exact selected Main acknowledges one
  incoming `work.submitted` through the opt-in tool below. Otherwise it is
  `unverified`. The receipt describes the particular result acknowledged at
  the time, not any later result or proof that Main understood it. Delivery,
  read/archive flags, replies, `lane.assigned`, and sender labels do not count.
- **Native wake:** for an attempted automatic wake, Work shows its retained
  native message ID and `uncertain`, `not_sent`, `rejected`, or
  `native_message_observed`. `unresolved` means the local attempt/receipt
  could not be matched. None of these is a Main-handled receipt.
- **Session archived:** Work status can show the local `archived` receipt or
  an `uncertain` attempt, but does not query live native archive state. See
  `project task-archive check` below. Archiving inbox mail does not archive
  a session.

### Opt in to one-inbox Main handling

Stop dispatch and coordinate replacement of queue clients that do not support
schema 8. Upgrade an existing schema 6 or 7 queue explicitly:

```sh
ocq --state-dir STATE project main-handled upgrade
```

The upgrade is additive. From schema 6 it installs the schema 7 scheduling
tables on the way to schema 8, **without** activating scheduling. The tool and
receipt lookup do not migrate the store. Only new standalone admissions after
this opt-in get a handoff pointer. Legacy Work, manual mail and historical
results are not enrolled or replayed. Schema 8 in this branch is an unpublished
candidate; do not upgrade a deployed queue with it.

Admission retains the exact request, job and intended Main setup, session and
binding revision with the existing job and owned lane. After the task has a
retained terminal proof, publish its snapshot through the existing result path
(`capture-result` or `record-result`, with their existing verification limits).
Finished is not publication and imported claims are not test validation. The
first result association seals the lane, deposits the original `work.submitted`
message, and records its outbox pointer in one transaction. It creates no
second copy or queue. If proof is missing, the whole first publication rolls
back. Inspect the exact association and inbox first after an uncertain response;
absence alone does not authorize repeating an uncertain external action.

`ocq project work status PROJECT REQUEST` shows `task_lifecycle.handoff`: use
its `request_id` and `inbox_id` together. `awaiting_result` means no publication;
`pending_main` means the original envelope is ready for acknowledgement, not
that Main is running or that an OpenCode input was sent. Read the original
message through `project inbox` (include archived mail if it was archived
manually). Reconnect by reading the same status again. A replaced binding or
superseded result appears as `dead_letter` with a reason on the original
record; no redrive, retargeting, or copied mail. An existing handling receipt
stays `handled`, even after replacement or supersession. Global completion
acknowledgements and inbox read/archive are independent of this handoff.

In **trusted local project configuration only**, load the
`share/opencode/ocq-main-handled` server plugin. Configure the canonical Main
checkout and a trusted installed OCQ helper, not a binary from a worker lane:

```jsonc
{
  "plugins": [{
    "package": "/absolute/path/to/ocq-main-handled",
    "options": {
      "helper": "/absolute/path/to/ocq",
      "state_dir": "/absolute/path/to/private-queue",
      "directory": "/absolute/path/to/canonical-main-checkout",
      "project_id": "proj-EXACT"
    }
  }]
}
```

Use an explicit OpenCode `ask` rule for action `ocq_main_handled` on resource
`msg-*` in Main's effective policy if human confirmation is required. The
plugin runs the native permission assertion on the exact incoming message
ID; it never replies to a permission request. Do not install this globally or
use worker-controlled plugin options. This requires the same locally managed
OpenCode V2 server that hosts Main, not an external/standalone server.

After actually handling the result within the user's instructions, Main can
call `ocq_main_handled` with `request_id` and `inbox_id` from the exact standalone
task and its existing incoming `work.submitted` message. This does not send
mail, wake Main, change inbox read/archive flags, allow integration, release
capacity, or archive a native session. A result that is not the lane's current
seal, a missing result, a nondedicated Work request, or a changed Main binding
revision is refused rather than inferred or retargeted.

The engine passes the acting session, assistant message and direct call IDs
separately from model input. The helper corroborates them via exact native
session and assistant-message GETs, then checks the selected binding and the
exact Work receipt, lane, association and inbox sequence in one SQLite write.
This is attribution of an explicit call in a cooperative same-UID system; it
does not prove comprehension, independent validation, or protect against
malicious plugins, local processes or synthetic API injection by the same user.

For uncertain output, use exact read-back only, using the native tool-call IDs:

```sh
ocq --state-dir STATE project main-handled receipt PROJECT REQUEST INBOX SESSION MESSAGE CALL
```

Matching committed invocation evidence returns the original immutable receipt;
another invocation cannot replace it. Absence does not prove a delayed commit
cannot occur and does not authorize resend. The native viewer and generic
inbox commands remain inspection tools, not actor proof.

OpenCode V2's `/api/event` stream has no replay cursor. Typed execution,
tool, idle or archive events can only suggest a fresh, bounded read of the
stored handoff. Subscribe and reconnect by reading the committed OCQ snapshot
again; the existing observer already does this and observes the SQLite
publication and acknowledgement directly. A native listener would not
observe an additional authoritative transition, so this flow does not add one.

### Opt in to cooperative automatic wake and archive

After explicitly installing schema 8 and configuring the Main tool for the
project, opt in for one project for the lifetime of an agent-enabled dispatcher:

```sh
ocq --state-dir STATE dispatch --auto-task-handoff PROJECT
```

This enables **both** wake and task archive, including for previously published
eligible standalone handoffs in that project. Without the flag neither runs
automatically; `--commands-only` refuses it. The dispatcher advances one
stored handoff at a time at its bounded cadence, without a new service, native
event subscriber, second queue, or duplicate result body. Main is not an OCQ
job: the wake checks observed free queue capacity and memory headroom, but
does not reserve a slot for Main or limit its resource usage.

Wake uses the [V2 prompt API](https://opencode.ai/docs/api), with `delivery:
"queue"` and `resume: true`. It sends a fixed `ocq-task-handoff-v1` marker and
only the two existing tool pointers, never result contents, a user brief or a
permission answer. It checks the exact retained binding, selected session,
active role/profile and original sealed handoff, then observes stable idle
history (or a pristine session), no native inbox/forms/permissions and no
owned resources observable through the supported APIs. Observed activity or
unavailable evidence defers before arming. The project journal retains one
native message ID before POST. Exact consumed-message GET, not a successful
POST or queued input, records `native_message_observed`. A documented rejection
or ID clash does not authorize resend. A lost response may remain `uncertain`;
subsequent passes can only read that exact native identity. Main must still
inspect and explicitly acknowledge via `ocq_main_handled`. Neither a wake
receipt nor that call implies inbox read flags, a reply, validation or permission
to integrate.

After the Main-handled receipt, the same dispatcher may archive the exact finished worker
through the check and journal described below. It requires the original
still-current result and Main binding, retained and fresh worker proof, and
closed/unattempted viewer evidence as supported by the local archive path.
Uncertain viewer history holds. Automatic and manual archive use the same
single attempt and GET-only reconciliation.

**This is cooperative, not atomic.** Another native client can add input, start
execution, create a request or resource, change the session, open a viewer,
or archive/restore between observation and effect. A separately submitted SQL
binding or result change can also race the final check and native write.
The project journal serializes OCQ's attempts and its ordinary local Work/Main
actions, not independent native writers. V2 `queue` orders input, but is not an
idle predicate. Native read-back proves observed state, not who caused it or
that the preconditions remained true. Leave the flag off where you cannot
accept these windows. No synthetic messages, permission/form answers,
deletions, automatic viewer closures, or effect retries are used.

### Explicit local archive

For one finished standalone task on the current admission binding, use:

```sh
ocq --state-dir STATE project task-archive check PROJECT REQUEST SESSION
ocq --state-dir STATE project task-archive confirm PROJECT REQUEST SESSION PREVIEW_SHA256 --confirm-exclusive-window
```

Review the exact session and Main-handled receipt on `check`. Confirm only
while you can keep an exclusive window: no other native clients or writers,
viewer opens, or OCQ changes affecting this task. The local CLI refuses any
relevant armed or attempted OCQ viewer history because it cannot prove exact
historical HC closure; a missing viewer record is not closure. An unattempted
pointer alone is not a viewer launch.

Archive requires the original immutable Main-handled receipt and still-matching
sealed result, retained terminal ownership and transcript, and fresh native
idle, request and resource observations. The preview binds the queue, current
binding, original acknowledgement, viewer history and exact native service,
session and transcript. The project journal durably consumes one attempt and
rechecks the preview before sending only `archived: true`. The native API
offers no conditional ownership or idle check: this is **cooperative, not
atomic**. The manual path still requires explicit exclusive-window confirmation;
the opt-in dispatcher instead uses the cooperative window described above.

Success requires exact native `time.archived` and unchanged transcript
read-back, plus a durable local observation receipt. If the attempt is
uncertain, `project task-archive reconcile PROJECT REQUEST SESSION PREVIEW_SHA256`
may observe that same state using GET only; it cannot send another PATCH.
The receipt says what was observed, not which writer caused it, and it is
historical rather than a live check for a later restore. This does not delete,
close a viewer, change Main, integrate a result, mutate mail or release queue
capacity. It preserves session history and exact access by ID.
