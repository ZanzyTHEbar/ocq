# OpenCode agent jobs

The experimental adapter creates one dedicated Workmux worktree and OpenCode
session for one supplied prompt. OpenCode owns execution and permission decisions;
OCQ owns queue admission, durable mutation intent and completion observation.
This is cooperative runtime accounting, not sandboxing or arbitrary tool support.

## Submit

Execution needs an accessible OpenCode service, Workmux, a Git repository and an explicit
agent/model/permission profile. These tools must be discoverable in the
dispatcher's environment, not only an interactive shell.

```sh
ocq --state-dir STATE_DIR submit-agent --key KEY --repo REPO --base HEAD \
  --prompt-file prompt.txt --agent AGENT --model PROVIDER/MODEL \
  --permissions-file permissions.json
ocq --state-dir STATE_DIR dispatch
```

`--base` defaults to HEAD and is pinned at submission. Prompts must be nonempty
UTF-8, at most 16 KiB. `--memory-bytes` defaults to 64 MiB and is admission demand,
not enforcement. Model syntax is `provider/model#variant`, with variant optional.
Permissions are a nonempty JSON array with `action`, `resource` and `effect`
(`allow`, `deny` or `ask`). Use the intended explicit rules; OCQ never replies to
a pending permission. `dispatch --commands-only` does not admit or poll agents.

