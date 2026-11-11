# Herdr workspace

The OCQ Herdr panel shows stored projects, jobs, results and inbox mail. It can
explicitly acknowledge mail, compose local lane updates and submit local commands/agents
or project intake after preview and confirmation. It is not a scheduler or a live OpenCode
permission client. Queue also offers an explicit local commands-only dispatcher
lifecycle, separate from submission.

## Install

The installer requires a running **herdr-app 0.1.14** and an initialized app home.
It checks that exact version, validates the mod, refuses to overwrite an existing
OCQ mod or `ocq-workspace` layout, and requests activation of a separate personal
layout. It does not merge or replace the previous layout.

Build the bridge on the machine whose queue it will open:

```sh
mkdir -p "$HOME/.local/bin"
GOMAXPROCS=2 go build -p 1 -o "$HOME/.local/bin/ocq-herdr" ./cmd/ocq-herdr
```

Put `ocq-herdr` on that **Herdr core's noninteractive PATH**. Starting an
interactive shell or exporting PATH there does not configure a service. Start
the app once, then install the mod/layout on the display machine:

```sh
sh share/herdr/install.sh "$HOME/.config/herdr-app" "$(command -v herdr-app)" --activate
```

Supply the actual absolute app home if customized. If installation or the layout
switch fails, inspect retained files and the window before retrying; the
installer does not overwrite or clean up partial installation. Switch back with
Herdr's layout controls.

Update mod and bridge together. **Before upgrading, stop older panel/bridge
processes and ensure no delayed old `compose-dispatch` can start later.** Older
bridges do not require the durable claim; mixed versions are unsafe.

## Which machine runs what?

| Display machine, such as a laptop | Execution machine, local or remote |
| --- | --- |
| `herdr-app`, OCQ mod and layout | Herdr core (`hc`), `ocq-herdr`, private OCQ state |
| Selects a space/machine and presents observations | Runs bridge reads against the selected queue |
| Does not own scheduling or agent permissions | Independent OCQ dispatcher, OpenCode and Workmux handle execution |

These can all run on one machine. For remote selection, install the bridge on the
server core's PATH, not only the laptop. The bridge inherits `OCQ_STATE_DIR` or
uses the standard XDG/HOME state location on its execution host. It requires an
existing private schema-5 queue; reads never initialize or migrate it. The panel
does not install/configure the server, OpenCode or Workmux. Its explicit local
dispatcher control uses the fixed command-only policy described below.

Selected-machine read routing is supported. **Remote command/agent submission, compose
and project intake are disabled.** Real multi-machine deployment, disconnect recovery and live
permission handling are not yet end-to-end verified. A disconnected view never
means execution stopped or permits a retry.

## Inspect and acknowledge

The panel selects a unique project matching the active space's repository or
offers a picker. Project views refresh every 15 seconds. Latest inbox/completion
windows contain 20 items each; older history is paged separately. Each refresh revisits
one loaded historical page, so large histories update across multiple cycles.
These are separate stored reads, not a coherent live stream.

The Queue view reads jobs across projects, including standalone jobs, in pages
of 20. It shows the first 20 pending global events and accepts exact job/event
IDs, including acknowledged events. Job detail offers stdout/stderr reads in
chunks of at most 32 KiB. Log bytes are escaped rather than interpreted as terminal
output; EOF describes that read, not job completion. Queue jobs/events are read
when the view opens or its machine context changes; use its explicit Refresh
action for later updates. They do not use the project-view timer. Queue inspection
does not acknowledge events or change execution.

Opening mail does not mark it read. Read and separately confirmed archive actions
retain pending local intent, make one mutation attempt, then read back the exact
message. Unavailable/unmatched read-back stays uncertain: recheck, do not resend.
Archive is permanent but retains content. Inbox acknowledgement neither releases
capacity nor acknowledges global completion notifications.

