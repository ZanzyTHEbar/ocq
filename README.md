# OCQ

OCQ is a local-first execution queue for Linux. Submit commands with durable
receipts, run them under a concurrency and memory-admission policy, and inspect
their output and outcomes. A project control plane adds OpenCode agent jobs,
owned workspaces, immutable results and a project inbox. An optional Herdr panel
provides stored views and explicit project actions.

**Alpha software.** SQLite is the queue authority; OpenCode owns agent execution
and permissions, and Workmux creates agent worktrees. Commands and tools run with
the user's privileges: OCQ is not a sandbox. Uncertain execution retains capacity
and is never automatically retried.

## Install

Requirements: Linux, Go **1.26.5+**, and private persistent storage for queue
state. SQLite is embedded; no separate database server is needed.

```sh
mkdir -p "$HOME/.local/bin"
GOMAXPROCS=2 go build -p 1 -o "$HOME/.local/bin/ocq" .
export PATH="$HOME/.local/bin:$PATH"
```

State defaults to `$XDG_STATE_HOME/ocq`, or `$HOME/.local/state/ocq`. Use
`--state-dir DIR` **before** the command to select another queue. Linux VMs and
containers can use durable volumes, including suitable virtiofs mounts; support
depends on locking, SQLite WAL/shared memory and sync semantics, not filesystem
names. All queue access must stay in one host/kernel coherence domain. See the
[storage and runtime contract](docs/storage.md) for volume setup, persistence,
ephemeral-storage limits and managed-runtime gaps. Keep state outside repositories.
For existing queues, follow the [coordinated schema-6 upgrade](docs/command-core.md#state-and-upgrades)
before using a newer writable CLI; `list` is not a read-only compatibility check.

## Run a command

Use a fresh private state directory for this example:

```sh
STATE="$HOME/.local/state/ocq-demo"
ocq --state-dir "$STATE" submit --key hello-v1 --cwd "$HOME" -- /usr/bin/printf '%s\n' hello
ocq --state-dir "$STATE" dispatch --commands-only
```

The dispatcher stays in the foreground. In another terminal, use the returned
job ID to inspect the command:

```sh
STATE="$HOME/.local/state/ocq-demo"
ocq --state-dir "$STATE" list
ocq --state-dir "$STATE" status JOB_ID
ocq --state-dir "$STATE" logs JOB_ID
ocq --state-dir "$STATE" notifications
```

Stop dispatch with Ctrl-C after the command finishes. Shutdown stops admission
and waits for its owned foreground commands. Defaults are **two slots** and a
**6 GiB available-memory floor**; a smaller or busy machine may leave work queued.
These are admission controls, not enforced resource limits. Strict FIFO means a
blocked head job prevents later work from starting.

Use a new idempotency key for each distinct request. Retrying the same request
with its original key recovers its receipt; it does not execute the job again.
See [command usage and recovery](docs/command-core.md).

## Projects and Herdr

Project commands register a repository and coordinating OpenCode session, submit
implementation or static-review work, and retain lanes, manifests and inbox mail.
The [project guide](docs/project-control-plane.md) explains the input formats and
separates execution, validation, review and integration.

The [Herdr guide](docs/herdr.md) covers installation, selected-machine inspection,
explicit inbox acknowledgements and local compose/intake. Guided forms or Advanced
raw support registration and implementation/review intake, with retained review
and explicit confirmation before admission to the independent dispatcher.
Local Queue forms also submit general commands or standalone agents independently
of projects, with exact retained review and receipt recovery; they never start a
dispatcher. Standalone agent acceptance does not establish runtime readiness.
Queue separately offers confirmed local commands-only Start/status/Drain, fixed
at 2 slots and a 6 GiB floor. Keep its terminal open through explicit Drain;
closing the terminal/pane/space can force-kill, not gracefully drain.
Remote controls/admission/compose/intake and agent-enabled dispatcher controls
are unavailable. Permission decisions
remain in OpenCode; OCQ never answers them automatically.

The agent adapter supports dedicated-session completion and a bounded set of
cooperative tools. Full implementation-to-review agent workflows and real
multi-machine deployment are not yet end-to-end verified. Read the
[agent contract](docs/agent-integration.md) before submitting agent work.

## Reference

- [Architecture](docs/architecture.md) and [reliability contract](docs/reliability-contract.md)
- [Completion notifications](docs/notifications.md)
- [Optional command-only systemd supervision](docs/supervision.md)
- [Development and acceptance checks](docs/command-core.md#development)

## License

[MIT](LICENSE).
