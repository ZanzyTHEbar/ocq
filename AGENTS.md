# Working on ocq

- Keep this local-first and portable. Do not embed host paths, credentials,
  private diagnostics, or machine-specific infrastructure in tracked files.
- This is an alpha Go + SQLite command core with an experimental agent adapter,
  not a production agent system. Session-scoped completion is validated for the
  retained no-tool session and a real Build read/patch/foreground-shell/bounded
  background-shell fixture, including actual operator-once tool permissions.
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
  detects accidental mismatches, not hostile forgery. docs/runtime-provenance.md
  preserves a superseded, stronger optional contract, not a current blocker.
- Dagu was evaluated first. The approved source-backed decision selected SQLite
  as the sole queue authority because external admission/ordering/reconciliation
  would make a thin Dagu adapter another scheduler. Preserve the historical proof.
  No Dagu runtime dependency, Python, standalone JS/TS runtime projects,
  frameworks, or speculative backends. A small trusted embedded JavaScript
  GPUI mod for current herdr-app is approved; it may use a Go bridge calling
  the core directly, not a separate JavaScript service or the legacy adapter.
- Keep one queue authority. OpenCode owns execution and permissions; Workmux
  owns worktrees. Never modify existing live helpers as part of experiments.
- Make small, scoped changes; inspect existing work first. Never blindly retry
  ambiguous side effects or treat a permission block as successful completion.
- Validate harmless shell lifecycle jobs before live agent execution. Report
  exact checks, results, and gaps; do not invent tests or success claims.
- Keep runtime state, generated artifacts, and binaries untracked; retain useful
  fixtures. MIT licensing is approved; preserve LICENSE. Do not push or create
  a remote without approval.
- Run the small proof with `bash proof/dagu/run.sh`; never replace its harmless
  fixture with agent work. Verify the pinned official checksum before executing
  Dagu. Use private owned state, record PID/start identities, and verify scheduler
  and children have exited on success or failure. Do not patch live helpers,
  services, global configuration, resource limits, or production orchestrators.
- Build/vet with GOMAXPROCS=2 and `-p 1`. Run the real command-core acceptance
  scenario described in docs/command-core.md; do not add unit/mock matrices.
- SQLite state must use private persistent local storage, not the repository's
  shared mount or tmpfs. Keep WAL/FULL connection settings, immutable receipts,
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
  `<=16 KiB` deterministic inline Goldfish review gate (`read`/`glob`/`grep` plus
  catch-all `deny *`). An experimental read-only current Herdr native mod uses
  a short-lived Go bridge calling the core; the terminal board also remains
  available. No legacy Herdr plugin is supported. See docs/notifications.md and
  docs/project-control-plane.md.
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
