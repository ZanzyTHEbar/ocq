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

There is no automatic continuation or mutation replay after a lost response.
Lost Workmux acknowledgement remains unknown; session/prompt recovery uses exact
supplied IDs only. Worktrees, branches, sessions and evidence are retained.

## Completion

The `session-scoped-v1` policy requires:

- Exactly one matching consumed user prompt in the complete ordered transcript,
  not merely a durable inbox acknowledgement.
- An explicit final idle outcome: succeeded, failed or interrupted.
- Stable exact session/profile/location/permissions, repeated inactivity and no
  additional execution inputs or pending permissions/forms/inbox.
- Two matching full transcript reads, profile fences and accounted-for tools and
  runtime resources. Every pagination cursor is followed; latest text/ID alone
  is not completion evidence.

System/synthetic events are recorded context, not authenticated producer or causal
provenance. Pending auxiliary inbox input must still drain. Additional user,
command or shell execution inputs and profile mutations latch interference and
retain unknown ownership; a later idle response does not clear it.

The `foreground-shell-v1` extension accepts terminal completed/error states for
native read, edit, write, patch, glob, grep and shell tools. Nonterminal,
unrecognized or unsupported tools hold capacity. Tool errors are recorded, not
converted to tool success. Delegation, plugin/codemode execution and persistent
terminals are outside this supported boundary.

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

Stop dispatch, inspect the exact recorded session/location and use:

```sh
ocq --state-dir STATE_DIR reconcile JOB_ID
```

Reconciliation is dispatcher-lock-exclusive and API-only. It never prompts,
resumes, answers permissions, cancels or cleans up resources. Client PID/boot
identity cannot prove a shared-service agent stopped; `resolve` rejects agents.
If known resources disappear after a runtime restart, investigate rather than
retrying execution or guessing replacement IDs.

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
static-review gate specifically requires source-validated OpenCode server
**2.2.6**; see [review constraints](project-control-plane.md#static-review).
