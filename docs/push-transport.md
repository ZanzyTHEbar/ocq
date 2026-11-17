# Explicit native push transport

OCQ provides a foreground, authenticated WebSocket endpoint and a native GPUI
adapter. This does not change Herdr, HC, OpenCode, their configuration, helpers,
or services. No listener is started by opening the panel. There is no automatic
installation, tunnel, discovery, endpoint restart or deployment. Client transport
reconnection is automatic against one privately provisioned association.

## Start and stop explicitly

Build the CLI and bridge from the same source. Use an **existing**, deliberately
initialized private schema-6 queue; the endpoint never initializes or migrates
SQLite. Use matched OCQ writers too: they provide the after-commit hints described
below. The space and folder are explicit literal server-side scope.

```sh
# PRIVATE is an existing owned, nonsymlink mode-0700 directory.
# TOKEN is a NEW absolute filename inside PRIVATE. Its contents are not printed.
ocq-herdr push-token "$TOKEN"
ocq-herdr --mod-space "$SPACE" --mod-folder "$FOLDER" push-serve \
  --queue-dir "$STATE" --listen 127.0.0.1:0 --token-file "$TOKEN"
```

The foreground command prints the assigned endpoint URL and queue identity,
never the credential. Stop that exact foreground process with Ctrl-C or TERM;
it cancels sessions and waits for its reader/watch resources. Stopping it does
not cancel queue jobs, release unknown capacity, or establish job completion.
Token generation uses exclusive creation of an owned mode-0600 regular file.
Existing credentials are never overwritten. Retain/distribute the token privately;
do not put it in command arguments, URLs, tracked files, screenshots, or logs.
The token is scoped by the endpoint's one queue, space and folder, not by an HC
machine alias.

Plain `ws` listeners require a literal loopback IP. Non-loopback listeners require
both explicit `--tls-cert` and `--tls-key`; there is no certificate provisioning.
The native client accepts plaintext only at literal `127.0.0.1` or `[::1]`.
For another host, configure a reachable `wss://HOST:PORT/ocq` URL with a
certificate trusted by the native runtime, or provision a local tunnel separately.
The selected HC route does **not** supply WebSocket reachability or authentication.
An encrypted transport is still not filesystem sandboxing.

## Panel connection and authority

