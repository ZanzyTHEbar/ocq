# Opt-in command dispatcher supervision

ocq supplies a portable systemd **user-unit template**, not an installed service
or a new supervisor. SQLite remains the sole queue authority. This work validates
harmless foreground command supervision, **not unattended agents or deployment**.

Artifacts:
- `share/systemd/user/ocq@.service`
- `share/systemd/user/ocq.env.example`

## Configuration and manual operation

The template has no `[Install]` target: it is deliberately not enabled at login.
No unit was installed/enabled, no manager configuration reloaded, and no lingering
or system-wide service configuration changed in this experiment.

After a **separately approved** installation/loading of the template into the
user's unit search path, configure one instance using its required environment
file `%E/ocq/NAME.env` (normally `~/.config/ocq/NAME.env`). `%E` is resolved by the
user manager, not necessarily the invoking shell's configuration directory.
Use an owned mode-0600 file, for example:

```ini
OCQ_BIN=/absolute/path/to/ocq
OCQ_STATE_DIR=/private/persistent/ocq-state
```

Use actual absolute paths; shell expressions such as `$HOME` are not expanded
inside `EnvironmentFile`. Quote values containing spaces. `OCQ_BIN` must be an
existing executable, and state must satisfy the existing private local persistent
filesystem/mode requirements. Do not point multiple instances at one queue or
use retained evidence as a service/migration experiment. Perform intentional
schema migration while dispatch is stopped before starting a service.

`/usr/bin/env --` execs the configured binary in the same main PID; there is no
shell, wrapper daemon or application restart loop. Both variables become single
arguments through systemd's `${...}` expansion. Native `ExecStartPre` checks
require nonempty state and an executable binary. In particular, an empty state
variable must not invoke ocq's default-queue fallback. Missing/invalid
configuration fails startup rather than selecting another queue or binary.

For a separately installed/configured instance named `commands`:

```sh
systemctl --user start ocq@commands.service
systemctl --user status ocq@commands.service
journalctl --user -u ocq@commands.service
systemctl --user stop ocq@commands.service
```

These are operator invocation examples, **not commands run against an installed
service in this work**. Use exact instance names; never broad stop/kill/reset
commands. Inspect and fix startup errors before resetting that instance's failure
limit. A healthy user manager/session bus is required. If unavailable, do not use
sudo, start another manager, enable lingering, restart a service or import global
environment as a workaround. The CLI still runs manually without systemd.

## Command-only safety boundary

The template uses `dispatch --commands-only`. This explicit mode:
- Keeps the default **2 slots / 6 GiB MemAvailable floor** and singleton lock.
- Does not admit agent jobs or poll agent APIs. An agent at the queued FIFO head
  blocks later jobs with a visible reason; it is not skipped/backfilled.
- Still counts all existing launching/running/unknown demand. Existing agent
  ownership is never released because its client or unit stopped. Stop dispatch
  before any separately authorized explicit reconciliation.

Manual `dispatch` without the flag preserves the prior cooperative agent mode.
The flag is a queue admission/observation restriction, not a Go supervisor. It
prevents the adapter's API CLI from accidentally auto-starting a shared OpenCode
service inside a supervisor-owned cgroup. It is not a sandbox: a deliberately
submitted v1 command can still run arbitrary programs with the user's powers.
Do not wrap agent launchers/daemonizing commands as v1 work to evade this boundary.

A user service does not source interactive shell startup files. Future agent
service work would need independently verified PATH/Workmux/OpenCode discovery,
configuration, credentials and permissions matching the intended user context,
and an existing shared server outside the unit cgroup. No credentials should be
embedded in unit files or tracked examples; do not change global environment/auth
to obtain a passing test. That agent-service workflow is **not validated here**.

## Stop, forced kill and restart semantics

The template explicitly sets:

