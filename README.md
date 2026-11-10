# OCQ

OCQ is a local-first execution queue for Linux. It accepts commands with durable
receipts, dispatches them under a local concurrency and memory-admission policy,
and records their outcomes and output. It also has an experimental OpenCode agent
adapter and project control plane.

**Status: alpha, not a production agent system.** The command queue is intended
for cooperative foreground processes. Agent execution, the Herdr interface,
remote operation, and unattended deployment have narrower validation; read the
[reliability contract](docs/reliability-contract.md) before relying on them.

## How it works

SQLite is the **sole queue authority**: it stores requests, FIFO order, lifecycle
state, and completion notifications. A singleton dispatcher admits work from
that queue. OpenCode owns agent execution and permissions; Workmux provisions
agent worktrees. Neither the project views nor Herdr are schedulers. The
[architecture](docs/architecture.md) explains the single-authority decision.

OCQ executes command arguments directly, not through a shell unless you explicitly
request one. Commands run with a restricted fixed environment, but retain the
user's filesystem and network permissions: **this is not a sandbox**. Submit only
commands you trust, and avoid daemonizing or unmanaged child processes.

## Build and install

Prerequisites:

- Linux and Go **1.26.5 or later**.
- A private, persistent local filesystem for SQLite state: ext4-family, XFS, or
  Btrfs. Temporary filesystems, network filesystems, overlay filesystems, and
  shared VM mounts are rejected. See [storage requirements](docs/command-core.md#build-and-use).
- For agent workflows only: a compatible OpenCode service and Workmux setup; see
  [agent integration](docs/agent-integration.md). Neither is needed for the
  command-only example below.

From a checkout, build the CLI and optionally put it on your `PATH`:

```sh
mkdir -p "$HOME/.local/bin"
GOMAXPROCS=2 go build -p 1 -o "$HOME/.local/bin/ocq" .
export PATH="$HOME/.local/bin:$PATH"
```

The SQLite driver is included as a Go dependency; no separate SQLite server is
needed. State defaults to `$XDG_STATE_HOME/ocq`, or `$HOME/.local/state/ocq` if
`XDG_STATE_HOME` is unset. Specify `--state-dir DIR` **before** the command to
use a different private persistent location. OCQ creates private state; do not
copy live state or remove its lock files.

## Command-only quickstart

Use a fresh state directory on a supported filesystem. This example submits one
harmless foreground command, then starts a foreground dispatcher. Use a new key
for each distinct request: a key is an immutable idempotency key, not a job name
that can be reused for different work.

```sh
STATE="$HOME/.local/state/ocq-demo"
ocq --state-dir "$STATE" submit --key hello-v1 --cwd "$HOME" -- /usr/bin/printf '%s\n' hello
ocq --state-dir "$STATE" dispatch --commands-only
```

The dispatcher stays running. In another terminal, inspect the queue and use the
job ID returned by `submit` or `list`:

```sh
STATE="$HOME/.local/state/ocq-demo"
ocq --state-dir "$STATE" list
ocq --state-dir "$STATE" status JOB_ID
ocq --state-dir "$STATE" logs JOB_ID
ocq --state-dir "$STATE" notifications
```

After the job finishes, stop the dispatcher with Ctrl-C in its terminal. Shutdown
stops admission and waits for its owned foreground commands; a stuck command can
therefore delay shutdown. The default dispatcher has two slots and a 6 GiB
available-memory floor, so a job may remain queued on a busy or smaller machine.
These are admission controls, not resource limits. `--commands-only` prevents
agent admission and polling; an agent at the FIFO head can block later commands.
See [command usage and admission](docs/command-core.md) for flags and recovery.

Completion notifications are durable local pull events. `ocq --state-dir "$STATE"
ack EVENT_ID` acknowledges one idempotently; it does not release capacity or
retry work. See [notifications](docs/notifications.md).

## Agent and project workflows

The experimental adapter provisions a dedicated worktree and OpenCode session for
an explicitly submitted prompt. Project records add bindings, owned lanes,
results, and a local integrator inbox. A terminal view is available through
`ocq project list` and `ocq project board PROJECT_ID`. Results and stored views
are observations, not proof that a deliverable was validated or that an agent is
still running. See [agent integration](docs/agent-integration.md) and the
[project control plane](docs/project-control-plane.md) for prerequisites, exact
semantics, and supported workflows.

OCQ never answers permission requests. Agent completion requires the documented
session and tool/resource observations; a closed client, disconnected view, or
completed shell tool call alone is not proof that all work has stopped. Unknown
ownership retains capacity and is not automatically retried. Arbitrary tools,
escaped daemons, remote disconnect handling, and production operation are not
broadly validated. Review isolation is cooperative, not a filesystem sandbox.

## Herdr workspace (experimental)

The native `herdr-app` mod uses a short-lived Go bridge to show stored OCQ project,
job, and inbox information. It can explicitly mark inbox messages read or archive
already-read messages. Local compose can prepare a `lane.blocked` message from a
selected lane to its integrator, preview the retained request, and send it only
after a separate confirmation. A previously prepared supported `lane.assigned`
message can also be previewed and confirmed. An uncertain send is not automatically
resent; use the explicit stored-receipt check instead. Remote compose is unavailable.

**Before using the new compose claim protocol, stop older panel and bridge
processes and ensure no previously launched old dispatch can start later.** Older
bridges do not require the durable claim, so mixed versions are not safe.

The compose journal checks the queue file's device and inode, but cannot detect an
in-place restore. Do not replace or retarget database paths concurrently with
compose operations. Explicit local project intake can register projects or queue
work, which an already-running dispatcher may start. Remote intake and compose
are disabled. The panel does not reconcile jobs or answer permissions; only the
human operator may approve permission requests. The mod is
trusted code running with the user's permissions. The bridge runs on the selected
Herdr machine and uses its OCQ state directory, or `OCQ_STATE_DIR` from its
environment. Remote use and disconnect behavior have not been validated end to end.

For **herdr-app 0.1.14**, build the bridge and make it available on the selected
machine's Herdr core `PATH`. Start the app once, then explicitly install and
activate the separate personal layout:

```sh
mkdir -p "$HOME/.local/bin"
GOMAXPROCS=2 go build -p 1 -o "$HOME/.local/bin/ocq-herdr" ./cmd/ocq-herdr
sh share/herdr/install.sh "$HOME/.config/herdr-app" "$(command -v herdr-app)" --activate
```

Replace the app-home argument with the actual absolute home if customized. The
installer requires a running app, checks the version, refuses to overwrite an
existing OCQ mod or layout, and switches the running window's layout. It does not
merge the previous layout's customizations. If installation or switching fails,
inspect the retained files and window before retrying. The dispatcher runs
independently of the panel. See [Herdr setup and limitations](docs/project-control-plane.md#current-herdr-terminal-workspace).

## Developing

The repository pins Go dependencies in `go.mod` and `go.sum`. SQL schemas and
queries live in `internal/core/sql`; the generated Go query package lives in
`internal/core/dbquery`. After changing SQL, regenerate with **sqlc v1.30.0** by
running `sqlc generate` from the repository root. Goose runs the locked,
transactional migration in the application; schema initialization and SQLite
connection safety checks remain OCQ's responsibility. See
[command-core development notes](docs/command-core.md#build-and-use) for migration
and generation details and [acceptance instructions](docs/command-core.md#real-acceptance-evidence).

Optional [systemd user supervision](docs/supervision.md) is command-only and is
not installed or enabled automatically.

## License

[MIT](LICENSE).