Version-2 requests store the canonical repository, pinned base, prompt, profile,
permissions, provisioning policy and memory. V1 command receipts are unchanged.
CLI standalone submission canonicalizes live paths before key recovery; project
submission offers [receipt-first recovery](project-control-plane.md#registration-and-submission).

### Local Queue intake

Herdr also offers [standalone agent submission](herdr.md#submit-a-standalone-agent)
without a project association. The guided form requires an explicit key,
repository/base selector, prompt, agent, provider/model/optional variant, decimal
memory and ordered permission JSON. Go generates canonical v2 bytes; memory
remains exact int64 text through the UI, not a rounded JavaScript number.
Exact JSON import accepts `{key,request}` with an already canonical v2 request
and preserves its bytes and original import. Core validators are authoritative.

Private draft review precedes preparation and a separate full retained review of
prompt/profile/ordered rules, exact bytes and digests. Pointer confirmation
durably claims one admission attempt. Fresh admission checks repository/base only
after receipt lookup; exact key/byte recovery works offline even when the repo is
unavailable. A lost response requires journal/receipt inspection, never automatic
resubmission. Receipt matching proves admission, not successful execution.

Standalone intake has no live runtime/API availability gate. It neither provisions
sessions/worktrees, sends prompts, answers permissions nor starts dispatch; an
already-running dispatcher may execute queued work. Acceptance does not establish
agent/model availability or execution readiness. Remote admission is disabled.
Project intake's separate binding and reviewer/runtime/pre-prompt gates still apply.

## Provisioning

OCQ records intent before Workmux creation, session creation and prompt submission.
Workmux runs headlessly with hooks and file operations disabled; OCQ checks the
exact returned path, branch, base, repository identity and zero hooks. Git
checkout hooks are disabled for that child process.

Sessions and prompt IDs are preallocated. Exact GET checks verify unparented
session identity, location, profile, ordered permissions and ownership metadata
before the one prompt. A deferred inbox acknowledgement is not proof that the
prompt was consumed. API calls use `opencode api` and its intended discovery and
authentication context.

There is no automatic worker continuation or mutation replay after a lost response.
While an already-running job's exact initial input or supported native project
conversation text is still in the inbox,
the observer retains nonterminal running capacity if its durable prompt
acknowledgement, delivery mode and inspected history match. It never resends or
resumes that input, revives an unknown job, or treats auxiliary context as
ownership proof. All inbox input must still drain before terminal proof.

Lost Workmux acknowledgement remains unknown; session/prompt recovery uses exact
supplied IDs only. Worktrees, branches, sessions and evidence are retained.
The separate [opt-in project handoff](goals-and-sessions.md#opt-in-to-cooperative-automatic-wake-and-archive)
can start a pointer-only turn on the selected Main after result publication; it
does not add a second worker input or change the worker completion proof.

## Completion

The `session-scoped-v1` policy requires:

- The exact original consumed user prompt in the complete ordered transcript,
  not merely a durable inbox acknowledgement. Only project workers may have
  subsequent native plain-text inputs after that anchor.
- An explicit final idle outcome: succeeded, failed or interrupted.
- Stable exact session/profile/location/permissions, repeated inactivity and no
  unsupported additional execution inputs or pending permissions/forms/inbox.
- Two matching full transcript reads, profile fences and accounted-for tools and
  runtime resources. Every pagination cursor is followed; latest text/ID alone
  is not completion evidence.

System/synthetic events and later project-conversation text carry no producer
attestation. Pending input must drain and success must include a response after
the last observed text. Unsupported user attachments, command/shell inputs and
profile mutations still latch interference; an idle response does not clear it.
Native conversation after the first terminal commit is separate from the
historical job and its review. See [goals and sessions](goals-and-sessions.md).

`opencode-cooperative-v1` observes the supported OpenCode V2 API shapes; it is
not a second tool-permission engine. The exact observed version and PID must
agree across the observation. Unknown shapes or missing evidence hold capacity.
Normal configured local foreground tools and
awaited MCP RPCs are trusted to finish when the harness reports their terminal
state. There is no ordinary-tool name whitelist, producer-binary attestation,
capability declaration or extra permission profile. Static review independently
remains read/glob/grep plus catch-all deny.

Native Code Mode catalog search and foreground RPC composition are supported.
Completed children remain completed; error/interrupted children are recorded as
errors, never relabelled successful because the outer tool completed. Running or
missing child lifecycle rows prevent completion. The native runtime joins its
host-call cleanup before returning. Shared MCP connection lifetime is not an
individual job's resource lifetime. Source: `anomalyco/opencode` commit
`c0ad18b402eb2827fc1ee71c171b1177b3fc02d1`,
`packages/{core/src/codemode/tool,codemode/src/language/runtime/{execute,driver}}.ts`.

Concrete persistent resources remain separate: directly reported shell handles
are retained and inspected to actual exit; pending job/task/PTY/delegation
handles are not closed by an outer return. OpenCode 2.3.2 omits nested resource
result handles from Code Mode rows, so native nested resource operations cannot
claim the same support as ordinary foreground RPCs. Non-shell runtime jobs and
live PTYs without supported exact terminal lookup retain an actionable resource
diagnosis; their disappearance from a list is not accepted as exit. The global
experimental job list is filtered by exact session, never adopted by Location.

The policy is retained in `agent_operations` with new admission (or before a
queued legacy receipt's first execution intent), without changing v1/v2 receipt
bytes or schema. Runtime version is checked before provisioning/session/prompt
and before/final completion observation; runtime identity drift holds ownership.
Old jobs are never upgraded by polling or binary replacement. An explicit
`reconcile JOB --lifecycle-policy opencode-cooperative-v1` on an unknown job
retains one immutable prior-job/ownership/policy intent under the dispatcher
lock before API observation. Policy adoption is single-attempt; subsequent
`reconcile JOB` calls explicitly request further API-only observation under the
retained policy, while automatic polling still skips this historical job.
Historical terminal evidence and frozen
completion events remain intact. New proof is `terminal-evidence-cooperative-v1`.
This command is existing-store only and never initializes/upgrades the queue.

Each new unknown/blocked transition for an attributed job atomically inserts
`lane.blocked` to its integrator inbox with sender `observer:ocq`. This is an
observer diagnosis, not forged worker mail or terminal proof. Content-derived
identity deduplicates unchanged observations and recurring identical diagnoses.
No Main prompt, acknowledgement, capture/review or capacity release follows.

A completed shell tool may still own a running background shell. OCQ checks
location-scoped shell lists before prompting and during terminal observation,
persists structural shell IDs, and reads each known ID at the exact location.
ID, session, cwd and lifecycle times must match. Missing IDs remain unknown even
when the running list is empty. At most 64 known shell IDs are inspected.
Background exit requires a terminal runtime state with a completion time, not
tool return or parsed output text. Running/unrecognized PTYs also hold capacity.
Final resources are checked again before transcript/activity/profile fences.

`agent_succeeded` records execution outcome with null exit code and
`validation=not_run`. Durable user output contains assistant text, not reasoning
or tool payloads. Idle or CLI exit alone never releases ownership.

## Recovery

Project reviews inspect the effective agent in the exact owned checkout before
creating a session, and revalidate it before prompting. On source-pinned 2.3.0/2.3.2,
session creation does not initialize location services and the agent route can
observe an empty catalog during asynchronous plugin activation. The gate resolves
`GET /api/location` with the explicit directory, then uses the read-only
`GET /api/model` server-owned activation barrier before inspecting the agent.
Location resolution alone is not readiness. No sleeps, GET retry loop, reload,
agent substitution or policy relaxation is involved; unavailable inspection fails
closed before a session POST. A retained worktree still has ownership and is not
automatically released.

Stop dispatch, inspect the exact recorded session/location and use:

```sh
ocq --state-dir STATE_DIR reconcile JOB_ID
```

Reconciliation is dispatcher-lock-exclusive and API-only. It never prompts,
resumes, answers permissions, cancels or cleans up resources. Client PID/boot
identity cannot prove a shared-service agent stopped; `resolve` rejects agents.
If known resources disappear after a runtime restart, investigate rather than
retrying execution or guessing replacement IDs.

An unprompted provisioning failure has no completion path in this contract.
Reconciliation explicitly retains unknown when durable prompt intent is absent;
empty transcript/inbox/requests, idle activity and no owned resources do not
substitute for the consumed prompt and explicit terminal outcome. Releasing such
a reservation would require a separately specified, explicitly authorized
provisioning-abort capability with affirmative no-execution/fencing evidence,
not a fabricated agent outcome or an automatic absence-based reconciliation.

The Herdr Queue view also offers [explicit local reconciliation](herdr.md#reconcile-agent-jobs).
Separate human review/confirmation binds the exact queue, receipt, session,
worktree and stored snapshot before a durable arm and one API-observation attempt.
It uses the same dispatcher lock and completion policy, not another execution
path. Stop dispatch yourself; the panel never stops it automatically.

Panel authorization is retained once per receipt. After an unclear response,
check the stored operation receipt only: recovery makes no live API calls and
does not rerun reconciliation. An operation receipt may record observer return,
error or lock refusal; neither it nor a historical terminal event establishes
agent success or validation. Only existing completion proof can release capacity.
Remote reconciliation is disabled.

## Operational limits

Completion covers cooperative runtime-tracked resources, not detached daemons or
hostile same-user API mutation. Ownership metadata detects accidental mismatches,
not forgery. A shared-server tool is not contained by the client's cgroup.
General tool support, unattended agent supervision and complete project
implementation-to-review execution are not end-to-end verified. The project
static-review gate accepts only source-validated OpenCode server
**2.2.6, 2.3.0 or 2.3.2**; see [review constraints](project-control-plane.md#static-review).
Source compatibility is not live implementation-to-review execution validation.

## Main-conversation handoff

For an existing project binding, use
[`project work`](project-control-plane.md#parent-driven-operator-work): prepare
from a plain task file and explicit execution profile, inspect the complete
frozen preview, then confirm its pointer once. Status/output resolve the exact
original receipt and owned worker without choosing a latest session. Keep the
main conversation outside bounded worker jobs; do not change a worker's prompt
or profile to manufacture completion.

After separately proven terminal execution, prepare/confirm machine capture,
prepare/confirm the independent exact-snapshot review, and prepare/confirm the
strict final review report. Handoff returns integration evidence, never merge
authority or invented test validation. Unknown tools/resources, pending requests
and missing shell IDs retain capacity exactly as before.

The admission-only acceptance entrypoint is `proof/operator-work`. It requires a
fresh explicit queue path, an existing exact coordinating session/checkout and
an explicit profile. It exercises real CLI/SQLite admission, receipt recovery and
journal observation without dispatching or creating any agent session/worktree.
Actual implementation/review execution requires a separately allocated runtime
budget and remains a distinct validation phase.

Use [persistent Main](project-control-plane.md#persistent-project-main--integrator)
for the product's multi-turn integrator conversation, not another bounded job.
Bootstrap/adopt once from an explicitly selected Main role profile and private
prompt file, preview/start, then use its exact SQL-bound identity. Native human
input and human profile changes are legitimate there; they are not legitimate
shortcuts around worker one-prompt/profile/resource completion fences.
Initial setup also requires `--role-file` selecting the exact implementation and
review WorkProfiles. Its previewed persistent operating instructions explicitly
carry one goal through implementation → capture → independent review → strict
review result → handoff. `project main role PROJECT` reads this retained contract;
`project work wait PROJECT REQUEST --timeout-seconds 20` reuses the read-only
observation lease. Main renews pending waits without a fresh human prompt, but
permissions/unknown ownership remain human attention—not permission to retry.

The [native client launcher](project-control-plane.md#exact-native-client-navigation)
opens verified existing targets only. Main bootstrap is the separate explicitly
confirmed creation path. Main mutation bodies use the verified explicit-server
origin/authentication in a bounded private HTTP request because OpenCode 2.3.0/2.3.2's
`api --data` accepts argv text only. No prompt, permissions, token or credentials
are put in argv; no service discovery/authentication settings are changed.
