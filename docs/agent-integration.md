# Experimental agent adapter: session-scoped cooperative tools

**Do not deploy or infer general agent-tool support.** The bounded real experiment
provisioned a Workmux worktree, created an exact shared-service OpenCode session,
sent one preallocated-ID prompt, and killed only the dispatcher while that session
was active. The initial stronger causal-provenance policy kept the job unknown
because of a synthetic Plan reminder. The user explicitly corrected that scope:
normal system/synthetic events are auxiliary context in the job's dedicated
session, not an upstream provenance prerequisite. API-only reconciliation of the
same retained job now records **finished / agent_succeeded**, durable output,
`exit_code=null`, `validation=not_run`, and released capacity. Nothing was resent.

## Verified contracts

Inspected official [V2 API docs](https://opencode.ai/v2/docs/api),
[published OpenAPI](https://opencode.ai/v2/openapi.json), installed `/openapi.json`,
`opencode run --help`, `opencode api --help`, and Workmux's installed headless docs.
Then verified actual responses on explicitly owned disposable resources:

- OpenCode **v2.1.4**; Workmux **0.1.276**. The service's OpenAPI describes an
  experimental surface with API schema version `0.0.1`, not runtime version 2.1.4.
  Code targets observed response envelopes/shapes, not presumed V1 compatibility.
- `workmux add BRANCH --headless --json --no-hooks --no-file-ops --base COMMIT
  --name BRANCH --config PRIVATE_CONFIG` returned schema 1 with exact branch,
  absolute worktree/working-directory paths, pinned `base_branch`, and integer
  `post_create_hooks_run: 0`. No tmux, hooks, file copying, or interactive-default
  changes. A child-only Git configuration also disables Git checkout hooks.
- `opencode api post /api/session --data ...` preserves supplied `ses_...` IDs,
  exact location, ownership metadata, explicit agent/model, and session permission
  rules. A GET of that exact ID verifies identity before prompt admission.
- `POST /api/session/ID/prompt` preserves supplied `msg_...` ID/payload in its
  durable inbox acknowledgment. `resume:false` was probed separately: it leaves
  deferred inbox input, **not** a user-history anchor and not an active-run proof.
  The real resumed prompt later appeared under its exact ID in user history.
- Message history is `.data[]` plus `.cursor.next/previous`. Readers follow every
  next cursor, including a final empty page. The audit used seven one-message
  pages, not a guessed latest message or a timestamp-only comparison.
- Session GET contains agent/model/location/metadata/permissions, but no active
  execution field. `/api/session/active` supplies a running map; only the known
  session's bit is retained. Permission/form/inbox GETs are exact-session scoped.
- Permission reply uses `decision`, not `reply`; form IDs use `frm_`. The proof
  used the installed schema and actual responses, not guessed request fields.

All calls use the supported `opencode api` subprocess with the normal discovery,
authentication, environment, and shared service. There is no private server,
`--auto`, `--continue`, guessed latest session, or persisted authentication header.
Public/private schemas and raw owned-resource responses remain private evidence.

## Request and queue semantics

V1 `Request` serialization is unchanged. Its stored canonical bytes/digests are
read as-is, never migrated by reserialization. Separate immutable request **v2**
contains canonical repository, pinned commit, exact UTF-8 prompt, explicit
agent/model/ordered permissions, fixed provisioning policy, and declared memory.
Prompts are capped at 16 KiB for the supported CLI JSON-body invocation.

Schema1->2->3 upgrades in an additive IMMEDIATE transaction under the existing initialization
lock; dispatch must be stopped. It creates dedicated agent ownership and immutable
operation-intent records and the completion outbox, preserving receipts/triggers and rejecting future
schema versions. A real saved v1 binary created the migration fixture. The proof
compared canonical bytes/digest before/after and verified a matching cancelled
v1 retry still recovered the same receipt. No new direct dependency was added.

The same SQLite queue, monotonic sequence, strict blocked-head FIFO, default two
slots, real MemAvailable 6 GiB floor, and conservative inflight accounting apply.
`launching/running/unknown` agent work holds capacity. The local CLI PID and boot
identity never prove a shared-service agent stopped. `resolve` rejects agent jobs;
`reconcile JOB_ID` takes the dispatcher lock and performs API reads only.

Each external mutation has committed intent before invocation:

1. Workmux creation, followed by receipt/path/branch/base/repository verification.
2. Session creation with preallocated identity and ownership metadata, followed
   by exact GET verification and an execution-free session/history check before
   the prompt. Initial system/synthetic context is permitted and later audited.
3. One prompt with preallocated message ID, request/prompt digests, and owner token.

There is no POST retry after a lost acknowledgment, no branch/name-only adoption,
and no automatic provisioning continuation. Lost worktree acknowledgment stays
unknown. Lost session/prompt acknowledgments allow only exact-ID observation.
Shared-service execution continues independently of dispatcher/CLI lifetime.

## Completion policy: `session-scoped-v1`

Completion requires exact session/location/ownership, a matching durable user
anchor consumed exactly once, the complete ordered transcript through explicit
idle outcome (`succeeded`, `failed`, or `interrupted`), stable session
agent/model/location/permissions, no additional execution inputs, no active loop,
no pending permissions/forms/inbox, and accounted-for cooperative tool resources.
The `foreground-shell-v1` extension below distinguishes outer tool termination
from background exit. Additional user/command/shell execution inputs,
unrecognized history types, and agent/model/location changes remain unknown.
Such new-policy interference is latched as `session_interference`; it is not
cleared merely because a later GET looks idle or a foreign inbox input disappears.

`system` and `synthetic` history messages are auxiliary context, irrespective of
their text. Their IDs/types/count and full private transcript are recorded in
versioned terminal evidence. Pending auxiliary inbox input must still drain before
completion. No producer, causal ownership, or hidden-input authentication is
claimed. A malicious same-user API client is outside this local single-user
threat model. Caller-supplied ownership metadata catches accidental wrong-session
or mismatched-profile use, not forgery by another process with the same access.

The observer compares two complete transcript reads, checks exact session profile
before/after/final, observes inactivity twice, and verifies empty inbox/permission/
form queues. A latest-ID query is only a fence, not the completion criterion.
These are stable coherent observations, not an atomic lock on the runtime or a
cryptographic guarantee against concurrent hostile writers. CLI exit zero,
latest text, or idle alone remain insufficient.

Actual observed interval:

```text
owned user prompt -> assistant text -> system instruction update
-> synthetic Plan reminder (no ownership/provenance metadata)
-> another assistant text -> idle succeeded
```

There was exactly one owned user prompt and zero tool content. The synthetic
reminder was first observed in the inbox and later in history, with no supplied
prompt ID/owner metadata tying it to this job. It appears to be a Plan-mode
injection, but recognizing its text is not a provenance guarantee. No such
guarantee is required by the corrected session-scoped contract. Both auxiliary
events are recorded without claiming they are caller-owned execution inputs.

`agent.server_outcome` records the explicit session outcome separately from job
state and validation. Terminal evidence identifies the completion policy and
retains the superseded interference diagnosis. Old private observations and
historical failed results are preserved; re-evaluation does not change immutable
request receipts. Raw API evidence remains private, and durable user output
contains assistant text, not reasoning. No upstream/global configuration was
changed, and no alternate agent or repeated prompt was used.

## Tool/resource policy: `foreground-shell-v1`

The installed V2 OpenAPI defines assistant tool parts with `id,name,state,time`
and optional `executed`; state is `streaming`, `running`, `completed`, or `error`.
Input, output content, structured error and metadata are retained privately.
User stdout contains assistant text only, not reasoning or tool payloads.

The observer accepts terminal `completed`/`error` for the native built-in
`read`, `edit`, `write`, `patch`, `glob`, `grep`, and `shell`. The foreground
semantics were inspected in matching v2.1.4 source under
[`packages/core/src/tool/plugin`](https://github.com/anomalyco/opencode/tree/68bc26b7da1990ce10ffecfbc1e09eb13aa5d824/packages/core/src/tool/plugin).
Real acceptance exercises **read, patch and shell**, not every listed file tool.
Other tools, including delegation, plugin/codemode execution and terminal control,
remain unsupported and hold capacity. Nonterminal or unknown tool states also
hold capacity with a visible reason. Terminal tool errors are recorded/countable;
they are not an indefinite resource hold when the overall session has an explicit
terminal outcome and no pending effects. A server outcome is still not a validated
deliverable or a claim that every tool succeeded.

For shell tools, outer `state.status=completed` can coexist with
`state.metadata.{status:running,shellID:…}`. This is **not process exit**:

- Before prompt submission and each terminal observation, read
  `GET /api/shell?location[directory]=WORKTREE`; the installed API lists only
  running shell commands, so an empty list alone cannot resolve a known ID.
- Persist observed/referenced shell IDs; exact `GET /api/shell/ID` uses the same
  explicit location. Verify the response location, ID, metadata `sessionID`, cwd
  within the owned worktree, and lifecycle times. `exited`, `timeout`, or `killed`
  with a completed time is runtime termination, not validation. Up to 64 known
  shell IDs are inspected per job; larger sets fail closed with an actionable
  bounded-observation reason. Missing/deleted/unsupported known IDs stay unknown.
- Repeat the resource snapshot before the final transcript/activity/profile
  fence. Running shells retain capacity even if the session is already idle.
  Native foreground shell calls without a shell ID are accepted from their
  terminal tool state plus the location resource checks; background calls need
  a structural ID, not a shell ID parsed from output text.
- Location-scoped `GET /api/pty` is a conservative additional guard: a running
  or unrecognized PTY state holds capacity. No PTY/persistent-terminal workflow
  was executed or claimed supported.

Background completion can notify the same session through auxiliary synthetic
input and another assistant turn/idle. Intermediate idle messages do not imply a
second external prompt; full history must still contain exactly one owned user
input and no foreign execution/profile mutation. Final completion requires the
same stable transcript, explicit last terminal outcome, inactive session, empty
inbox/permission/form queues and no running owned runtime resources.

This scope is **cooperative runtime-managed commands**, not a universal sandbox.
Shell scripts that escape tracking with detached/daemonized processes are outside
the supported contract. No host process scan, broad kill, permission bypass,
automatic cancellation or guessed cleanup is implemented. Runtime restart or
capture loss may make a known ID unobservable and require operator investigation;
client PID exit cannot release it. Peer locations are never enumerated/persisted.

## Executed acceptance and safe recovery

Product was implemented first. Serial bounded builds, gofmt, vet, module verify,
and the existing real command-core acceptance were run. The new integration
acceptance used persistent local SQLite and a disposable Git repository; its
initial fixture commit was the only commit, outside ocq. All owned worktrees,
branches, sessions, receipts, and results were retained.

The original acceptance **failed** under the superseded stronger contract.
That historical failure is not relabeled as a pass. A continuation inspected only
the recorded job/session, without recreating worktrees/sessions or sending another
agent prompt. It verified the exact anchor, seven paginated history pages, the
unattributed synthetic input, immutable v1 bytes, one worktree/session/prompt
intent each, and retained unknown capacity. It exited **2 / BLOCKED**.

API-created permission and form requests on the owned session were visible as
blocked with capacity held. The proof explicitly rejected that permission and
cancelled that form, then verified no pending requests remained. This is **API
visibility**, not an agent-tool permission workflow test; no tool was invoked to
provoke a denial. Closing the requests left the job unknown under the old policy.
The exact real-agent session became inactive naturally; no interrupt was needed.
The separate deferred-ID contract session remains inactive with its retained
`resume:false` inbox input, clearly not a completed job.

Reproduction commands (only in an explicitly disposable fixture):

```sh
# Save the prior v1 binary BEFORE upgrading; do not fabricate legacy provenance.
# Create an empty disposable PROOF_ROOT/repo with one initial fixture commit.
GOMAXPROCS=2 go build -p 1 -o runtime/ocq .
GOMAXPROCS=2 go build -p 1 -o runtime/ocq-agent-acceptance ./proof/agent
GOMAXPROCS=2 runtime/ocq-agent-acceptance "$PWD/runtime/ocq" SAVED_V1_BINARY PROOF_ROOT
# Reconcile/check the SAME recorded resources; no external mutation replay:
GOMAXPROCS=2 runtime/ocq-agent-acceptance --reconcile-existing "$PWD/runtime/ocq" PROOF_ROOT
```

The full runner refuses a reused database. `--reconcile-existing` reads the saved
job identity and invokes normal lock-exclusive product reconciliation, then checks
the real API and SQLite independently. It verifies the exact prompt/payload,
complete paginated history, auxiliary event IDs, no tools, terminal result,
inactivity, empty queues, durable output, slot release, unchanged original v1
bytes/digest, all immutable receipts, ownership, and exactly one acknowledged
worktree/session/prompt mutation. It never enters visibility/provisioning POST
flows. The old `--inspect-existing` spelling now aliases this safe mode; its
historical mutating visibility continuation is retired.

This retained-record check **passed** under `session-scoped-v1`: seven API pages,
one exact consumed user prompt, two auxiliary events, no tools, explicit success,
and held slots 1->0. Serial bounded builds, vet, module verification, and the real
command-core acceptance also passed on the corrected binary. The prior phase's
schema migration and permission/form visibility evidence remains separate; no
new agent prompt or visibility mutation was run. Exact identities/results and
the original failures remain in private `RESULT.md`, `RESULT-PROVENANCE.md`, and
the current `RESULT-SESSION-COMPLETION.md`.

## Real tool acceptance (2026-10-27)

One disposable Build job read `result.txt`, applied its exact fixture-only patch,
ran foreground verification, then started one cooperative bounded background
shell. Four **actual tool-origin** permission requests were observed pending;
API/DB assertions verified `unknown/blocked` and a held slot before an explicit
fixture operator replied `once` after checking session, tool-call source, complete
input, action and exact resources. Product code never replied. No synthetic
API-created permission was substituted for this test.

Independent assertions checked the real file bytes and Git diff (only
`result.txt` changed), actual foreground shell exit 0/output, one user prompt,
terminal tool states, durable assistant-only stdout, and background ID exit 0.
The outer background tool returned completed with runtime metadata still running;
the session became inactive, but ocq stayed unknown and held the slot. Only after
the operator released its fixture file and that exact shell exited did a stable
terminal session finish `agent_succeeded`, with `validation=not_run` and slot 0.

The first bounded attempt was not relabeled a pass: its harness rejected the
agent's native patch choice instead of the requested edit. It rejected only that
owned permission and verified runtime/session shutdown. The fixture brief and
allowlist were narrowed to one exact patch for the second fresh job; file/diff,
pending-request and background-lifetime assertions were not weakened. The first
record is retained for recovery, including its missing explicit idle outcome.
Two jobs total, one agent execution at a time; neither prompt was replayed.

```sh
# Fresh private persistent PROOF_ROOT; this creates a disposable repo/initial commit.
GOMAXPROCS=2 runtime/ocq-agent-acceptance --tools "$PWD/runtime/ocq" PROOF_ROOT
# Existing exact recorded job: API/DB/file reinspection only, no model prompt/POST.
GOMAXPROCS=2 runtime/ocq-agent-acceptance --verify-tools "$PWD/runtime/ocq" PROOF_ROOT
```

Final bounded builds/vet/module verification, the real command-core acceptance,
and retained no-tool reconciliation passed. The latter independently preserved
v1 receipts/all immutable state and exactly one worktree/session/prompt mutation.
Exact IDs, source/schema hashes, failed/passing checks and cleanup are private in
`RESULT-TOOLS.md`. All fixture Git resources and prior proofs remain retained.

## Remaining gates

The [runtime provenance analysis](runtime-provenance.md) preserves evidence and a
stronger **optional**, superseded proposal. Its inability to authenticate internal
causality is not a blocker or a demonstrated runtime bug under the current scope.
Arbitrary tools/background resources, PTY/persistent terminals, runtime cancellation,
automatic retries/cleanup, heavy-phase tokens, proactive chat/network notifications, unattended agent supervision,
profiling, migration of production work, and deployment are excluded.
Local completion notification pull/ack is now implemented separately; see
[notifications](notifications.md). Proactive chat/network delivery remains excluded.
The [command-only systemd template/proof](supervision.md) does not validate
unattended agents or shared-server containment; keep its command-only guard.
Real failed/interrupted tool-session closure, shell timeout/killed paths, all
listed file tools beyond read/patch, and runtime restart/resource loss were not
separately exercised. The initial rejected-tool record has no explicit terminal
idle and correctly remains unknown despite being inactive with no runtime shells.
Forced worktree/session/prompt acknowledgment
loss was not exercised; the crash occurred after prompt acceptance was recorded.
A shared-server client cgroup does not contain tools. Normal available slots were
used; the expired reprieve was not used.