| Setting | Meaning |
| --- | --- |
| `Restart=on-failure` | Restart a failed dispatcher, not its job executions; clean/manual stop does not auto-restart. |
| `RestartSec=5s` | Backoff rather than a hot restart loop. |
| `StartLimitIntervalSec=60s`, `StartLimitBurst=3` | Limit repeated failed/manual starts; investigate instead of repeatedly resetting. |
| `KillMode=mixed`, `KillSignal=SIGTERM` | Initial TERM targets the main dispatcher, letting it drain its owned foreground children. |
| `TimeoutStopSec=60s`, `SendSIGKILL=yes` | Remaining unit processes can be force-killed at timeout; with mixed mode, main-process exit also triggers final cleanup of remaining unit processes. |
| `UMask=0077` | Private newly created files. No arbitrary MemoryMax or concurrency increase. |

The dispatcher stops admission once it observes TERM/INT and waits for its direct
foreground children. A launch already in progress can complete its intent/spawn
handoff before the stop signal is observed; there is no instantaneous signal/SQL
transaction boundary. A cooperative child can finish during the drain interval.
If it hangs or exceeds the finite stop timeout, force-kill may interrupt writes.
Never infer successful completion or safe retry from unit inactivity alone.

After a dispatcher crash/restart, unresolved running/launching jobs become unknown
and keep capacity, even when systemd killed their known processes. Before command
operator resolution: stop the exact unit, independently inspect PID/start/boot and
process-group/cgroup exit plus outputs/side effects, then use `resolve --evidence`.
Resolution records `operator_reconciled`, not success or validation. Only afterward
should an operator explicitly start dispatch again. Notifications/ack do not
release unknown capacity or replay execution.

These semantics follow `systemd.kill(5)` and `systemd.service(5)`. Do not change
KillMode to `process`/`none` to leave unmanaged children behind, or disable final
kill to pretend a stuck unit exited. Unit cgroup cleanup applies only to processes
in that owned unit, **not shared-server agent tools, other units, escaped daemons,
or universal process-tree containment**. No shared runtime limit is configured.

## Validation without installation

```sh
systemd-analyze --user verify share/systemd/user/ocq@.service
GOMAXPROCS=2 go build -p 1 -o runtime/ocq .
GOMAXPROCS=2 go build -p 1 -o runtime/ocq-acceptance ./proof/command-core
GOMAXPROCS=2 runtime/ocq-acceptance --supervision "$PWD/runtime/ocq"
```

The proof first requires a usable user manager and permission to create transient
user services. It records intent/creation receipt, exact unit/description/command,
invocation/cgroup and main PID/start/boot before using those identities. It does
not install/link units or reload the manager. A record-and-exec proof launcher
preserves each systemd invocation identity; **systemd alone performs restart**.
The unit's environment-file/`env` expansion is exercised on the real manager.
The transient fixture checks nonempty `OCQ_STATE_DIR` with one `ExecStartPre`;
repeated `systemd-run --property=ExecStartPre` options replace rather than append
on the tested manager. The portable template retains both the nonempty-state and
executable-binary checks. Template syntax is verified separately; the transient
proof does not exercise its executable-binary preflight or invalid configuration.

One isolated scenario (two uniquely named transient phases, never concurrent)
uses a private fresh schema-3 queue, one-slot fixture, the same lifecycle/backoff
policy and a shorter **10s** stop timeout. Each harmless probe has a 90s safety
bound. It proves automatic restart after killing only the owned main dispatcher,
unknown held capacity and FIFO/no rerun, unchanged receipts/no premature outbox,
independent prior-child/group exit before safe stopped-unit resolution, follower
execution once afterward, pending notification/ack preservation, and clean TERM
drain with a live child and no new tail admission. Cleanup releases only owned
fixtures, stops only receipt-verified transient units, collects their definitions,
and verifies recorded processes exited and cgroups empty/removed on success/failure.

Observed on systemd **259**, Linux cgroup v2: real supervision and existing command/
notification acceptance passed. All test transient resources were stopped; no
agent execution or retained queue mutation occurred. Exact identities, checks and
cleanup are private in `RESULT-SUPERVISION.md`.

The template's full 60s timeout-expiry path, repeated-failure rate-limit exhaustion,
older distributions, login/logout/boot/lingering, production storage pressure and
unattended agents are not proven by this harmless scenario. Verify on the intended
target. Missing agent shell/resource evidence remains unknown; supervision is not
a workaround for that documented operational gate. No deployment is authorized.