Select the intended machine, space and folder. The panel automatically resolves
the [private association](#automatic-native-association), authenticates and checks
the expected queue identity. It has no connection form or lifecycle controls.
An HC alias is a UI label, not an authenticated server identity.

The client uses Herdr's actual `websocket` module:
`WebSocket.connect(url)`, then asynchronous `read()`, `write(text)`, and `close()`.
This is not the browser WebSocket API or an `exec` stream.

One owned socket supplies the existing controller seam:
`updateHealth {state, reason, readAt}`, binding/revision, selected stored views,
`WorkOperator.apply`, and `MainClient.apply`. Subscription changes follow user
selection; subsequent projections are pushed by the server. There are no
recurring UI projection reads, exec/watch-wait renewals, refresh timers, or
fallback polling. Old unresolved native observers are not silently replaced.

Disconnect, scope/panel-generation changes and view disposal invalidate retained
confirmations. Automatic reconnect obtains fresh pins and resets projection
ownership; it never restores earlier confirmation authority. The prior read loop,
close and all issued writes must actually settle. The host bounds connection/handshake and write operations at
30 seconds and view-close cleanup at 250 ms; it exposes no connect-abort API.
An unresolved old connection remains unavailable rather than permitting a second
owner or claiming settlement. Disposal closes automatically. An unrecognized close
failure holds the physical slot instead of resetting it after either host deadline.
The pinned native runtime's exact terminal close result,
`WebSocket connection is closed`, is treated as an already-closed resource,
not a generic transport failure; see the native evidence below.

The default endpoint is read-only. To allow reviewed OCQ actions, its owner must
explicitly add `--allow-actions` when starting it. Authentication alone never arms
an operation. The panel keeps its original exact retained-page review, current
scope/generation/image guards, durable uncertainty markers, arm-before-attempt
and receipt-first recovery rules. A disconnect invalidates all old confirmations;
it does not authorize resending an uncertain action.

Supported explicit controls include prepared task preview/confirm/recover/output/
handoff, Main preview/start/recover, client-intent preparation, inbox
acknowledgements, compose, registration/project intake, command/agent submission,
result publication/capture, queued cancellation, local command resolution and
separately human-confirmed local agent reconciliation.
These reuse the existing core action contracts and CAS rules. Legacy local-only
controls remain local-only; prepared task/Main/client intents retain their own
explicit peer pins. Explicit agent handoff verification is GET-only and remains
available without mutation opt-in; its configured-service/version/ownership
checks are unchanged.
Without an explicit private HC binding, native client open/verify and dispatcher
execution controls are not exposed:
the endpoint's queue/space/folder pins do not bind the ambient HC host/core.
These routes require the separately reviewed HC binding and explicit launch
authority, never an arbitrary native execution request. The UI does not launch
a second helper or poll readiness. Disabled controls do not imply success.
Stored status/receipt reads are separate from live execution authority.
Work status/recovery/output/handoff, mail read-back and owned-worktree inspection
controls remain enabled independently of mutation readiness, including after an
uncertain operation reconnects to a read-only endpoint. Private preview,
preparation, arm and attempt controls retain their original mutation gates.

### Explicit private HC backend (experimental)

An endpoint owner can supply `--allow-actions --hc-binding-file FILE`.
`FILE` must be an absolute, owned `0600` regular JSON file, at most 16 KiB,
containing exactly the fields of `ocq-private-hc-v1`:

```json
{
  "protocol": "ocq-private-hc-v1",
  "socket": "/absolute/private/core.sock",
  "device": 0,
  "inode": 0,
  "uid": 0,
  "pid": 0,
  "start_ticks": "EXACT_EXISTING_PROCESS_START",
  "boot_id": "EXACT_EXECUTION_HOST_BOOT",
  "host": "EXACT_EXECUTION_HOSTNAME",
  "core_id": "EXACT_EXISTING_HC_CORE_ID",
  "space": "EXACT_SELECTED_SPACE",
  "folder": "/absolute/selected/folder",
  "queue_identity": "EXACT_EXISTING_QUEUE_IDENTITY"
}
```

These are placeholders, **not usable default pins**. Use independently retained
owner receipts for the existing isolated core. The socket path must be canonical,
owned, inaccessible to other users and inside an owned `0700` directory.
The bridge connects directly to that Unix socket, checks `SO_PEERCRED` against
PID/UID/start/boot/host and socket device/inode, then checks JSON-RPC generation 1,
HC 0.1.21 and the exact core ID. It checks at endpoint startup and on each native
operation. No HC CLI, discovery, SSH/machine routing, autostart or reconnect exists.

The explicit read `hc-binding` returns the verified exact binding and its SHA256.
Review those retained bytes alongside the original pointer/intent pages. Native
commands require `--hc-binding-sha256 EXACT_REVIEWED_DIGEST` in addition to their
original compatibility/context/preview arguments. The digest becomes part of
client authorization claims and immutable dispatcher targets; a refreshed core
or socket cannot inherit an old authorization.

`client-open` retains its original arm/one-attempt/receipt-first contract and
launches only the pointer's retained argv/cwd, with no parent or notifications.
`client-verify` reads only the exact recorded terminal on the pinned core and
checks argv/cwd/pointer/binding/space labels. Neither verifies the TUI's current
route. An ambiguous run is never repeated or adopted by label.

For dispatch, explicitly select the original native pane/terminal/tab. Prepare
and review the fixed intent, arm it, then call `dispatcher-attempt` once. The
backend commits the attempt before its single pinned-core `terminal.run`;
only the exact helper argv is used. It retains the returned terminal ID and
waits for the durable birth via filesystem events. The helper cannot dispatch
yet. The native frontend must present the exact new terminal in a distinct
pane of the original tab and call `dispatcher-bind` with that pane and the
actual PID/start. The backend independently checks pinned-core terminal labels,
argv, cwd and foreground PID against the durable birth and original run response.
Binding then waits for durable readiness by events. Missing run response prevents
binding/adoption; unbound helpers time out without dispatch. Drain still uses
the original separate durable claim and exact pidfd TERM; retire remains
limited to proven stopped lifetimes. Neither status nor reconnect binds or starts.

Both helper binding waits and backend birth/readiness waits use filesystem
notifications installed before the first snapshot, with bounded deadlines and
fail-closed watch loss. There is no readiness polling or exec fallback in this
backend. The native frontend reads and pages the exact HC binding separately,
then includes that retained binding in the dispatcher/client review. Reconnect
invalidates its approval. Dispatcher attempt invokes only the backend run.
Explicit focus and `terminal.keep` present that returned terminal without
launching another; bind uses its actually observed distinct pane/tab/PID/start.
Only the acknowledged start continuation offers bind, never read/reconnect.
Drain and stopped-only retirement have separate retained reviews. Status reads
are explicit; queue/job projections continue through server push. The legacy
native launch/readiness polling path has been removed.

Keep the selected native space available through retirement. If its last terminal
exits and Herdr removes the space, action authority expires; OCQ does not recreate
or adopt a replacement. A closed observation subscription requires explicit
reconnect/read-back, never automatic bind, launch or signal replay.

The backend's isolated HC acceptance now exercises a real owned core, fixed
commands-only helper, durable birth/bind/readiness, unsolicited command outcome
push and explicit drain/exit. It also exercises the guarded native inspection
terminal's open/verify/stored-receipt contract against an exact saved idle worker
session. These checks are not rendered-control or current-TUI-route acceptance.
HC's sampled `foreground` is optional at birth: a present sample must match,
and the core independently checks pidfd/start/image/argv plus the live kernel
tty, process group, session and foreground process group. An absent sample is
not replaced with a guessed process.

Separate native rendered-control acceptance covers reviewed HC identity,
one backend helper launch, explicit presentation/bind, pushed command completion,
drain, stopped read-back and retirement at 320/360px panel widths. It also covers
read-only paging of a historical consumed client receipt without HC approval.
That pass does not launch a client, consume a new pointer or verify a TUI route;
private-CA transport and retired-connection physical settlement remain host
integration boundaries.

Client pointer preparation and revalidation require bounded GET evidence of no
active loop, pending inbox/permission/form, an explicit latest idle outcome and
an unchanged exact session. Native inspection never resumes or sends input.
An isolated HC core receives only a fixed allowlist of the bridge's original
same-user configuration/service validation environment, not client-supplied
environment or arbitrary launch flags. This context travels privately, never in
argv, diagnostics or pointer receipts. Only after those original checks does the
ordinary TUI receive private generated data/state/cache directories under the
bridge's temporary directory; it still uses the explicit verified server.
An HC terminal receipt proves launch admission, **not** completion of the
launcher's further validation or a successful TUI attach. The separate immutable
`.launch.attempt` receipt is also not proof of a rendered route. Never repeat a
consumed pointer just because the client was stopped before that stage.

### Already published capture receipt recovery

`capture-receipt SPACE QUEUE PROJECT JOB SEQUENCE MANIFEST_SHA256` reads one
original already-published `machine_capture` association. It checks exact agent
receipt/location ownership, stored canonical content/digest, deterministic
association lineage and original `machine_verified_snapshot` / `not_run`
provenance in one read-only SQLite snapshot. It does not read or recapture the
live worktree.

`capture-receipt-preview` has the same exact target and returns those original
manifest bytes with the receipt. Private preview requires endpoint owner
`--allow-actions` and the explicit matched action contract
`capture/recorded-receipt-preview/v1`; ordinary receipt inspection remains
available without mutation opt-in. Both routes check the endpoint's queue/space.
They have no payload/import endpoint, no claim, arm, attempt, publish, seal/mail
repair or acknowledgement. Repeated receipt reads return the original
association; an existing publication is recovered rather than executed again.
The importer's rejection of `machine_capture` bytes is unchanged.

Private compose/intake/submission/publication/resolution bytes travel inside the
authenticated socket, not argv, another TCP listener, rendezvous polling or
stdout logs. The Go bridge invokes the same explicit command validators in
process with bounded captured output; there is no shell/arbitrary-executable
dispatcher. The original public CLI transport remains available independently.

## Wire and observation contract

- `/ocq` admits native WebSockets only: browser Origin headers and URL queries
  are rejected. The first bounded text frame must authenticate within five
  seconds using `protocol=ocq-push-v1`, `type=auth`, the token, space and folder.
  The fixed-length credential is compared in constant time. Errors never echo
  frame contents or credentials.
- Welcome supplies a random connection ID, the matched read descriptor, the exact
  control allowlist and an explicit `mutations` capability (false by default).
  Controls have strictly increasing
  decimal-string IDs. Subscribe carries a bounded `ocq-observe-v1` request pinned
   to the supplied queue, store and bridge contexts.
- Explicit `command` frames have the current connection ID and an allowlisted
  action with its original versioned flags. `command-received` is only a transport
  receipt, not durable admission or success. `command-result` returns the original
  bounded bridge DTO. An accepted operation settles through its core journal even
  if the reply is lost; the client never automatically resends it. Up to four
  command promises may be outstanding; server dispatch is serial per connection.
  Request IDs are not durable mutation idempotency keys.
- Snapshots retain `single_sqlite_snapshot` consistency for SQL views, committed
  decimal-string revisions, and the existing device/inode/schema/image/context
  pins. Generation, selector and sequence guards reject stale responses.
  Context replacement/watch loss closes the connection; observation reconnects
  automatically using the same exact private association and fresh pins.
- Filesystem logs and work/Main/client journals remain separately observed,
  non-atomic with SQL. They are not promoted to committed SQL state or verified
  execution/test evidence. Log cursors advance on the server, within a 64 KiB
  selected window; the UI does not renew reads to follow output.
- Linux inotify watches are installed before the initial snapshot. Notifications
  coalesce into one pending hint; `poll(2)` blocks for events/cancellation, not a
  timer. There is no periodic server projection read. Actual database filenames
  are `queue.db`, `queue.db-wal` and `queue.db-shm`. SHM and lock activity are not
  authority changes. A retained read-only connection and ignored empty-WAL
  create/close/delete events prevent observation from feeding back into itself.
- A WAL write can precede publication of the commit. Consequently the endpoint
  also installs a private, at-most-one-byte `observation.notify` hint file.
  Supported OCQ `store.write` calls touch it **after** successful COMMIT. The byte
  contains no revision or authority and needs no durable delivery; SQLite alone
  supplies the next coherent snapshot. Hint failure never changes a committed
  operation into a reported database failure or an execution retry. WAL/main-file
  notifications remain additional hints. Raw SQL writers and older unmatched
  producers do not provide this after-commit guarantee; arbitrary external SQL
  monitoring is not supported.
- The hint file is retained, not deleted on session close; concurrent observers
  can use it. No receipt, journal, schema or queue authority is rewritten.
  Directory creation/journal/output writes install fresh descendant watches.
  Symlinks are not followed; worktrees are excluded. Overflow, directory/watch
  loss or bounds failure closes the channel, with no polling fallback.
- Bounds: eight endpoint sessions; 16 KiB auth/subscription/control frames,
  1 MiB command envelopes (inner payload limits still apply); 1 MiB snapshot plus
  bounded envelope; eight selectors; 1,024 watched directories per session;
  one pending reader frame and coalesced dirty hint; five-second writes.
  Slow consumers are disconnected rather than accumulating an unbounded backlog.
  More than eight retained selected pages reports `limit_exceeded`; later pages
  are not subscribed. Reopen the panel to reset retained pages.

## Source and validation

Implementation:

- `cmd/ocq-herdr/push_serve_linux.go`, `push_session_linux.go`, `push_commands_linux.go`: explicit commands,
  first-frame authentication, scope, controls, bounded snapshots and lifecycle.
- `internal/core/observation_events_linux.go`: cancellable inotify watches and
  advisory commit hints. The three-line addition in `internal/core/store.go`
  emits the necessary post-COMMIT hint without changing transaction results.
- `share/herdr/mods/ocq/push-connection.js`, `stored-updates.js`,
  `compatibility.js`: native input/socket ownership, controller integration and
  explicit action allowlisting and fail-closed unsupported actions.
  `agent-handoff.js` and `worktree-check.js` route their former direct exec imports
  through that same boundary rather than bypassing the selected socket.
- `internal/core/{project,publication,resolution}_socket*_linux.go`: concrete
  private-byte adapters retaining journal/preview/claim semantics.
- `main.js`: no connection chrome; replacement of its two private
  transport methods. B's task-first rendered UI and Main/client/Work edits remain
  intact. `command-submission.js`, `result-publication.js`, `command-resolution.js`
  replace their former rendezvous transport only, retaining review validation.
- `github.com/coder/websocket v1.8.15` is the server WebSocket dependency.

Checks run serially with `GOMAXPROCS=2`, `-p 1`, and verified job-scoped scratch:

```sh
go build -trimpath -p 1 -o "$RESULT/ocq" .
go build -trimpath -p 1 -o "$RESULT/ocq-herdr" ./cmd/ocq-herdr
go vet -p 1 ./...
go build -trimpath -p 1 -o "$RESULT/ocq-acceptance" ./proof/command-core
"$RESULT/ocq-acceptance" "$RESULT/ocq"
```

Build, vet and real command-core acceptance passed. Native Herdr 0.1.15 checks
and a private headless GPUI fixture exercised the actual endpoint and adapter:
auth/subscribe, a separate CLI commit pushed into visible queue rows without
another subscription/read, explicit disconnect/reconnect, scope refusal and
endpoint stop. A real-socket contract check also verified Origin rejection,
wrong token/scope rejection, subscribe/unsubscribe/ping and mutation refusal.
Idle endpoint CPU counters and subscription counts were stable between events.
Fixture resources were explicitly stopped; no production listener or deployment
was created. Protected receipts and screenshots are retained outside the tree.

The later real `proof/push` scenario additionally exercises TLS with private
client trust (including untrusted-certificate refusal), real command draft/
retained preview/arm/attempt, dropped cancellation reply and exact receipt
recovery without replay, slow-reader closure, and a separate CLI producer.
Its optional explicitly supplied existing session/repo context exercises real
project registration, prepared task preview/confirmation/recovery without
dispatch, and journal-only CLI changes pushed without a SQL revision change.
It never manufactures terminal results or starts an agent.

Acceptance is **not complete**: the later native command/TLS test driver hit an
explicit filesystem grant denial before connecting, and was stopped without a
grant workaround. Native mid-handshake disposal, populated Main/client and
terminal review/result flows, actual store replacement and complete native
mutation acceptance remain unproven. The host exposes no connect-abort API for
the 250 ms close budget; no immediate settlement claim is made. The original native
fixture used an owned headless compositor after Xvfb lacked the required DRI3
rendering support. No agent execution, shared service/configuration change,
production job, or installed-mod replacement was part of validation.
# Automatic native association

The corrective native panel has no connection form or lifecycle buttons.
Provision the display host privately before starting the app. This is separate
from deploying/starting an endpoint; the mod never starts either an endpoint or
an OCQ job to obtain observations.

The fixed local `process.run(APP_HOME + "/ocq-push/ocq-herdr",
["push-association", APP_HOME + "/ocq-push", MACHINE, SPACE, FOLDER])` bootstrap
uses an explicitly provisioned matched OCQ bridge, not an ambient PATH lookup.
It runs
only at startup/selected-context change, not on reconnect or a timer, and never
opens a queue. Native generated API declarations expose reads but no stat/lstat,
mode or UID validation, so the Go bootstrap does those checks. No HC alias is
used to discover an endpoint or read remote credentials.

The file is `APP_HOME/ocq-push/association.json`, where `APP_HOME` is the native
`appHome()` (`HERDR_APP_HOME`, otherwise the app's standard home). Its containing
`ocq-push` directory must
be owned mode0700, with a canonical nonsymlink path. Configuration and credential
are owned nonsymlink regular0600 files opened relative to the verified directory
descriptor. The credential reference is a basename in that same directory.
Provision the matched `ocq-herdr` executable there as an owned regular0700 file;
no Herdr binary is changed. The bootstrap receives this directory explicitly,
so no shell/profile/XDG override can silently select another association.
All seven fields are required; example placeholders are not usable credentials:

```json
{
  "version": 1,
  "endpoint": "wss://explicit-server.example:7443/ocq",
  "credential_file": "endpoint-token",
  "machine": null,
  "space": "sp00000000000000000000000000000000",
  "folder": "/absolute/selected/project",
  "queue_identity": "<exact 64 lowercase hex queue identity>"
}
```

`machine` is the exact selected machine string or explicit null for local;
`space` is the full native selected space ID (including its machine prefix for
remote selection), not a guessed local alias. The token file contains the
endpoint's 64 hex token bytes plus newline. Keep these files out of repositories.
Neither token nor private bootstrap stdout/stderr may be logged or copied.
The mod consumes the bootstrap result in memory, never argv, URLs, clipboard or
localStorage. Provisioning is an operator responsibility; the panel is not a
credential editor. After correcting a stopped configuration fault, selecting the
context again or restarting the app triggers a new automatic bootstrap.

The socket authenticates, validates the descriptor against selected context and
expected queue identity, then subscribes to server-pushed authoritative views.
One cancellable exponential-backoff timer with jitter (at most30s) recovers
transient transport loss only after the prior physical owner has settled.
No polling of projections/configuration, exec observation fallback, command
replay or old approval recovery exists. Unsafe/missing association, mismatched
identity or unsupported contract stops recovery with one quiet Check setup
affordance. An unrecognized physical close failure holds the global slot, never
clearing it on a grace period/deadline. Disposal/context switch cancels retry and
invalidates confirmations. Unknown command outcomes remain uncertain even after
observation reconnects. Human permissions and durable action contracts do not
change. Independent native lifecycle/visual acceptance is required before release.

### Corrective source verification status

An initial isolated native run established secure automatic association, coherent
Jobs and Notifications at 320/360px, actual stored output and invalidation of a
prepared acknowledgement on loss. Recovery initially held ownership because
the close rejection was not retained precisely.

Fresh native instrumentation of OCQ's calls (not Herdr implementation changes)
distinguished these outcomes on the pinned host:

- Client close overlapping the outstanding read: read rejected with
  `WebSocket closed by this endpoint`; the single memoized close resolved.
- Normal peer close: read rejected with `WebSocket closed by peer`; close
  rejected with exactly `WebSocket connection is closed`.
- Abrupt endpoint loss: read rejected with
  `WebSocket protocol error: Connection reset without closing handshake`;
  close rejected with exactly `WebSocket connection is closed`.
- Subsequent direct read, write and duplicate close on that exact old native
  resource each rejected with `WebSocket connection is closed`.

Only the exact terminal **close** result is accepted as already closed.
Neither a read error, an arbitrary close rejection nor elapsed time releases
ownership. The original read loop must finish and all issued writes must settle;
new writes are refused during closing. One successor then authenticates afresh.
The fresh native flow passed all three close paths, coherent pushed queue lists,
old-confirmation rejection and disposal with retry pending. No mutations were
armed, retried or replayed. This is pinned-host compatibility evidence, not a
documented guarantee for every future host; unrecognized close results hold.
Full exact-page review, project-rich Work and independent visual acceptance
remain separate release gates. Do not deploy from partial acceptance.
