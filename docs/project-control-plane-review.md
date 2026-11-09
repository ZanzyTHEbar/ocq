# Independent Goldfish design review

**Current verdict: ready for human scope approval.** No remaining material design
blockers. This is not implementation approval, implemented behavior, runtime proof
or deployment readiness. See the current re-review appended below.

## Prior-draft review (historical)

The following findings and design line references describe the original draft,
not the revised design. They are preserved as review history.

**Prior verdict: needs-revision.** The direction fits Phase 1, but resolve the three
must-fix contracts below before approving implementation. This is a fresh-context
static review of `project-control-plane.md` and current source, not a runtime proof.

### Prior blockers / must-fix items

#### B1 — High: historical intake retry is behind live-binding validation

**Design:** `project-control-plane.md:70-92,154-168`.
Step 1 verifies the current repository/session and resolves the current binding
before step 2 checks the job key. That can reject a lost-ack retry after the
Elephant disappears or is rebound, despite step 4 promising the original receipt
with its old revision. Registration explicitly avoids this trap; intake does not.

**Source:** `agent_request.go:69-95,133-138` canonicalizes live paths/base before
`store.go:231-266` checks stored canonical bytes. Legacy CLI retries already depend
on resolvable inputs (`command-core.md:54-79`); they do not provide an offline retry
primitive to reuse unchanged.

**Smallest fix:** specify a receipt-first project retry path using caller-supplied
accepted revision and exact immutable request/manifest identity. Match all project
metadata before returning; never regenerate it from today's binding, `HEAD`, or
session. Only new acceptance requires fresh availability verification and CAS.
State which original bytes/pinned inputs the caller must retain. Preserve legacy
submit semantics and legacy-job conflict behavior; do not adopt or re-execute jobs.

#### B2 — Medium: late result publication lacks a concrete retrieval contract

**Design:** `project-control-plane.md:57-59,102-107,142-143,176-205,245-247`.
The completion cursor deliberately excludes later associations. The remedy says
to re-read a known job/project, but the specified `project show` projection names
queued/active/unknown jobs, not terminal-job result history or manifest bytes.
There is no defined exact-job/manifest retrieval operation. A consumer can persist
a completion with `not_recorded`, advance its cursor, and have no specified direct
way to fetch the subsequently published snapshot needed for review or handoff.

**Source:** `notifications.go:29-44` freezes one event and retains it even after
ack; `main.go:122-135` and `store.go:27-47,306-320` expose current job/ownership, not
proposed associations or manifest content. No existing command fills this gap.

**Smallest fix:** define an on-demand exact-job project lookup returning all result
association IDs/digests, supersedes links and manifest availability, including for
terminal jobs, plus retrieval of exact bounded manifest bytes. Specify that a
cursor consumer retains jobs with `not_recorded` for explicit re-read (or replays
from cursor 0). Publish manifest and association atomically; a matching repeated
`record-result` must recover the same publication after a lost response. No new
notification sequence, subscriber state or change-feed framework is needed.

#### B3 — High: the reviewer journey still has an unresolved feasibility gate

**Design:** `project-control-plane.md:107-120,209-229,266-278`.
The draft correctly discloses context transport and limits as pre-coding gates;
they are not implementation-ready details. A digest/reference alone cannot give
the reviewer the promised full changed bytes. The chosen transport also determines
whether a normal result is reviewable without materializing candidate files or
introducing an unsupported tool.

**Source:** `agent_request.go:18-29,58,89-95` permits one text prompt capped at
16 KiB. `agent_lifecycle.go:172-201` sends text only and rejects file/agent/skill
attachments; `agent_observe.go:125-139` requires that exact single input.
`agent_resources.go:168-203` recognizes native read/glob/grep but not a manifest
plugin. `agent_lifecycle.go:23-30,115-141` verifies stored session rules, not a
general effective-agent permission policy.

**Smallest fix:** choose an initial inline-only review envelope containing criteria,
rules, pinned base, digest, diff and full changed text; reject intake if the entire
canonical prompt exceeds 16 KiB. Name the permitted native read-only tools and
deny everything else in the session policy; verify the intended effective profile
and newly created exact session before sending the prompt, blocking rather than
mutating a global profile. Keep larger-artifact transport explicitly deferred.
This preserves a useful small-change implementation → independent static review
journey without changing v2 or adding a context service.

### Prior sound decisions and useful suggestions

- **Keep non-consuming project results.** Joining all retained events is compatible
  with `notifications.go:13-44,69-105`; global ack records handling, not deletion.
  Frozen history versus current stored unknown and coherent read snapshots are
  correctly separated. No per-project ack/lease mechanism is needed.
