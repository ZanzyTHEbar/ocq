# Working on ocq

- Keep this local-first and portable. Do not embed host paths, credentials,
  private diagnostics, or machine-specific infrastructure in tracked files.
- This is an alpha Go + SQLite queue core with an experimental agent adapter.
  Follow docs/architecture.md, docs/reliability-contract.md, and
  docs/command-core.md and docs/agent-integration.md. Provisioning/API observation
  and cooperative tool completion exist. One fresh dedicated session/worktree, one exact
  consumed user prompt, stable full transcript, explicit terminal outcome, no
  active loop, pending input/permissions/forms or running owned resources are
  required. Known tool states must be completed/error; shell tool completion is
  not background exit. Check location-scoped shell lists and exact known shell
  IDs before/final observation; missing/unsupported resources hold capacity.
  Product never answers permissions. System/synthetic
  events are recorded auxiliary context, not proof of producer or causality.
  Extra execution inputs/profile mutations remain unknown. Same-user malicious
  API injection is outside this local single-user model; ownership metadata
  detects accidental mismatches, not hostile forgery.
- SQLite is the sole queue authority. Preserve private historical evidence outside
  the public tree. No Python, standalone JS/TS runtime projects, frameworks, or
  speculative backends. A small trusted embedded JavaScript
  GPUI mod for herdr-app may use a Go bridge calling the core directly, not a
  separate JavaScript service or an alternate queue authority.
- Keep one queue authority. OpenCode owns execution and permissions; Workmux
  owns worktrees. Never modify existing live helpers as part of experiments.
- Make small, scoped changes; inspect existing work first. Never blindly retry
  ambiguous side effects or treat a permission block as successful completion.
- Validate harmless shell lifecycle jobs before live agent execution. Report
  exact checks, results, and gaps; do not invent tests or success claims.
- Keep runtime state, generated artifacts, and binaries untracked; retain useful
  fixtures. Preserve the MIT LICENSE. Do not push or create
  a remote without approval.
- Do not patch live helpers, services, global configuration, resource limits, or
  production orchestrators.
- Build/vet with GOMAXPROCS=2 and `-p 1`. Run the real command-core acceptance
  scenario described in docs/command-core.md; do not add unit/mock matrices.
- SQLite state must satisfy docs/storage.md: private storage with coherent
  single-host locking/WAL shared memory, file/directory sync and explicit retention.
  Do not infer capability or durability from filesystem names; suitable VM/container
  volumes are allowed, multi-host shared WAL is not, and ephemeral state cannot
  promise durable recovery beyond its lifetime. Keep WAL/FULL connection settings, immutable receipts,
  FIFO head blocking, launch-intent persistence, and unknown ownership intact.
  Never release unknown capacity or retry execution just because a client exited.
