# Architecture: one Go + SQLite queue authority

ocq implements an alpha Linux CLI/dispatcher for harmless foreground commands,
plus an experimental Workmux/shared-OpenCode V2 provisioning/observation adapter.
The adapter's session-scoped completion is validated for a real no-tool Plan
prompt after dispatcher crash and a Build fixture with real read/patch, tool
permissions and cooperative foreground/background shells; see
[evidence and boundary](agent-integration.md). Arbitrary tools and detached
resources are not validated. SQLite remains the sole queue authority, and no
service is installed. The [reliability contract](reliability-contract.md) remains
the target; unimplemented agent/deployment requirements are not claimed as delivered.

## Decision: one queue authority

The core uses SQLite as the single authority for durable receipts, strict FIFO
ordering, resource admission, and recovery of uncertain ownership. An external
wrapper responsible for withholding, ordering, admitting, and reconciling jobs
would itself become a scheduler; maintaining a second queue authority would make
these guarantees harder to establish. OpenCode and Workmux retain their distinct
execution and worktree responsibilities.

The selected core uses `dbbootstrap` v0.1.1 with `modernc.org/sqlite` v1.61.0
(pure Go; requires Go 1.26.5), Goose v3.28.0 for its locked migration, and
sqlc-generated application queries. There is no HTTP server, plugin framework,
or generic backend layer. See
[command-core semantics and acceptance](command-core.md) for what actually works.

## Ownership boundaries

- **Queue authority:** ocq's single SQLite database accepts, persists, orders,
  admits, and reconciles command jobs. A singleton flock protects dispatch;
  short SQLite transactions protect concurrent CLI submissions. Its schema-3
  outbox stores one atomic terminal notification per job; schema 4 adds the
  project control plane and schema 5 adds
  owned lanes plus the local integrator inbox. Local pull/ack delivery is not
  another scheduler or a reason to release capacity. See
  [notification contract](notifications.md) and
  [project control plane](project-control-plane.md).
- **OpenCode:** owns agent execution and permissions through the normal shared
  service. Experimental v2 jobs preallocate unshared session/prompt IDs, verify
  location/ownership, and never infer agent completion from CLI exit. V1 commands
  still have no OpenCode sessions. No permission autoapproval is implemented.
- **Workmux:** owns worktree provisioning. V2 jobs create dedicated headless
  worktrees with hooks/file operations disabled; v1 commands create none. A
  worktree is not isolation. Owned worktrees/branches/sessions are retained, not
  automatically cancelled, deleted, merged, or adopted by name after lost acks.
- **Service/admission:** a systemd service and admission policy can bound the
  queue service and regulate starts. They do not provide per-job tool cgroup
  containment when tools execute through a shared OpenCode server. Do not claim
  such isolation or apply disruptive limits to existing shared helpers.
  The opt-in [command-only user template](supervision.md) uses systemd restart/
  cgroup lifecycle, not another Go supervisor. Its isolated crash/drain proof is
  validated; no persistent service is installed or enabled, and unattended agents
  remain excluded.

## Resource admission

The dispatcher defaults to **2** slots; keep this until profiling justifies a
change. A future experiment with **4 light jobs and 1 heavy job** is not a proven
safe configuration or a current capacity claim.

The configurable memory floor defaults to **6 GiB pending measurement**. Admission
uses real Linux `MemAvailable`, fails closed if unavailable, and subtracts all
launching/running/unknown declared demands conservatively. It examines only the
earliest queued receipt, never backfilling a blocked head. Include external/native
activity in profiling and capacity decisions; queue-owned process totals alone
do not describe host pressure. This is admission control, not memory reservation.

Heavy phases are disabled/not implemented. Later, separate heavy build/validation
from implementation. A waiting heavy phase must
not retain an implementation slot while waiting for a global resource token.
Define token acquisition/release and restart recovery before enabling that phase;
no token dependency may deadlock inside the agent-slot pool.

## Proof boundary and remaining work

The command-core acceptance uses real CLI processes, SQLite, MemAvailable, and
harmless foreground probes to check concurrent idempotency, head blocking, singleton dispatch, and crash
uncertainty without duplicate execution. A SQLite transaction cannot atomically
spawn a process: durable launch intent precedes spawn; restart turns unresolved
launching/running jobs into unknown and retains ownership/capacity. There is no
automatic rerun, even if the process is gone.

The approved next slice verified real Workmux receipts, exact shared-service
session creation, durable supplied prompt IDs, and an active-session dispatcher
crash. The initial stronger causal-provenance assertion failed on a synthetic Plan
reminder; that historical failure remains recorded. The user corrected the scope
to local single-user, dedicated-session completion. API-only reinspection of the
same job then established one exact consumed prompt, recorded auxiliary events,
stable full history ending in success, no active loop/pending requests/tools,
durable output and slot release, without another POST. This is not trusted
per-event attribution or deliverable validation. API-created permission/form
visibility was verified separately from actual agent-tool permission handling.
Unresolved jobs still hold capacity. Existing live helpers remain untouched.
Keep private diagnostics out of this repository.

A subsequent disposable Build fixture verified actual pending read/patch/shell
permissions hold capacity until an explicit fixture operator approves once;
product reconciliation never replies. The real file/diff and foreground shell
verification passed. A background shell's outer tool completed and the session
became inactive while the runtime shell still ran: ocq held its slot until an
exact-ID, session/location-matched terminal shell observation. Background exit
notifications are auxiliary context, and a later stable terminal idle can finish
the same single-prompt session. Runtime resource observations supplement, not
replace, the session checks. This is cooperative API accounting, not isolation.

Arbitrary tool/resource work, production migration, and unattended deployment are not
validated by these results. Client cgroups do not contain shared-server tools.
The agent proof used normal available slots and the existing memory gate, not the
expired reprieve.
