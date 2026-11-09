# ocq

A local-first **alpha command queue**, intended as a foundation for later
coding-agent integration—not a production agent system.

The Go CLI and singleton dispatcher use SQLite as the **sole queue authority**.
The Dagu proof passed its shell lifecycle scenario, but pinned-source review
found no suitable external prelaunch resource/head-admission hook. An external
adapter meeting the full contract would itself become another scheduler.
The approved follow-up therefore chose a narrow Go + SQLite core, with no Dagu,
Python, or JS/TS runtime dependency. See the [decision](docs/architecture.md).

The goal is reusable local orchestration, not another agent runtime. OpenCode
owns execution and permissions; Workmux owns worktrees; exactly one queue
authority owns scheduling. Existing live helpers must remain untouched.

## Start here

- [Architecture and proof-of-fit boundary](docs/architecture.md)
- [Required reliability contract](docs/reliability-contract.md)
- [Command core: usage, semantics, acceptance, and deployment gates](docs/command-core.md)
- [Experimental agent adapter: verified operations and correlation blocker](docs/agent-integration.md)
- [Project control plane and current Herdr workspace](docs/project-control-plane.md)
- [Dagu proof: commands, results, and adoption gaps](docs/dagu-proof-of-fit.md)
- [Contributor instructions](AGENTS.md)

Requires Linux and Go 1.26+. Build with bounded parallelism:

```sh
GOMAXPROCS=2 go build -p 1 -o runtime/ocq .
runtime/ocq submit --key harmless-example -- /usr/bin/printf '%s\n' hello
runtime/ocq dispatch
```

State defaults to `$XDG_STATE_HOME/ocq` or `~/.local/state/ocq`; it must be on a
persistent local supported filesystem, not tmpfs, a shared VM mount, or a network
share. Use global `--state-dir DIR` before the command to choose another location.
Dispatch defaults to two slots and a 6 GiB MemAvailable floor. Unknown launches
retain capacity and require explicit reconciliation; execution is never retried.

Completion notifications are durable **local CLI pull**, not chat delivery:

```sh
runtime/ocq notifications --limit 20
runtime/ocq ack COMPLETION_EVENT_ID
```

Unacknowledged events redeliver with the same ID after client/dispatcher restart.
Acknowledgement is idempotent and never retries execution or releases capacity.
See [notification semantics and migration](docs/notifications.md).

An alpha **project control plane** (Schema 5) binds an OCQ product project to an
exact repository root, commit anchor, and coordinating OpenCode session
(**Elephant**) with compare-and-swap binding revisions, receipt-first registration
and submission envelope retries, non-consuming `project results` across globally
acknowledged completion events, atomic idempotent `record-result` with explicit
`supersedes` lineage and source-labelled assertions (`verification=structure_hash_linkage_only`),
cooperative before/after worktree snapshot capture (`project capture-result`,
`source=machine_capture`, `verification=machine_verified_snapshot`) and read-only
live worktree-to-snapshot verification (`project verify-worktree`),
first-class **owned lanes** with set-once `fsfork_path` and sealed result
snapshots, a local immutable **integrator inbox** (`project lanes`,
`project inbox`, idempotent `project mail-send` / `project mail-ack`),
a static **Goldfish** review gate (`<= 16 KiB` deterministic inline review prompt
and `read`/`glob`/`grep` + `deny *` permission policy), and a read-only terminal
board usable in current `herdr-app` and `herdr-core`. See
[project control plane](docs/project-control-plane.md).
Review intake and pre-prompt checks verify the exact agent and the supported
OpenCode policy version; the session's ordered read-only rules are checked again
before prompting. This is cooperative isolation, not a filesystem sandbox.
Choose an exact project with `ocq project list`, then run
`ocq project board PROJECT_ID` for a plain terminal overview. It shows stored
job and unread inbox observations without acknowledging mail or reconciling work.
An experimental native `herdr-app` panel is in `share/herdr/mods/ocq`; its
short-lived `ocq-herdr` Go bridge calls read-only core operations directly, not
the `ocq` CLI. It shows stored project, job and unread inbox observations only;
it does not submit work, reconcile jobs, acknowledge mail or answer permissions.

An opt-in [systemd user-unit template](docs/supervision.md) and a real isolated
restart/drain proof are available for **command-only** queues. Nothing is
installed/enabled; this does not validate unattended agents or deployment.

An **experimental Workmux + shared OpenCode V2 adapter** now provisions a dedicated
worktree/session and journals a single supplied-ID prompt. Its real tiny Plan-agent
probe recovered after dispatcher crash through API-only reconciliation: one exact
consumed prompt, stable full history, explicit success, no active loop or pending
requests, and no tools. System/synthetic events are recorded session context,
without claiming trusted provenance. The retained job finished `agent_succeeded`,
released its slot, and kept `validation=not_run`; nothing was resent.
See the [session-scoped contract and limits](docs/agent-integration.md).
The next real Build fixture also passed read/patch, foreground shell verification,
four actual tool permission blocks with explicit fixture-only once approvals, and
a bounded background shell. ocq held the slot while the outer tool was complete
and the session idle, releasing it only after that exact runtime shell exited.
**Arbitrary tools, detached daemons, and production deployment remain unvalidated.**

There is no permission autoapproval, heavy-phase admission, service installation,
HTTP server, or unattended deployment. The historical Dagu
experiment remains reproducible with `bash proof/dagu/run.sh`; it is not used by ocq.

Licensed under the [MIT License](LICENSE).
