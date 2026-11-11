# Command-only systemd supervision

OCQ supplies an optional systemd **user-unit template** for foreground command
queues. It starts the existing dispatcher, not another scheduler. It is not an
agent service and does not install or enable itself.

## Herdr terminal lifecycle is not service supervision

The local Queue [Start/status/Drain control](herdr.md#start-inspect-and-drain-a-command-dispatcher)
uses a direct Go helper in an explicitly selected model pane's right split, not
this unit. It fixes commands-only dispatch at 2 slots and a 6 GiB floor; a FIFO-head
agent still blocks later commands. Preparation does not launch. Separate human
confirmation, durable one-shot launch intent and exact born/bound/ready evidence
precede admission; stored status recovery never relaunches or adopts a process.

Explicit owned Drain uses pidfd TERM and leaves the terminal open until actual
exit. **Native terminal/pane/space close can force-kill in about one second; it is
not graceful Drain.** HUP stops admission but promises no grace. There is no global
close interception, core-restart supervision or escaped-process containment.
Unknown lifecycle and process exit prove neither job completion nor released
capacity. Remote and agent-enabled dispatcher controls are disabled.

## Configure

Build `ocq` first. Copy `share/systemd/user/ocq@.service` into your user unit search
path, normally `$HOME/.config/systemd/user/`. Create a private mode-0600 instance
environment file at `%E/ocq/NAME.env`, normally `$HOME/.config/ocq/NAME.env`:

```ini
OCQ_BIN=/absolute/path/to/ocq
OCQ_STATE_DIR=/private/persistent/ocq-state
```

Use actual absolute paths, not `$HOME` expressions: `EnvironmentFile` is not a
shell. Quote values containing spaces. `%E` is resolved by the user manager,
which may have a different environment from your shell. The template checks
nonempty state and an executable binary before starting, preventing accidental
fallback to another queue. Do not point multiple instances at one queue.

State must meet [storage requirements](command-core.md#state-and-upgrades). Stop
dispatch and perform any schema upgrade before starting the service. The template
has no `[Install]` target; start it explicitly rather than enabling it at login.

For an installed instance named `commands`:

```sh
systemctl --user daemon-reload
systemctl --user start ocq@commands.service
systemctl --user status ocq@commands.service
journalctl --user -u ocq@commands.service
systemctl --user stop ocq@commands.service
```

A working user manager/session bus is required. Fix configuration failures before
resetting the exact instance's rate limit. If a user manager is unavailable,
manual CLI dispatch remains available; no system-wide service is needed.

## Runtime policy

The unit uses `dispatch --commands-only`, default two slots and a 6 GiB memory
floor. It neither admits agents nor polls their APIs. A queued agent at the FIFO
head blocks later commands; unknown demand still counts. Do not wrap agent
launchers or daemons as commands to bypass this boundary.

| Setting | Behavior |
| --- | --- |
| `Restart=on-failure`, `RestartSec=5s` | Restarts a failed dispatcher, never replays job execution |
| 3 starts per 60 seconds | Bounds repeated failures; investigate rather than resetting blindly |
| `KillMode=mixed`, TERM | Signals the main dispatcher first so owned foreground children can drain |
| `TimeoutStopSec=60s`, `SendSIGKILL=yes` | May force-kill remaining unit processes after timeout or main-process exit |
| `UMask=0077` | Private newly created files |

`/usr/bin/env --` execs the configured binary without a shell or wrapper loop.
Admission is not resource containment; the template adds no MemoryMax. A user
service does not source interactive shell startup files.

## Stop and recover

TERM stops new admission and waits for direct owned commands. A launch handoff
already in progress may finish before the signal is observed. Forced cleanup can
interrupt writes and is not successful job completion.

After a crash/restart, unresolved launching/running jobs retain unknown ownership
and capacity even if systemd killed their processes. Stop the exact instance,
inspect stored process identities, groups, outputs and side effects, then follow
[command resolution](command-core.md#recover-unknown-commands). Only afterward
restart dispatch. Notifications and acknowledgements do not resolve uncertainty.

Unit cgroup cleanup covers only its processes, not shared OpenCode tools, other
units or escaped daemons. Unattended agent supervision and per-job shared-server
containment are not provided by this template.

## Validate a configuration

Static unit syntax can be checked without installation:

```sh
systemd-analyze --user verify share/systemd/user/ocq@.service
```

The acceptance runner also has `--supervision OCQ`, which creates isolated
transient user-service fixtures and exercises restart, unknown ownership and
drain behavior. It requires a working user manager and permission to create
transient units; it is not a production deployment or a boot/login test.
