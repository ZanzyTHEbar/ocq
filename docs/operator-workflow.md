# Operator workflow (unreleased)

This describes the retained agent/core contracts and the previous operator
panel. For current panel interaction see [Goals and sessions](goals-and-sessions.md).

## All-machine panel

The default OCQ panel discovers runtimes for every machine in Herdr's public
catalog, including this computer. Herdr supplies machine routing; HC, OCQ and
OpenCode run together on each execution machine. No OCQ remote executor,
transport relay, machine installer or global Main is introduced.
The matched bridge must already be available as `ocq-herdr` in that machine's
execution environment. Missing commands or rejected exec probes are unavailable,
not evidence that its queue is absent.

Work lists projects from all connected runtimes. Queue shows Jobs and
Notifications simultaneously. Inbox shows project mail and notifications as
distinct, non-consuming observations. Rows retain machine/project ownership;
their identity is backend core + queue + entity ID, not a machine label.
Opening an observation never acknowledges mail or a notification.

An owner starts the existing foreground push endpoint with `--runtime` and an
existing verified machine-local HC binding. It publishes a private, exclusive,
read-back-verified registration at `$XDG_CONFIG_HOME/ocq/runtime.json` (or the
standard user config directory). `ocq-herdr runtime-discover --json` reads only
that registration and checks its owner/modes, protocol and exact live
PID/start/boot/executable image. It never opens/initializes a queue, enrolls a
project, starts HC/OCQ or installs software. Its private stdout contains auth
material and is consumed only by trusted native code in memory.

Catalog refresh is bounded to two concurrent public Herdr exec probes. Queue
projections come only from each source's existing authenticated push/watch
stream, not recurring exec or SQL polling. Sources have separate revisions,
cursors, pending commands and socket ownership. Reconnect, route/alias changes
and panel changes invalidate confirmations; retained observations are stale,
not empty or completed. An unsettled connection holds only its own source.

Registrations without an owner-supplied client-reachable endpoint are
machine-loopback-only. A remote runtime in that state is detected but connection
unavailable. The UI never invents WebSocket forwarding or creates SSH routes.
An owner may advertise an already provisioned reachable `wss` endpoint or
explicit loopback route using `--advertise-endpoint`; all existing TLS/loopback
rules still apply. Nothing in discovery proves transport reachability.

Machine-runtime mode authenticates exact core and queue and retains the
connection's current Herdr route label for peer-bearing action claims. The core
and queue are identity; the label is routing/display only. Each project action
also supplies explicit project/space/canonical folder context. Existing
bindings are checked server-side. New setup requires explicit user choice of a
Herdr project space and the existing canonical-primary-Git checks; discovery
never enrolls arbitrary folders.

Machine-runtime workflow registries use the existing profile/dispatcher
contract below with empty `space` and `folder`; those are bound to explicit
project action context, not duplicated per project. Roles, the one queue
dispatcher, image and HC owner remain machine-local. Static reviewer policy is
unchanged. Main selection/role/journal remains per ProjectID and checkout.
The endpoint permits only the existing reviewed workflow/native pointer actions;
aggregation does not enable legacy remote submission, reconcile or mail mutation.

The single-context view/association and original local protocols remain as
legacy contracts. The named legacy view is retained; the installed default is
the aggregate surface. Independent aggregate visual/two-source/offline/action
acceptance and actual intended-machine Main workflow verification are separate
gates; earlier local acceptance is not relabeled as those gates.

Work can author Main setup and goals through the foreground push endpoint. This
is not a release or evidence that the complete journey has passed native
acceptance. The real Main → worker → exact-artifact reviewer → handoff scenario,
human attention routing and lost-response recovery must pass before deployment.

## Product interaction

In a local Herdr space pointing at the canonical primary checkout:

1. Open Work and choose **Set up project**. The server inspects the current branch;
   detached or linked worker checkouts are refused without changing Git.
