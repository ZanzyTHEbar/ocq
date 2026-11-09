# Required reliability contract

These are acceptance requirements, not implemented guarantees. The proof-of-fit
must establish them before live coding-agent jobs are entrusted to ocq.

## Submission and admission

- Persist acceptance durably before returning a job ID as acknowledgment.
- Accept a caller-provided idempotency key. A retry after a lost acknowledgment
  must recover the same accepted job rather than enqueue duplicate work. Reject
  reuse of a key for a different request. The durable job/key association must
  survive restart; document its retention boundary.
- Establish a durable submission order. Initial admission is strict FIFO:
  a resource-blocked head job blocks later jobs, even if they would fit. Do not
  silently skip the head for throughput. Concurrent completions are unordered.
- Recover accepted jobs after service restart without silently losing them or
  launching a second execution. Reconcile uncertain running jobs before acting.
  [Command-only systemd supervision](supervision.md) is now proven on harmless
  transient fixtures: dispatcher restart retains unknown ownership/capacity and
  never retries execution. Graceful drain and forced unit cleanup are distinct
  from successful job completion. This is not unattended agent deployment.

## Execution and ownership

- Record exact job, session, and worktree identities before relying on them for
  recovery or cleanup. Give each job one writable worktree and an unshared
  OpenCode session. Do not infer ownership from names or the current directory.
- Workmux owns worktrees; OpenCode owns execution and permissions. Worktree
  separation does not imply OS isolation or per-job resource containment.
- A denied or pending permission is visible as blocked/denied, not success.
  Recovery must preserve that distinction and must not bypass permissions.
- Do not blindly retry execution with ambiguous side effects after disconnect,
  timeout, cancellation, or crash. Persist uncertainty and require reconciliation
  or an explicit operator decision. Retrying an idempotent submission is not
  permission to retry its execution.
- Cancellation requests are not proof that tools stopped. Establish the actual
  execution state before releasing ownership, cleaning up, or rerunning work.

## Outcomes and recovery

- Persist lifecycle state, terminal outcomes, and durable output references so
  they remain discoverable after process restart or client disconnect. Expose
  missing output explicitly rather than presenting a broken reference as success.
- Store validation status and evidence separately from process exit status.
  Exit zero alone does not establish a validated change; missing validation,
  permission blocks, and ambiguous outcomes must remain visible.
- For the experimental agent adapter, use the local single-user, dedicated-
  session completion policy in [agent integration](agent-integration.md): one
  exact supplied prompt consumed once in full ordered history, explicit terminal
  outcome, stable profile/transcript, no active loop or pending input/permissions/
  forms. Record system/synthetic context without asserting its producer or causal
  ownership. Additional user/command/shell execution inputs and profile changes
  remain unknown. Known foreground tools require terminal completed/error states;
  running/streaming/unsupported tools hold capacity. Runtime-tracked background
  shells require exact ID/session/location-bound exit observations and before/
  final resource checks; an outer tool's return is insufficient. Missing resources
  stay unknown. Idle/CLI exit alone is insufficient. This is a cooperative local
  scope, not malicious same-user API defense or escaped-daemon detection.
- Make notification delivery recoverable from durable state, with retriable
  delivery and stable job identity for duplicate handling. A lost notification
  must not erase an outcome or require executing the job again.
  The first implemented interface is [local CLI pull/ack](notifications.md): one
  frozen completion event per job, atomic with terminal transition, redelivered
  until explicitly acknowledged. Proactive chat/network delivery is not provided.

Resource admission must also honor the memory floor, heavy-phase separation,
and non-deadlocking token policy in [architecture](architecture.md).