- **Keep identity/CAS and atomic intake.** Product-project IDs are correctly separate
  from OpenCode IDs (`agent_store.go:15-19`; `agent_lifecycle.go:134-165`). Immutable
  binding revisions, exact session/repository checks and transactional metadata
  avoid a second authority. Extend the existing transaction boundary, not ordinary
  submit followed by attachment (`store.go:204-266`). External observation races
  are honestly bounded to the cooperative local model.
- **Clarify delegated authority, not authentication.** At
  `project-control-plane.md:87-98,109-110,145-147,170-172,224-235`, add a short actor
  rule: who may submit implementation/review within an operator-approved scope,
  what requires a new human decision, and that workers/Elephant never answer tool
  permissions or infer integration authorization. Define whether the allowed-action
  brief forbids worker commits/pushes/service/config changes. Do not require a new
  human click for every harmless read or invent same-user security ACLs.
- **Keep static review distinct from candidate execution.** A fresh pinned-base
  session plus immutable text is sufficient for this scope; no stronger exclusive
  provenance claim is needed. Label worktree/target matching as last observed or
  unknown until explicitly checked, not automatically current: project reads do
  not inspect the filesystem (`project-control-plane.md:195-203,231-235`).
- **Smaller first slice:** registry/CAS, atomic project intake, explicit imported
  bounded text results with assertion labels, exact-job lookup and inline review.
  Defer generalized verified capture and integration-record machinery; retain the
  human-only integration prohibition. The draft already distinguishes imported
  claims from verified evidence (`project-control-plane.md:133-147`).

### Prior review checks and boundary

Read local `AGENTS.md`, the design, referenced architecture/reliability/command/
agent/notification docs, and request, CLI, store, lifecycle, observer, resource and
API source. Checked Git status and absence of this review path before creation.
Existing dirty/untracked work was preserved; only this new document was written.
Whitespace checks: `git diff --no-index --check /dev/null
docs/project-control-plane-review.md` emitted no diagnostics (exit 1 for the new
file difference); an `awk` trailing-whitespace check passed (exit 0).

No queue was opened; no OCQ execution, execution session, Workmux operation,
service/API mutation, configuration change, migration, test, build or commit was
performed. This review created no owned execution resources; existing runtime
state was not inspected and no runtime-cleanliness claim is made.

## Current re-review

**Verdict: ready for human scope approval; no remaining material design blockers.**
Independently read the revised design and re-read the relevant local source;
the conclusions below do not rely on the author's report.

- **B1 resolved — receipt-first recovery.**
  `project-control-plane.md:177-209` requires caller-retained exact envelope bytes,
  historical revision and pinned inputs; existing-key lookup precedes live checks.
  Transactional key recheck and recheck after external-validation failure cover a
  concurrent winner. Mismatch/legacy conflict and new-intake CAS remain explicit.
  This is correctly a new project-only path: `agent_request.go:69-95,133-138`
  still performs live canonicalization before `store.go:231-266` accepts a request.
- **B2 resolved — late publication and retrieval.**
  `project-control-plane.md:242-261` defines terminal-inclusive exact-job lookup,
  exact manifest retrieval, atomic publication and idempotent association recovery,
  with pending job IDs retained independently of the completion cursor. All
  associations are exposed, so supersedes links need not imply an unspecified
  single mutable result head. Non-consuming results (`:217-240`) remain compatible
  with the frozen, retained, acknowledge-once events in `notifications.go:29-44,89-105`.
- **B3 resolved for design scope — bounded inline static review.**
  `project-control-plane.md:279-307` fixes framing, full candidate text, ordering,
  whole-prompt byte limit and oversize rejection without attachments or plugins.
  This fits `agent_request.go:89-95`, `agent_lifecycle.go:172-201` and
  `agent_observe.go:125-139`; native read/glob/grep are recognized by
  `agent_resources.go:168-203`. Candidate execution/materialization stays forbidden.
  Effective policy verification is explicitly still required, not asserted to
  exist: `agent_lifecycle.go:23-30,115-141` checks session rule equality/identity,
  not effective permission precedence. The revised fail-closed, pre-prompt gate
  (`project-control-plane.md:295-307,356-360`) is sufficient for scope approval, not proof of feasibility
  for any particular installed profile.

### Human decisions and implementation cautions (not new blockers)

- Approve or reject the deliberately small imported-assertion slice: at most
  8 text paths, 16,384-byte manifests and a separately bounded 16,384-byte complete
  review prompt (`project-control-plane.md:111-157,279-293`). A fitting result is
  not necessarily reviewable; larger transport and verified capture stay deferred.
