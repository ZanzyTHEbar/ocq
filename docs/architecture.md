# Architecture: candidate, not implementation

ocq is a local-first adapter proof-of-fit. No scheduler or service is implemented.
The initial question is whether Dagu can provide the queue lifecycle required by
the [reliability contract](reliability-contract.md), with only a thin Go adapter.
No Python or JavaScript/TypeScript runtime is required by the intended adapter.

## Ownership boundaries

- **Queue authority:** exactly one authority accepts, persists, orders, admits,
  and reconciles jobs. Evaluate Dagu in that role first. Do not build a second
  custom queue alongside it; adapter metadata must not become another scheduler.
- **OpenCode:** owns agent execution and permission decisions. Each job has its
  own exact session identity; sessions are never shared across jobs.
- **Workmux:** owns worktree lifecycle. Each job gets one writable worktree with
  recorded ownership. A worktree is not OS isolation.
- **Service/admission:** a systemd service and admission policy can bound the
  queue service and regulate starts. They do not provide per-job tool cgroup
  containment when tools execute through a shared OpenCode server. Do not claim
  such isolation or apply disruptive limits to existing shared helpers.

## Resource admission

Keep initial implementation concurrency at **2** until profiling justifies a
change. A future experiment with **4 light jobs and 1 heavy job** is not a proven
safe configuration or a current capacity claim.

Use a configurable memory floor, initially **6 GiB pending measurement**. Gate
admission on Linux `MemAvailable`, not swap capacity. Include external/native
activity in profiling and capacity decisions; queue-owned process totals alone
do not describe host pressure. This is admission control, not memory reservation.

Separate heavy build/validation from implementation. A waiting heavy phase must
not retain an implementation slot while waiting for a global resource token.
Define token acquisition/release and restart recovery before enabling that phase;
no token dependency may deadlock inside the agent-slot pool.

## Bounded proof-of-fit

First use harmless shell jobs in an isolated experiment to observe Dagu's actual
acceptance, persistence, ordering, admission, completion, interruption, restart,
and output-recovery behavior. Include lost acknowledgments, duplicate submission,
and a head job blocked on resources. Do not run live OpenCode jobs until this
lifecycle evidence supports the contract; mark untested properties explicitly.

Only then evaluate exact OpenCode session and Workmux worktree ownership,
permission-block visibility, and separate heavy-phase admission. Existing live
helpers remain untouched. Keep private host diagnostics out of this repository.

If Dagu cannot satisfy a required property without duplicating queue authority,
record the failing evidence and assess a narrow Go + SQLite replacement. That
fallback is not permission to implement a speculative scheduler now.