2. Name the project; configured Main, implementation and independent review roles
   are selected by default and editable under **Advanced**. Review one readable
   confirmation showing the project, machine, canonical repository and branch,
   then **Create Main**. For a legacy bound project, **Adopt Main** refers only to
   its exact bound session, never a session discovered by title.
3. Write one goal, review its full goal text and **Start goal**. This sends one
   distinct Main input; opening history never resends it.
4. Main uses the existing Work prepare/wait/capture/review/handoff commands.
   Goal links are explicit retained operation/request identities, not timestamp
   or prose inference. Jobs, output and stored attention remain inspectable.
5. **Open** opens Main or the selected worker in your existing opted-in
   OpenCode tab. Choose a tab in Herdr if more than one matches. OCQ does not
   launch another client. Humans handle permissions in OpenCode.
6. Inspect the handoff and choose **Keep separate**. This leaves the owned
   checkout intact; it is a UI acknowledgement, not an integration receipt.
   Integration remains unauthorized. No integration mutation endpoint is added.

Actual blockers or unknown ownership stop progression. A lost author/start
response requires exact retained-record recovery, not another goal or Main.

Setup, goal and task confirmations retain the complete immutable
preview, native binding, operation/session IDs and server hash checks. They do not
require paging through JSON. **Technical details** optionally exposes the full
retained evidence; the explicit action confirms the semantic view of those same
bytes. Permissions remain human-controlled in OpenCode. Ordinary OpenCode
sessions are not automatically OCQ projects or jobs; jobs appear after admission,
and project mail after submitted work/review. Empty lists do not prove completion.

## Owner capability boundary

Product users do not supply JSON, profile paths, credentials, endpoints or CLI
commands. The owner provisions the existing automatic association, matched
`ocq`/`ocq-herdr`/mod/TUI asset, an isolated existing HC binding and a private
workflow registry. This is a separate explicit foreground lifetime, never a
shared-service adoption or persistent daemon installation.

The owner runs the ordinary matched CLI on the isolated existing queue:

```text
ocq --state-dir STATE dispatch --slots 1 --memory-floor-bytes 6442450944
```

Use the canonical absolute CLI path in the actual invocation. One queue execution
slot leaves room for persistent Main; review follows implementation, never runs
alongside it. Run only with the two-agent budget available. The endpoint verifies
the exact PID/start/boot, executing image, argv, held queue lock and matching
discovery/configuration environment; readiness also needs 6 GiB available memory,
packaged native assets, Workmux, exact existing HC and configured agents/models.
This is a point-in-time capability observation, not a completion guarantee.

Pass `--allow-actions --hc-binding-file FILE --workflow-registry-file FILE` to
the separately foreground-owned `push-serve`. The default remains read-only.
`--allow-actions` alone does not enable onboarding or the goal composer.
The historical native dispatcher validator remains `commands_only=true`.

The registry is an absolute, nonsymlink, owned `0600` file, at most 128 KiB.
For the Machines panel (`push-serve --runtime`), set registry `space` and
`folder` to `""`; these are resolved from the user's explicit checkout selection.
Keep the endpoint's own `--mod-space`/`--mod-folder` and private HC binding
matched to that foreground runtime as described in
[push transport](push-transport.md). The single-context endpoint uses the
explicit placeholders below instead:

```json
{
  "protocol": "ocq-operator-workflow-v1",
  "space": "EXACT_LOCAL_SPACE",
  "folder": "CANONICAL_PRIMARY_CHECKOUT",
  "queue_identity": "EXACT_QUEUE_SHA256",
  "dispatcher": {"pid": 123, "start_ticks": 456, "boot_id": "EXACT_BOOT"},
  "cli_sha256": "MATCHED_CLI_SHA256",
  "selections": [
    {"id":"main-choice","label":"Configured Main","role":"main","profile":"EXACT MainProfile JSON TEXT"},
    {"id":"implementation-choice","label":"Configured implementation","role":"implementation","profile":"EXACT WorkProfile JSON TEXT"}
  ]
}
```