Global completion acknowledgement is a **separate, local-only** action in event
detail. Review it, then confirm once; remote selections disable acknowledgement.
It removes the event from the global pending list without changing the job or
capacity. Its durable prepared/armed/attempt journal binds the originating space,
queue, event, job and exact decimal sequence. One call is followed by exact event
read-back, never automatic retry. After a lost response or reload, use the job's
retained event link or exact event lookup in the original space, then **Check
exact event and journal**. Armed/uncertain states permit checks only. See
[acknowledgement recovery](notifications.md#herdr-acknowledgement).

Unknown job rows show only the last stored request reason. Permission and form
are not distinguished; the observer records no timestamp for these reasons.
Session inbox input is not necessarily a human request. Panel read time is not
request freshness, and absence of a warning proves nothing about current requests.
Use the recorded session/location to find the exact session in OpenCode. These
stored request views do no live lookup, navigation or permission reply. Live
agent observation requires the separate confirmed reconciliation action below.

With panel keyboard focus, Up/Down selects actions, Enter opens them, Left/Right
pages through inspected fields, and Escape returns. Stable IDs preserve selection
across refresh. Long fields have continuations; original manifest text is also
available. Values outside JavaScript's safe integer range are flagged, not shown
as exact. Intake's final confirmation specifically requires a pointer press.

## Cancel queued work

In Queue job detail, review queued-job cancellation, then confirm it separately.
This action is **local-only** and accepts only jobs still `queued`, including
queued agent jobs. It does not stop launching/running work, resolve unknown
ownership or cancel an agent process. Remote cancellation is disabled.

The durable intent binds the original space, queue identity, job ID, exact decimal
receipt sequence and displayed `queued` state. Confirmation must acknowledge a
durable claim before one attempt; its irreversible marker is synced before the
queued-only write. If admission wins the race, cancellation is refused and the
actual noncancelled state is preserved, not reported as cancellation.

Cancellation is observed only from a consistent read of the exact job and its
matching immutable `cancelled_before_launch` completion event. This does not prove
which caller caused it or acknowledge that event. After a lost response or
reload, reopen the exact job in the original local space and use **Check exact
job, completion and journal**. Prepared intents can be reviewed again; armed or
uncertain intents permit checks only, never automatic retries—even if the job
still appears queued. Missing/unmatched read-back remains uncertain. A call
already launched may finish after leaving the view.

## Resolve unknown commands

Queue job detail offers **local-only** resolution for `unknown` command jobs,
never agents. Stop dispatch yourself and inspect the exact process/group and
side effects first; the panel does not stop the dispatcher or kill processes.
The existing [command-resolution checks](command-core.md#recover-unknown-commands)
still require the dispatcher lock and established process/group exit.

Enter private, nonempty UTF-8 evidence (at most 16 KiB, no NUL), prepare it, then
review the exact retained reason/evidence before separate confirmation. The full
stored reason must also fit 16 KiB. The panel sends evidence over an authenticated
local connection, not process arguments; bridge status returns its digest rather
than its text. Evidence is retained in the private queue and available through
CLI job status.
The intent binds the original space, queue, job, decimal receipt sequence,
command/unknown observation, reason digest and exact evidence payload.

Confirmation durably arms one attempt. The attempt marker is synced before the
resolver call; lost responses or refused attempts never authorize automatic
retry. Reopen the exact job in the original space and use **Check exact job,
completion and evidence digest**. Resolution is observed only when the stored
evidence digest, `resolved` / `operator_reconciled` job and immutable completion
match. Changed or unavailable observations require investigation, not a success
claim. Prepared evidence can be reviewed again; armed, uncertain or refused
attempts are check-only. An already-launched call may finish after leaving.

This records an operator assertion and may release eligible unknown capacity;
it is not execution success or test validation. Remote resolution is disabled.

## Reconcile agent jobs

Queue agent detail offers a **local-only**, separately reviewed and confirmed
live API observation. It requires the exact recorded session/worktree and an
admitted job (`launching`, `running`, `unknown` or `finished`). Stop dispatch
yourself first: reconciliation needs the dispatcher lock and never stops it
automatically. It does not prompt, resume, send input, reply to permissions, kill
or cancel processes, create sessions or adopt worktrees.

Preparation retains the original space, queue identity, job/decimal receipt
sequence, session/worktree and requested stored snapshot. Confirmation durably
arms one attempt before the existing core observer runs. Observation may update
local lifecycle/evidence and retain unknown/blocked state; capacity is released
only when the existing session/tool/resource completion policy proves completion.

There is **one retained authorization per receipt**. After a lost response or
reload, reopen the exact job in the original space and use **Check operation
receipt and stored job (no live API)**. Recovery reads only SQLite/filesystem
state, never reruns the observer. Armed, changed or uncertain intents are
check-only; another space or location cannot create a replacement authorization.
An already-launched observation may finish after leaving the view.

The operation receipt records the requested snapshot, resulting snapshot and
observer return/error or dispatcher-lock refusal. It is not agent success or
deliverable validation. Current stored data may have changed since that receipt;
a terminal job or frozen completion event alone cannot prove this operation ran.
Missing/unavailable receipts remain uncertain, with no automatic retry. Remote
reconciliation is disabled.

## Publish an imported result

From a stored project job, open **Import / publish result manifest**. This JSON
entry form accepts an exact version-1 `result` for an implementation job or
`review_result` for an existing review job, at most 16 KiB. It is not a guided
manifest builder. Enter an optional exact supersedes association ID; lineage is
immutable. Publishing a review result does not create or submit a reviewer.

Preparation validates the core linkage rules and retains the exact bytes over an
authenticated local connection; it does not publish. Review the structured field
pages and optional reversible raw JSON-string view, then open the separate
effects confirmation and use its **pointer-only final press**. Unsupported review
representations block publication rather than silently omitting fields.

The journal binds the original space, queue/project/job/receipt, ownership,
manifest bytes/digest and supersedes link. Confirmation durably arms one attempt;
the attempt marker is synced before the core write. The core commits the
manifest/association, lane-sealing update and `work.submitted` or
`review.submitted` inbox mail in one transaction.

After a lost response or reload, reopen the same job, **Read retained publication
intents**, select the exact manifest digest and **Check exact stored publication**.
Recovery matches the association, stored raw bytes/digest and supersedes linkage,
not generic job state or client return. Armed/uncertain intents are check-only;
missing or mismatched read-back never permits automatic resend. A matched
publication does not establish which caller caused it.

Imports remain `structure_hash_linkage_only`, with claimed validation kept as an
assertion, not machine verification or a test run. This flow does not execute or
reconcile agents, capture files or authorize integration. Remote imported-result
publication is disabled. Machine capture uses the separate reviewed flow below.

## Capture and review a machine snapshot

For an implementation job, open **Prepare / publish machine snapshot** and check
the exact project/job/receipt, owned lane, worktree and pinned base. A stored owned
implementation worktree is required; there is no arbitrary-path fallback. Enter
ignored/external exclusion descriptions as a JSON array (default `[]`) and an
optional exact supersedes association ID. Exclusions are descriptive notes, not
filesystem filters: they do not change which files the core captures.

**Capture once and retain for private review** persists a capture ID and scoped
request before reading the checkout. The core compares before/after snapshots,
then privately retains the exact machine-generated manifest without publishing
it. Review the structured fields and reversible raw JSON-string view, including
paths/content/diff, exclusions and limitations, before the separate pointer-only
confirmation.

Confirmation durably arms one publication attempt. It publishes the **same
retained bytes**, even if checkout contents have changed since preparation;
ownership/linkage checks still apply. No publication step recaptures files. The
association, lane seal and `work.submitted` mail commit atomically.

After a timeout or reload, reopen this capture flow for the same job, read the
retained publication intents, select the **capture pointer** and check the exact
stored publication. Checks read the journal/association, not the checkout. A
`preparing` pointer without a completed snapshot remains uncertain—never recover
it by silently recapturing. Armed/uncertain publication is check-only, matching
the exact association, manifest bytes/digest and supersedes link without resend.

`machine_verified_snapshot` means a cooperative point-in-time snapshot was
captured and linked. It does not establish test validation (`not_run`), a current
tree match or integration authority. Use the separate [fresh Verify action](#verify-an-owned-worktree)
to compare the checkout afterward. Remote capture is disabled.

## Verify an owned worktree

From a stored project job or completion result association, open **Verify owned
worktree**, check the project/job/association/result digest and recorded owned
path, then choose **Verify filesystem now**. Opening the screen or refreshing
stored views does not run the check. The action is local-only; remote checks and
checks without a recorded owned worktree are disabled. There is no arbitrary-path
entry, manifest-path fallback or adoption of another checkout.

The bridge rechecks that exact association and owned implementation worktree.
Only available `machine_capture` / `machine_verified_snapshot` results qualify;
imported structure/hash/linkage claims are rejected without a filesystem check.
This comparison never promotes imported claims to machine proof.

The result shows attempt/check timestamps, match or drift and a difference count.
An attempt timestamp alone is not a completed check; missing/unavailable worktrees
and errors establish no match. Even `matches_snapshot` is point-in-time evidence:
files may change immediately afterward. Validation remains the stored core status
and integration remains `not_authorized`. This is a filesystem read, not job/test
execution, result import/capture, reconciliation or integration authorization.

## Submit a general command

From the local **Queue** view, choose **Submit general command / recover
admission…**. No project is required or associated. Supply an explicit immutable
key, executable name/path, JSON string array of arguments **excluding the
executable**, working directory and nonnegative decimal memory bytes. Use `[]`
for no arguments; memory `0` declares zero demand but does not bypass the queue floor.

**Resolve exact command draft (no admission)** resolves cwd/executable paths and
builds canonical v1 bytes in Go. Bare executable names use the bridge's PATH;
relative executable paths containing `/` resolve against cwd. Review all escaped
draft pages, then **Prepare these exact bytes (no admission)**. Open **Review exact
retained command** and sequentially review every field, argv entry, canonical
request and digest before **Review admission…** and pointer-only confirmation.
Typing, draft generation and preparation do not admit work.

Confirmation rechecks retained bytes, durably claims the intent and makes one
admission attempt. After a lost acknowledgement or reload, return to the same
local Queue/space, **Refresh journal (no retry)** and **Check exact receipt (no
retry)**. Recovery matches the immutable key and exact v1 bytes without resolving
paths again, even if paths have changed or disappeared. Missing/conflicting
receipts remain uncertain; never resend automatically. Receipt matching proves
admission, not successful execution.

Admission only queues work, but a separately running dispatcher may execute it
immediately. The form never starts a dispatcher or inserts a shell; an interpreter
runs only if explicitly chosen. Paths remain mutable and contents are not
snapshotted; resolution proves neither executable suitability nor safety. Remote
command submission is disabled.

## Submit a standalone agent

From local **Queue**, choose **Submit standalone agent / recover admission…**.
No project, binding or lane is required or associated. In **Guided form**, enter
an immutable key, repository, explicit base commit selector, complete nonempty
UTF-8 prompt (≤16 KiB), exact agent, provider/model, optional variant, memory bytes
and nonempty ordered permission JSON (`action`, `resource`, `effect`). Go resolves
the repository and pins the supplied base before review; it guesses no HEAD,
profile or permissions. Blank variant means omitted. Memory is decimal text parsed
as a nonnegative int64, without JavaScript number rounding; `0` is declared demand,
not a runtime limit. Provisioning policy is the fixed core policy.

**Exact JSON import** takes `{"key":"…","request":"EXACT canonical v2 JSON"}`;
the request may also use the supported exact object/wrapper representation.
Go validates without rewriting imported request bytes. The original import is
preserved; all canonical fields must already be complete. Invalid, oversized or
noncanonical inputs are rejected, not repaired.

Review every escaped draft page, prepare those exact bytes, then separately open
and sequentially review the retained request before pointer confirmation. Review
includes the full prompt, ordered rules, model/variant, exact memory, provenance,
canonical v2 bytes and digests. Typing/building/preparing admits nothing.
Confirmation requires an acknowledged durable claim before one admission attempt.
Fresh admission validates repository/base; matching receipt recovery is first and
offline, using the exact key/bytes even when the repository is unavailable.
After a lost response, return to the originating local Queue/space, refresh the
journal and check the exact receipt—never automatically resend. Missing or
conflicting receipts remain uncertain.

Intake does not check live API, agent or model availability, provision a session
or worktree, send a prompt, answer permissions or start dispatch. A separately
running dispatcher may execute admitted work immediately. Queued acceptance
proves neither runtime readiness nor execution success, validation or sandboxing.
Remote admission is disabled. Project intake remains separate, including its
binding, target and reviewer/runtime/pre-prompt gates.

## Start, inspect and drain a command dispatcher

In local **Queue**, open **COMMANDS-ONLY dispatcher · start / status / drain…**.
Check the displayed queue, space and configuration: Start may execute existing
queued commands immediately, and later submissions while it runs. Policy is fixed
at **2 slots, 6 GiB admission floor, commands-only**. A FIFO-head agent blocks
commands behind it; unknown demand retains capacity. No agent/API polling or
permission handling is enabled.

Explicitly select a positively identified live model pane in the current local
space, then **Prepare fixed commands-only intent · NO launch**. Review and confirm
Start separately. After an acknowledged durable claim and one launch-attempt
marker, the panel opens a **right split** running the Go helper directly—not an
exec child, new tab or shell wrapper. Exact process/terminal/space/tab/pane binding
to the retained `born` record and singleton-linked `ready` evidence are required
before command admission begins.

Reopen this control and **Read exact retained status only** after a lost response
or reload. Reads never launch, bind a replacement or retry. Unknown lifecycle
remains unknown; no automatic adoption, restart or stop of an unowned dispatcher
is allowed. Process state or exit is not job completion or capacity release.

For a positively owned ready dispatcher, **Prepare / review separate DRAIN…** and
confirm once. The durable drain claim/attempt sends TERM through a verified pidfd,
stopping admission and waiting for direct owned commands. Keep the terminal open
until actual exit; returning to Queue does not stop dispatch. A launch handoff
already in progress may finish, and a stuck command may delay exit indefinitely.

**Closing the native terminal, pane or space is not Drain.** Closure can escalate
through HUP/TERM to force-kill in about one second. HUP stops admission, but there
is no guaranteed grace, global close interception, core-restart supervision or
escaped-process containment. Missing exit evidence never authorizes job retry or
capacity release. Only proven stopped/cancelled lifetimes can be retired, retaining
history; retirement does not restart anything. Remote controls and agent-enabled
dispatch are disabled. For optional service supervision, see [systemd](supervision.md).

## Local compose and intake

Local compose offers guided **lane.blocked** and **lane.assigned** mail from a
selected message-owner lane and its exact stored owner job:

- `lane.blocked`: that lane or owner job → `integrator:PROJECT`.
- `lane.assigned`: a retained integrator sender or recorded binding session
  identity → any retained lane in the same project. The recipient may differ
  from the message-owner lane. No live session identity is guessed.

Enter subject/body and optional same-project retained manifest digest and reply
message ID. Choose the stored lane's default worktree metadata (empty stays empty)
or a nonempty literal override. Paths are metadata only; compose opens or creates
no worktree. Blank attachment/reply fields mean no link. Stored links and exact
ownership are checked before preparation and rechecked when sending.

Typing does not send. **Prepare retained message (no send)** privately retains
the request and a generated idempotency key. Open every escaped retained-field
page sequentially, including complete subject/body, routes, attachment/reply,
requested/default/effective path and request digest; then separately confirm.
Confirmation requires an acknowledged durable claim before one send attempt.
Recover uncertainty by exact receipt read-back, never automatic resend. The
preview covers retained bytes, not merely current form text; navigation does not
prove comprehension.

Only these two kinds can be authored. `work.submitted` and `review.submitted`
remain automatic result-publication mail. Routing identities are metadata, not
authentication, permissions or execution approval. Remote compose is disabled.

Project intake offers **guided registration, implementation and review forms**
and byte-preserving **Advanced raw** import. All intake is local-only and checks
space, machine, folder and project; submissions also pin the binding revision.

Choose **Register a new project** for creation (expected revision 0), or
**Registration** in the selected project context for rebind at its displayed
positive revision. Confirm the explicit project ID, key, label, non-secret
provenance reference and matching repository/session directory, and supply the
full commit and exact coordinator session/OpenCode project IDs. Rebind must
preserve the stored label. No HEAD,
latest-session lookup, session creation or inferred new/rebind operation occurs.

The form takes handoff intent plus one optional decision and one optional risk.
Multiline text stays one entry; use Advanced raw for multiple entries or additional
handoff fields. **Build generated preview** uses Go to encode the handoff,
registration envelope and exact handoff digest and validate their structure.
Typing/building does not register, rebind, prepare an intake intent or submit work.
Review the generated pages, choose **Prepare these generated bytes**, then review
the retained request again before the separate pointer confirmation below.

Advanced raw preserves the original supplied JSON bytes; it is not rebuilt by
the form. Typing there does not prepare or dispatch. New registration requires
an explicit project ID in either panel input mode.

For implementation, choose **Submit implementation job**. Confirm the bound
project/revision and repository; enter an explicit key, full base commit, exact
agent, provider/model and optional variant, memory demand and ordered permission
rules. An empty optional lane ID uses the core's deterministic key-derived lane.
No execution profile is inferred or permissions relaxed. Supply the exact prompt
and brief goal, scope, exclusions, acceptance criteria, allowed actions and context
references. Brief claims are not validation; allowed actions do not grant permissions.

For review, select an available implementation result and choose **Submit review
job for this result**. The association, target job/result digest and binding
revision are pinned. Enter the execution profile, criteria and rules. Go builds
the deterministic prompt from the full immutable result diff and changed-file
contents, never a summary or caller substitute. Prompts exceeding 16 KiB are
rejected, not truncated. The existing core gate checks target linkage, the exact
reviewer, OpenCode server
2.2.6 and ordered read/glob/grep policy at fresh intake and again before prompting.

Guided reference input accepts only digests of existing stored manifests from the
same project. Go embeds their full bytes in digest order, rejects duplicates and
automatically includes the review target (do not add it again). Advanced raw can
supply other supported full-manifest representations; digest-only envelope
references are not accepted. Core validators remain authoritative.

**Build generated preview** generates the request, brief, digests and envelope
without queue admission or a prepared intent. Review all generated pages, then
**Prepare these generated bytes** and separately review the retained request.
Authenticated retained preview
shows the envelope bytes/digest, structured fields and the core parser's exact
request, brief and each referenced manifest's original bytes/SHA-256. Referenced
claims do not become machine or test evidence merely by inclusion.

Wrappers, unsupported fields and unsafe/lossy representations still block
preparation; retained unsupported inputs are raw-view-only with no dispatch.
Navigate every structured field/array/embedded-byte page sequentially, then open
the separate confirmation and use its pointer-only final press. Navigation is
not proof of comprehension. The exact retained envelope and context are rechecked
before durable arm and one dispatch attempt. Recover uncertain outcomes through
exact receipt read-back, never automatic resubmission.

Acceptance creates an exact receipt and, for submission, a queued job, lane and
assignment mail. A queued review is not reviewer execution, a completed review or
validated work. The panel does not itself execute the agent, but an already-
running dispatcher may start newly queued work immediately.

## Mutation recovery

Compose and intake each retain one unresolved intent in a private local journal.
Confirmation durably **arms** the exact intent. Only after acknowledged arm may
one dispatch persist its irreversible attempt marker and call the core.

- Prepared or armed intents may be explicitly cancelled before an attempt, under
  the journal lock. A delayed dispatch must recheck the claim and is then refused.
- A recovered arm may already have a delayed dispatch scheduled: never dispatch
  it again automatically.
- After attempt, use exact receipt reconciliation. Missing, mismatched or
  unavailable receipts remain uncertain and do not allow cancellation or retry.
- Only cancelled or receipt-reverified completed intents can be retired; retirement
  retains evidence. No flow automatically resends uncertain operations.

The bridge uses bounded, mutually authenticated one-shot loopback connections for
private request preparation/preview, not request bytes in process arguments or
bridge output. Capabilities and request contents must not be logged. Direct core/
bridge callers are responsible for obtaining human confirmation.

Journals check queue device/inode, not in-place database restores. Never replace
or retarget database paths concurrently; checks do not pin SQLite's file across
path races. The mod and bridge are trusted code with the user's privileges, not
a hostile-user security boundary.

## Interface coverage

Business logic is centralized in the Go core. Both CLI and bridge call it
directly; this table describes interface coverage, not separate implementations.

| Capability | CLI/core | Herdr panel |
| --- | --- | --- |
| Command submission | `submit` | Local Queue form, exact retained review/confirmation and immutable receipt recovery |
| Standalone agent submission | `submit-agent` | Local Queue guided profile or exact JSON import; retained review/confirmation and offline receipt recovery |
| Global queue and logs | `list`, `status`, `logs` | Read-only job pages, exact lookup and bounded stdout/stderr |
| Queued cancellation | `cancel` | Separately confirmed local queued-only cancellation and read-back recovery |
| Unknown command resolution | `resolve` | Local private evidence entry, retained review/confirmation and exact read-back |
| Agent reconciliation | `reconcile` | Separately confirmed local API observation; stored operation-receipt recovery without rerun |
| Dispatcher controls | `dispatch` | Local fixed commands-only Start/status/owned TERM Drain; remote and agent-enabled controls disabled |
| Completion notifications | `notifications`, `ack` | Pending/exact reads, retained job-to-event links and separately confirmed local global ack; remote ack disabled |
| Project registration | `project register` | Local new/rebind form or byte-preserving Advanced raw; retained review and pointer confirmation |
| Project submission | `project submit` | Local implementation/review forms or Advanced raw; full supported references, retained review and pointer confirmation |
| Projects, jobs, lanes, manifests, results and inbox | Project read commands | Stored inspection and paged history |
| Imported result publication | `project record-result` | Local JSON `result`/`review_result` entry, exact retained review, pointer confirmation and association read-back |
| Machine result capture | `project capture-result` | Local owned snapshot preparation, retained review/pointer confirmation and exact publication recovery |
| Live worktree comparison | `project verify-worktree` | Explicit local comparison of an exact machine-captured result and stored owned worktree |
| Mail | `project mail-send`, `mail-ack` | Read/archive and local guided blocked/assignment mail with retained review and exact receipt recovery |

The panel limits sender/recipient choices to supported stored routes, not arbitrary
identities or message kinds. Human permissions deliberately remain in OpenCode.
Git apply/commit/merge/push is not an automatic OCQ feature.
See [project contracts](project-control-plane.md) for the shared operations.
## Matched mod/bridge compatibility and quiesced upgrades

The mod requires the exact `ocq-herdr-local-v1` protocol, journal claim contract,
and **every per-action semantic value** advertised by the non-initializing
`ocq-herdr compatibility SPACE FOLDER` endpoint. Endpoint presence or a matching
version string alone is insufficient. Missing, malformed, incomplete, timed-out
or different action semantics leave all mutation controls disabled. Stored queue
and project inspection, existing journal status/rendezvous reads, worktree
verification, copy, and explicit human-handoff GET verification retain their
existing read/observation contracts. Journal observations may open their existing
private lock files; the compatibility endpoint itself does not initialize a queue
or journal. A missing supported queue cannot enable mutations.

The check is bound to the selected local machine/space/folder, existing queue
identity and device/inode, state-directory context and executing bridge image.
Changing the selected context invalidates it. Each action process rechecks the
exact protocol, image and queue context before entering its action, using safe
`--mod-*` fields; unknown older bridges reject those flags. The shared mod adapter
requires the original callback context immediately before each mutation. Native
dispatcher launch additionally rechecks compatibility before opening a terminal;
its helper retains the existing immutable-v1 intent, exact image/argv/native
ownership and bind checks. Read/status/reload never bind or launch a helper.
An established view remains pinned to that bridge image and queue incarnation
across read-only rechecks, including failed checks. Recheck cannot silently rebind
old project/mail/intake views to a new queue. Quiesce and reopen the panel (or
leave and re-enter the selected context, which clears the old views) after a
deliberate bridge/context change.
Private authenticated exchanges additionally retain their original authorization
snapshot: any compatibility recheck invalidates an in-flight exchange, even if
the next handshake matches. The client checks that snapshot and its original UI
context immediately before authentication and each payload/framing write. It
closes on invalidation without retry. This stops future client writes; it cannot
revoke a receiver's already-issued capability server-side, retract bytes already
written, or undo a preparation/capture already authorized at authentication.

Coverage includes mail acknowledgements and compose/send, project drafts/intake,
global notification acknowledgement, queued cancellation, command resolution,
agent reconciliation, standalone command/agent drafts/admissions, imported result
and machine-capture publication, dispatcher preparation/claim/bind/start/drain,
and cancellation/retirement/recovery operations that write journals. Existing
versioned immutable receipts and journal validation remain authoritative; no
schema rollback, journal rewrite, repair or automatic mutation retry is offered.
These are distinct action contracts, not a claim that every action arms or sends:
draft/preview never dispatch; the arm/attempt families retain their existing
durable one-claim/one-attempt rules and exact receipt recovery. Mail acknowledgement
retains its flushed client ledger, sequence check and read-before-archive rule;
it does not acquire a new core arm protocol merely by passing compatibility.
Compatibility checks do not change journal meanings or serialization. Future
claim/receipt semantic changes must version or refuse the affected durable
journals as well as change the action contract; a new handshake alone is not a
journal migration or permission to replay an older uncertain operation.

Build identity is the actual executing binary's SHA-256, labelled
`local-development`. This is not a claim that the binary was built from a clean
Git commit, a signed release, or a verified source tree.

**Quiesce before upgrading.** Close old panels and stop launching new actions;
let already-launched bridge processes/private receivers finish and establish that
no delayed old operation can still act. If that is unknown, keep the upgrade
blocked; a closed panel or retained journal alone does not prove quiescence.
Drain owned dispatchers and verify their actual terminal/resource exits. Preserve
the queue, immutable receipts and journals. Only then arrange a matched bridge
binary and mod release, reopen the panel in the intended context, and use the
read-only compatibility recheck before authorizing new work. The installer still
refuses to overwrite existing files; it does not perform upgrades or quiescing.

A handshake does **not** lock an executable or environment. Replacement after
checking can race native process launch (the helper's existing image check then
refuses admission). Old already-running panels, old bridge processes, and delayed
operations already launched are not retroactively fenced by new code. A matching
handshake does not make their pending journals safe to replay or resolve unknown
outcomes. Quiescing remains required. Remote mutation controls are disabled; this
is compatibility protection, not authentication, signed updates, a transport
manager or a second scheduler.
The bridge refuses headerless mutation calls, but cannot stop an old
panel using an old still-executable bridge. Do not replace/retarget queue files
concurrently or use a database/schema rollback to bypass incompatibility.

## Local human OpenCode handoff

An agent's stored job view offers exact recorded session/worktree handoff details
and an explicit clipboard copy. Refreshing the view reads stored SQLite state
only. This is an intentional inspect destination, not a command or proof that
the session still exists. Stored request reasons do not distinguish live
permissions from forms. Humans use the actual OpenCode interface for approvals;
copying or verifying a handoff does not resolve a request or prove completion.

This action does **not** launch a client or replace a pane. Installed
OpenCode 2.2.6 documents `--session` as continuing **or creating** an absent
session. No existing-session-only exact-service isolated navigation route has
been validated here, so the UI reports that limitation rather than emitting a
launch command or unsupported link. Remote handoff is disabled.

Optional explicit read-only verification requires `OCQ_HANDOFF_SERVER_URL` in
the local bridge environment: a credential-free `http://127.0.0.1:PORT` or
`http://[::1]:PORT` origin with no trailing path, query or fragment. Configure it
only for the intended existing service. Nothing discovers, starts, pairs or
reconfigures a service when this setting is absent. The UI receives only a safe
service label and origin fingerprint, never the endpoint or authentication.
The supported `opencode --server ORIGIN api get /api/session/EXACT_ID` path keeps
the existing CLI authentication environment unchanged. Credentials must not be
put into the URL or command arguments.

Verification is a separate human click with one shared 15-second command deadline,
up to 250 ms of inherited-pipe drain after exit/cancellation per command, and
256 KiB of API output. Pipe-drain failure refuses verification; it is not proof
that an escaped CLI descendant stopped. It requires installed client 2.2.6 and
compares the returned exact
session, directory, project, profile, permissions and private ownership metadata
with the selected local queue/job/session/worktree. A mismatch, missing session,
changed configured origin or unavailable authentication never falls back to
another service/session and never creates or resumes anything. A successful
read is a snapshot, not continuing availability or completion. No prompt,
execution intent, permission response or queue mutation is made.

The opt-in `proof/herdr-agent-handoff` scenario uses a fresh isolated real
OpenCode service and empty exact session, synthetic imported queue ownership
(not execution), and native Herdr callbacks/clipboard. It must run beneath the
launch-time `proof/herdr-command-dispatcher` cleanup owner; kernel-owned repeated
inventories and final empty inventory plus `wait4(ECHILD)` certify shutdown.