- Delegated bounded submissions, human-only permission answers and separate
  integration decisions are now explicit (`:101-107,164-170`). Freshness is
  last-observed/unknown, not a live verification (`:263-269,316-321`). Keep these
  distinctions in eventual CLI output; imported assertions never become verified
  validation just because a reviewer approves them.
- No new material ambiguity found. Before implementation, retain the stated gates
  for effective policy verification and binding/authorization fields (`:356-365`).
  Strict manifest schemas and prompt generation must enforce the documented
  linkage, bounds and lossless framing; existing permissive stored-v2 decoding
  (`agent_request.go:145-164`) alone is not new-envelope validation.

### Re-review checks and boundary

Only `docs/project-control-plane-review.md` was modified. Read the revised design
and the request/store/lifecycle/observer/resource/notification source cited above;
checked Git status and this document's trailing whitespace. No queue, execution,
service/API mutation, configuration, migration, build, test, delegation or commit
was performed. No owned execution resources were created; existing runtime state
was not inspected. Scope approval does not mean implemented, proven or runtime ready.

## Historical legacy Herdr adapter extension review

This review concerns a superseded legacy interface. The legacy adapter is not
approved or part of the current product; current Herdr uses the terminal board.

**Verdict: ready for human scope approval; no material design blockers.**
Reviewed the complete current design, especially
`project-control-plane.md:323-531`, against Phase 1 and the local OCQ source.
This approves neither implementation nor installation/runtime use.

- **B1/B2/B3 remain resolved at design level.** Receipt-first exact-envelope
  retries (`:177-209`), atomic idempotent publication and exact-job/manifest reads
  (`:242-261`), and the sole inline <=16,384-byte static-review prompt (`:279-307`)
  remain intact and are expressly preserved by `:430-436`. The board is not a
  reviewer tool, attachment or alternative context channel. Results remain
  non-consuming, including globally acknowledged events (`:217-240,399-421`);
  neither sync nor links may acknowledge them. This fits retained immutable events
  in `notifications.go:29-44` and text-only matching in `agent_lifecycle.go:172-201`.
- **One authority and replaceable UI.** `:325-329,371-405,438-458` leave identity,
  state, capacity and completion in OCQ. Mapping is an explicitly selected,
  revision-checked disposable cache, not another registry. Missing Herdr resources,
  expired badges and Herdr done/idle/completion sequences cannot resolve unknown
  ownership or cause retries. Sync is observational; explicit binding retains
  operator authorization and OCQ registration/rebind CAS (`:415-428`).
- **Three extension layers are kept distinct.** `:331-354` separates executable
  plugin hooks/argv, pane-local integration metadata, and workspace token reports
  plus declarative Agent Views. `:388-397` correctly gives headless workers
  workspace-level visibility, not invented agent panes: source-scoped monotonic
  durable sequence, serialized reads/publication and 60-second TTL. Sequence/cache
  loss stops publication; expiry is unknown/stale, not zero work or completion.
- **Restore and execution boundaries are explicit.** `:430-458` prohibits mutating
  worker attach, Herdr provisioning/resume and automatic prompts; it requires
  operator-reviewed `resume_agents_on_restore = false`. `:338-340,364-369,423-428`
  specify argv execution, validated context and exact read-only link routes, not
  shell interpolation or execution of terminal-supplied commands/paths.

### Implementation cautions, not scope blockers

- **Projection reads need a genuinely non-initializing path.** The requirement in
  `project-control-plane.md:399-400,503-506` is not provided by today's CLI:
  `main.go:46-51` opens the store before dispatching commands, and
  `store.go:168-195` may initialize/migrate it. Do not implement startup sync by
  blindly reusing that path; absent/incompatible state must return an adapter error.
- **Retain the separate Herdr compatibility/restore gate (`:503-506`).** Check
  actual supported plugin/RPC schemas and effective restore configuration before
  enabling worker views. Require the restore setting before restore, not merely
  after startup hooks run. This review assesses the supplied pinned-v0.9.5 facts
  and proposed contract; it independently proves no Herdr schema or runtime behavior.
- **Keep links and stale tokens inert.** Implement strict ID grammar and argv
  option separation in the owned inspector; never forward links to a shell or
  ambient URL handler. Render imported output as untrusted text. Ensure TTL-removed
  tokens are shown as unavailable/stale rather than a misleading empty-success
  badge; publication time never upgrades OCQ evidence freshness (`:388-405`).

Only this review document was appended; existing work was preserved. Checked Git
status and ran its trailing-whitespace check (exit 0, no diagnostics). No queue,
runtime, build, service, installation, delegation or commit operations were run;
no owned execution resources were created or existing runtime state inspected.