These placeholders are not defaults. For panel setup the machine owner chooses
exactly one installed Main agent/model/variant and one for implementation,
using the real configured catalog and explicit ordered permissions for Work
profiles. A variant of `default` means the configured model's default. Setup
checks profile shapes, enabled chat models and exact agents. Legacy registry
review entries do not affect ordinary readiness or appear in setup. An explicit
restricted review still
fails closed at intake and again before prompting unless the OpenCode release's
permission semantics and the exact reviewer agent can be verified. This is not
an exact OpenCode release pin for project setup.

Registry bytes are frozen for the endpoint lifetime. Activating this configuration
requires a separately authorized foreground owner lifetime; do not start a
second dispatcher against the same queue or retry an uncertain effect. This
bootstrap only makes setup available. It does not register a project, start
Main or a goal, approve permissions, or enable handoffs. No provider credentials
are returned to the UI.

## Protocol and recovery

`main-author-v1` accepts only bounded typed create/adopt/send/reopen choices.
The selected endpoint folder is the only repository; IDs resolve to retained
profile bytes on the server. Project and operation IDs are generated by the
controller. Core `PrepareProjectMain`, preview/start/recover, immutable journals,
CAS and separate one-attempt stages remain the authority. Browser storage keeps
only recovery pointers, not an alternative execution journal.

The send retains its goal operation in disclosed input. Main records every Work
request with `project work link-goal PROJECT REQUEST MAIN_OPERATION` before
confirmation. `ocq-goal-lineage-v1` sidecars preserve exact input/work digests
without rewriting receipts. Existing filesystem observation pushes these links
through the Main list's additive `goals` projection. Projection reads remain
separate/non-atomic filesystem observations.

`ocq-attention-client-v1` and `ocq-attention-handoff-v1` are sibling navigation
contracts. Original inspection pointers still require idle/no pending requests.
Attention pointers permit busy/pending sessions but keep exact service, location,
ownership, profile, binding, executable and asset fences. The public TUI router
checks the exact route immediately after navigation and discloses its ID. This
instantaneous check never proves the terminal's subsequent current route.
Neither contract prompts, resumes, approves permissions, changes policy, retries
unknown launches or releases capacity.

## OpenCode V2 compatibility

Native service discovery, packaged clients and the TUI handoff use the OpenCode
V2 family and require the specific API responses, managed registration and public
client/router capabilities they use. Missing capabilities and changed shapes
fail closed; OCQ does not try substitute endpoints. Session identity, profile
and metadata checks remain mandatory. The observed release is still part of an
exact service identity; compatibility never adopts a changed instance.

Restricted review is different: an API shape cannot prove permission precedence
or suppression of denied tools. Its permission-policy gate still requires
source-validated 2.2.6, 2.3.0 or 2.3.2, including the exact agent at the exact
location. This is not a condition for opening an already-owned review session.

2.3.2 shell timeouts background commands by default. OCQ still checks
location-scoped shells and exact known shell IDs, not just tool completion.
Code Mode background jobs are not newly supported: unsupported tools/resources
still retain unknown capacity. Static review disables Code Mode through its
catch-all deny. This is cooperative accounting, not process sandboxing.

Compatibility source changes do **not** migrate an existing Main. Its retained
role pins the executable path and hash; its operations pin the service identity.
`project main refresh` refreshes the canonical anchor/binding, copies the selected
role unchanged, and does not upgrade its CLI or installed instruction entry.
There is currently no explicit role-image upgrade operation. Never overwrite
the old package, relabel a new image with its hash, resend an attempted goal, or
silently change the role/profile/binding to work around an upgrade. Continuing
such a Main requires separately authorized product work for a reviewed role
upgrade/recovery contract; a new build alone is not continuation acceptance.