- Agent requests are a separate immutable v2; never add fields to/re-serialize v1
  receipts. Schema1->2->3->4->5 is additive/transactional under the initialization
  lock with dispatch stopped. Schema3's completion outbox is separate from
  receipts; Schema4 adds the project control plane (`projects`,
  `project_manifests`, `project_bindings`, `project_registrations`,
  `project_submissions`, `project_jobs`, `project_result_associations`) without
  attributing legacy jobs. Schema5 adds first-class owned lanes
  (`project_lanes`) and the immutable integrator inbox
  (`project_inbox_messages`): one lane per owner job/session, set-once
  `fsfork_path`, seal digest must reference an owner association, no lane
  deletion, immutable message content, read/archive once (archive after read),
  no message deletion, and automatic `work.submitted`/`review.submitted` mail in
  the same transaction as the sealing association. Projection reads
  (`project list|show|results|job|manifest|lanes|inbox|board|verify-worktree`)
  use non-initializing read-only SQLite access.
  Schema6 adds committed `observation_revision` triggers without rewriting receipts
  or journals. Preserve Goose migration 1's historical schema1->5 steps; use separate
  transactional migration 2 for 5->6 under the initialization/dispatcher locks.
  Existing-store APIs explicitly support {5,6}; automatic observation requires 6
  and never initializes/migrates. Coordinate CLI/bridge/dispatcher/mod replacement
  before explicit upgrade; keep selected database views coherent in one read
  transaction and filesystem log observations separate/non-atomic.
  Terminal transitions and events commit atomically; unknown is not completion.
  Preserve frozen first-terminal snapshots and idempotent acknowledgements;
  pull/ack must never release capacity or retry execution. `project results` is
  non-consuming across globally acknowledged events. `project register` and
  `project submit` enforce receipt-first idempotent recovery before live
  repo/session checks, CAS on binding revisions, atomic owned-lane creation with
  `lane.assigned` mail, atomic `record-result` with
  `verification=structure_hash_linkage_only` (never promoting imported claims to
  verified validation) and automatic lane sealing plus
  `work.submitted`/`review.submitted` mail, idempotent explicit
  `mail-send`/`mail-ack`, cooperative before/after `capture-result`
  (`source=machine_capture`, `verification=machine_verified_snapshot`, keeping
  test `validation=not_run`), read-only `verify-worktree` live worktree-to-snapshot
  drift detection (`matches_snapshot` vs `modified_since_snapshot`), and the
  `<=16 KiB` deterministic inline static-review gate (`read`/`glob`/`grep` plus
  catch-all `deny *`). The Herdr native mod uses a short-lived
  Go bridge for stored views, explicit inbox acknowledgements, and local-only
  `lane.blocked` compose via prepare, retained preview, and confirmation; supported
  previously prepared `lane.assigned` mail can also be previewed and confirmed.
  Local project intake supports exact-JSON registration and implementation/review
  submissions with supported full referenced manifests, structured retained-byte
  review and pointer-only final confirmation. Compose and
  intake require durable arm plus one attempt and exact receipt read-back; never
  automatically resend uncertain operations. Remote compose/intake are disabled.
  Stored request reasons have no observation timestamp or permission/form type
  distinction; stored request views do no live lookup or approval. Explicit local
  agent reconciliation is a separate confirmed action. Do not infer agent workflow
  completion or remote deployment readiness from panel intake-to-queue support.
  Queue/log/global-event panel inspection uses existing-store read-only access;
  it must not acknowledge global events or invoke execution controls implicitly.
  See docs/herdr.md. Each journal checks queue device/inode but cannot
  detect in-place restores; do not replace or retarget database paths concurrently.
  Human permissions are never auto-approved. The terminal board is also
  available. See
  docs/notifications.md and docs/project-control-plane.md.
- Journal intent before Workmux/session/prompt mutations. Persist exact supplied
  session/prompt IDs and ownership. Never retry ambiguous POSTs, adopt by branch
  name, guess a latest session, or change shared-service discovery/auth context.
- Reconcile is API-only and dispatcher-lock-exclusive; it must not resume, prompt,
  answer permissions, cancel execution, or use a process/boot check to resolve an
  agent. Preserve owned worktrees/branches/sessions/results; no automatic cleanup.
- Do not whitelist synthetic input by text, disable global injections, or switch
  agents as a workaround. Keep tool support to the documented cooperative native
  tools/runtime-tracked shells. PTY/delegation/plugins/escaped daemons and resource
  loss/restart recovery are not broadly validated. Do not deploy.
- Project reviews verify the exact agent and supported OpenCode policy version
  at intake and again in the owned checkout before prompting. Preserve the
  ordered session rules, cooperative boundaries, and conservative uncertainty;
  do not claim filesystem sandboxing or silently substitute agents.
- Optional systemd user supervision is command-only: keep `--commands-only`,
  default 2 slots/6 GiB floor, singleton lock and unknown ownership. Template is
  not installed/enabled. Its mixed kill mode drains on TERM but can force-kill
  only its unit cgroup on main exit/timeout; never infer job success/retry or
  shared-server containment from that. See docs/supervision.md. Experiments use
  only uniquely named, receipt-verified transient units and fresh private queues;
  no daemon-reload, persistent units, global config or shared-service changes.
