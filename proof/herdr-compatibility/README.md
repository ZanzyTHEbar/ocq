# Native compatibility acceptance

These opt-in proofs accept an explicit Herdr app 0.1.14 or 0.1.15 binary,
HC 0.1.19, an explicitly supplied network guard and a retained pre-contract
bridge binary. Use fresh private
profiles, never shared queues or running user sessions. No agent executes.
The exact candidate gate is not acceptance: each supplied app must pass the native
scenario. The peer proof retains the selected queued-job screenshot and requires
the personal layout to have no native errors or obstructing overlays. Only the
fresh private fixture home starts with the welcome marked skipped.

Supply private job-scoped `TMPDIR` and `GOCACHE`; builds use
`GOMAXPROCS=2 -p 1`. Run from the repository root. Runtime evidence is retained
under the private state home, outside disposable build scratch.

## Local boundaries

```sh
sh proof/herdr-compatibility/native.sh --local-corrections "$APP" "$HC" "$GUARD" "$OLD_BRIDGE"
sh proof/herdr-compatibility/native.sh --local-audits "$APP" "$HC" "$GUARD" "$OLD_BRIDGE"
sh proof/herdr-command-dispatcher/native.sh --default-xdg-ancestor "$APP" "$HC" "$GUARD"
```

- `--local-corrections` pauses real private receivers before authentication and
  between framing/payload writes. A later matching handshake must not revive an
  exchange invalidated by failed compatibility. Publication, capture and
  resolution use real disposable Git/queue state. Negative cases must leave no
  preparation, association, lane seal or mail. Separately labelled matched
  preparation controls prove the requests work; capture creates one private
  snapshot without publishing it. Seeded unknown state is not recovery evidence.
- `--local-audits` checks matched cancellation, all 69 action contracts across
  six incompatible boundaries, retained reads and the post-attempt native-launch
  barrier. It never configures, starts or selects another machine.
- Dispatcher `--default-xdg-ancestor` unsets `OCQ_STATE_DIR` and uses a private
  XDG state path with a symlinked ancestor. It checks canonical queue identity,
  final-directory symlink/missing-store refusal, then real native ownership,
  bind, harmless command execution and Drain. `--default-xdg` omits the ancestor
  alias. Neither mode implies native HOME-fallback coverage.

## Selected-machine race

```sh
sh proof/herdr-compatibility/native.sh --peer "$APP" "$HC" "$GUARD" "$OLD_BRIDGE"
```

This separate same-host stdio proof uses two private queues, actual
selected-machine reads, a delayed old matching handshake, a paused real private
receiver and a paused real native Start callback. It requires:

- Ordinary non-temporary registration in an initially empty private HC registry.
  Installed Herdr excludes temporary machines from spaces and `machines()`.
- Exact returned registry name, private core ID, session and transport target;
  native layout readiness and recorded target absence before one space creation.
- Direct bounded Go model anchors as each private core's default command, with
  exact terminal/foreground argv, PID and cwd. `space.new` omits `argv`, which
  otherwise invokes the installed interactive-shell wrapper.
- A returned old handshake that cannot authorize the newly selected peer, a
  stored job read from its distinct queue, all 69 remote mutation actions denied,
  and no stale private preparation, native claim, attempt or helper.
- A selected-peer read endpoint returning unavailable clears the detail and
  retains an error, with exactly one read attempt (no retry/local fallback).
- Unchanged receipts/jobs, terminal closure and verified owned-child shutdown.

The registered name is the native `--machine` / `exec({on})` identifier; it is
not the hostname or permanent core ID. Remote native IDs retain its exact
`name/` prefix. Only a descriptor running inside the peer strips that prefix
for its local space ID.

HC 0.1.19 supports aggregate `hc ls --all --json`, not `ls --on`. The proof joins
its exact-name connection state to a separately rechecked `hc machine ls --json`
registry identity. Installed aggregate summaries omit core ID and argv despite
the broader published schema. Missing IDs never establish identity; conflicting
IDs, registry changes and refused connections fail. Only `connecting` permits
bounded read-only polling. Exact known anchor addresses, including finished
terminals, establish helper absence; native snapshots verify full anchor argv.

Both cores start under their own guard. The stdio transport must inherit the
origin's approved filter and retain its ancestry, `NoNewPrivs` and `Seccomp`
checks; it never installs a second notification listener or offers an unguarded
fallback. This is not SSH, remote deployment or general transport validation.
Local proof success never substitutes for the selected-machine assertions.

## Diagnostics and ownership

Before launching a core/window, a guarded parser-only negative check verifies
bounded actual HC JSON error capture and sensitive-line redaction. A nonexistent
native selection separately verifies error retention without selection fallback.
Failure records retain exit/signal classification and safe control-error
projections, never full mutation argv, private payload or authentication streams.

Every native invocation installs its launch-time subreaper before runtimes start,
retains exact identities and uses pinned pidfds for cleanup. Success requires an
empty kernel child inventory and `wait4(ECHILD)`. `scenario-failed` remains failure
even when cleanup succeeds. Never retry an ambiguous mutation or infer an older
uncertain resource outcome from a later pass.

Two focused diagnostics accept explicitly prebuilt proof/owner binaries:

```sh
sh proof/herdr-compatibility/transport.sh "$COMPATIBILITY_PROOF" "$DISPATCHER_PROOF" "$HC" "$GUARD"
PROBE_ROOT="$ROOT" "$DISPATCHER_PROOF" cleanup-private-proof
```

Transport observation uses no queue, UI or registration. It reproduces a nested
listener failure with an actual read-only HC status exchange, recording byte
counts and process/FD/syscall metadata, not protocol buffers. Diagnostic cleanup
is not selected-machine acceptance. Identity-denial inspection requires a fresh
private absolute mode-0700 `ROOT`; a bounded child restricts its own dumpability.
Denied live identity reads must propagate, never become apparent process exit.
The child exits naturally, without a permission bypass or signal to it.
