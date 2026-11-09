# Runtime provenance: superseded stronger optional guarantee

**Historical analysis, not a current blocker or runtime bug.** This investigation
assumed exclusive causal attribution for every internal event, beyond the user's
local single-user operational requirement. The user superseded that assumption
with the [session-scoped completion policy](agent-integration.md). Normal
system/synthetic events are recorded context, without claiming trusted producer
or causal provenance. API-only re-evaluation of the retained v2.1.4 job now finished
`agent_succeeded` with `validation=not_run`, no new prompt, and released capacity.

The evidence below is preserved, not erased or relabeled. At the time of this
read-only investigation the job remained unknown, and no input, observer change,
or capacity release occurred. Its finding was only that the inspected contract
does not support the **stronger optional attribution guarantee**. It was incorrect
to make that guarantee a prerequisite for local session-scoped queue completion.

## Observed API evidence

The investigation used the [official V2 API docs](https://opencode.ai/v2/docs/api),
the [published schema](https://opencode.ai/v2/openapi.json), and the previously
saved installed schema. These are experimental APIs; the schema version `0.0.1`
does not identify the runtime build. Exact IDs, raw responses, hashes, and source
copies are retained privately, not in this repository.

| Surface | Observed fields/result | Missing guarantee |
| --- | --- | --- |
| Exact owned user message GET | `data.{id,type,text,time,metadata}`; `type=user`, caller-supplied `metadata.ocq` | No server-authored execution/cause ID |
| Exact synthetic message GET | `data.{id,type,text,time}`; `type=synthetic` | No producer, originating input, execution ID, or provenance metadata |
| Retained idle history message | `{id,type,outcome,time}`; `type=idle`, `outcome=succeeded` | No prompt or drain ownership |
| Synthetic submission schema (inspection only) | Caller may supply `id,text,description,metadata,delivery,resume` | Description/custom metadata are not trusted attribution |
| `GET /api/experimental/session/{sessionID}/log` | SSE `data: {"type":"log.synced","aggregateID":…, "seq":18}` only | No replayed admission, delivery, or terminal payloads |

The exact-session log was read with `follow=false`, with no cursor and with
`after=0` and `after=1`; the operation-ID CLI form with `after=0` agreed. These
numeric cursors were diagnostic probes, not a supported recovery algorithm:
the inspected source requires cursors obtained from event envelopes or sync
markers. Every response contained only the same watermark. A watermark cannot
reconstruct omitted event contents or prove who caused them. Why this installed
service exposes no replay payloads is unresolved; persistence configuration,
retention, and build identity were not inferred or changed.

Exact-session inbox, form, and permission GETs each returned an empty `data`
array. Read-only SQLite inspection at that time found `state=unknown`,
`outcome=uncertain`, null exit code, `validation=not_run`, and empty terminal
evidence. These checks do not establish terminal causality.

## Source provenance and causal path

The public **V2** tag `v2.1.4` resolves to commit
[`68bc26b7da1990ce10ffecfbc1e09eb13aa5d824`](https://github.com/anomalyco/opencode/commit/68bc26b7da1990ce10ffecfbc1e09eb13aa5d824)
(`release: v2.1.4`, 2026-10-23). Selected files were fetched at that immutable
revision and all 27 downloaded blobs matched the Git tree's blob IDs. This is
not V1/main source. The installed CLI reports `opencode v2.1.4`; its binary hash
was saved privately. There is **no verified release-artifact hash linking that
binary/service to the public commit** (the release API returned 404). Thus this
is matching-version source evidence, not proof of the installed executable's
exact source. The API observations above stand independently.

Source links below all use that pinned revision:

1. [`plugin/plan.ts`, lines 56–93](https://github.com/anomalyco/opencode/blob/68bc26b7da1990ce10ffecfbc1e09eb13aa5d824/packages/core/src/plugin/plan.ts#L56-L93)
   reconciles a reminder in the model-context hook and persists it using
   `ctx.session.synthetic({sessionID,text,resume:false})`. Its creation/agent-switch
   listener also calls `synthetic`. Neither supplies producer or cause fields.
   This explains a plausible built-in origin, but does **not** authoritatively
   attribute the retained synthetic message to that plugin or that prompt.
2. [`plugin/host.ts`, lines 547–577](https://github.com/anomalyco/opencode/blob/68bc26b7da1990ce10ffecfbc1e09eb13aa5d824/packages/core/src/plugin/host.ts#L547-L577)
   forwards plugin `synthetic` directly to the session service.
   [`server/handlers/session.ts`, lines 370–382](https://github.com/anomalyco/opencode/blob/68bc26b7da1990ce10ffecfbc1e09eb13aa5d824/packages/server/src/handlers/session.ts#L370-L382)
   forwards HTTP synthetic input and caller metadata to the same service. A
   synthetic type or description therefore cannot distinguish these producers.
3. [`session/session.ts`, lines 283–315](https://github.com/anomalyco/opencode/blob/68bc26b7da1990ce10ffecfbc1e09eb13aa5d824/packages/core/src/session/session.ts#L283-L315)
   prepares synthetic payload text/description/metadata and admits it.
   [`session/inbox.ts`, lines 178–210](https://github.com/anomalyco/opencode/blob/68bc26b7da1990ce10ffecfbc1e09eb13aa5d824/packages/core/src/session/inbox.ts#L178-L210)
   publishes `session.inbox.enqueued` with `{sessionID,inboxID,item}`.
   [`schema/session-event.ts`, lines 208–265](https://github.com/anomalyco/opencode/blob/68bc26b7da1990ce10ffecfbc1e09eb13aa5d824/packages/schema/src/session-event.ts#L208-L265)
   defines delivery as `{sessionID,inboxID}` and execution success as
   `{sessionID}`. No structural root-input/producer relation is defined there.
4. [`session/projector.ts`, lines 640–669](https://github.com/anomalyco/opencode/blob/68bc26b7da1990ce10ffecfbc1e09eb13aa5d824/packages/core/src/session/projector.ts#L640-L669)
   projects delivered synthetic payloads into history.
   [`session/message-updater.ts`, lines 65–79](https://github.com/anomalyco/opencode/blob/68bc26b7da1990ce10ffecfbc1e09eb13aa5d824/packages/core/src/session/message-updater.ts#L65-L79)
   projects terminal events to idle messages, deriving the message ID from the
   event ID. Recovering that event ID alone would not recover a cause relation.
5. [`session/execution.ts`, lines 84–164](https://github.com/anomalyco/opencode/blob/68bc26b7da1990ce10ffecfbc1e09eb13aa5d824/packages/core/src/session/execution.ts#L84-L164)
   coalesces wakes until the inbox is empty and publishes session-level terminal
   events. It documents crash recovery through persisted execution claims, but
   does not expose a durable root-input receipt. Ordering within one session is
   not a statement that every intervening input belongs to one authorized prompt.
6. [`bus.ts`, lines 770–875](https://github.com/anomalyco/opencode/blob/68bc26b7da1990ce10ffecfbc1e09eb13aa5d824/packages/core/src/bus.ts#L770-L875)
   reads stored events and emits a captured-watermark `log.synced` marker.
   [`schema/event.ts`](https://github.com/anomalyco/opencode/blob/68bc26b7da1990ce10ffecfbc1e09eb13aa5d824/packages/schema/src/event.ts)
   defines envelope `id,created,type,data,durable.{aggregateID,seq,version}` and
   optional `metadata,location`; these identify/order events, not their causal
   authority. Even complete replay of these schemas would not prove provenance.

## Optional stronger upstream extension (not an existing API or current gate)

Extend existing durable admission/delivery and execution events, or expose an
equivalent durable receipt through a supported read-only surface. No new scheduler
or global protocol is needed. Two linked guarantees are required:

1. **Runtime-authored input origin.** At admission, persist an immutable origin
   discriminant (`external`, `builtin`, `plugin`, or `control`), authenticated
   producer identity, causal input IDs, and a stable session-local execution/drain
   ID where applicable. Record consumed inputs against that execution. An input
   created before any prompt must have an explicit session-initialization cause,
   not be retroactively assigned to the next prompt by chronology. Built-in Plan
   reminders created during context preparation must carry the actual triggering
   execution/input relation. Plugin identity must come from the runtime's bound
   plugin context, not a string the plugin submits as metadata. HTTP callers cannot
   create or overwrite internal-origin fields; unsupported/unknown causes stay
   explicit. Existing caller metadata remains separate and untrusted.
2. **Closed execution receipt.** Persist session ID, execution ID, root and
   consumed input IDs, terminal outcome, and a durable terminal sequence boundary.
   Success means the covered execution has ended, its causally owned input
   admissions are closed, and no owned tools, background continuations, permission
   requests, or forms remain pending. Include a small explicit effects summary
   sufficient to enforce ocq's current zero-tool policy. If the runtime cannot
   account for a background task or a plugin continuation, report incomplete/
   unsupported, not success. Interleaved external/control input must be identified
   as such, not silently absorbed into a successful prompt receipt.

Persist the relation and receipt transactionally with lifecycle transitions.
Dispatcher disconnects and server restarts must preserve IDs and causal closure;
recovery must not mint a second successful receipt or require input resubmission.
If shutdown, retention, or an older runtime leaves coverage incomplete, readers
must receive an explicit gap/incomplete result. A log watermark by itself must
never advertise recoverable causal coverage. Old history must not gain invented
provenance through backfill.

### Trust boundary and compatibility

The stronger proposal assumes a fresh, exclusively owned session with a pinned
location, exact supplied session/prompt IDs, and a trusted runtime. Other API clients, plugins,
control inputs, and caller metadata must not be able to masquerade as the
runtime's built-in descendants of that prompt. This does not defend against a
malicious server administrator or arbitrary code that can modify its database.

If implementing this optional guarantee, expose a contract version/capability for
durable origins, complete receipt replay, and causal closure, including unsupported effects. Detection must
use that structural capability and receipt schema, not the runtime version,
endpoint existence, title, or matching reminder text. Under that stronger policy,
reject unknown producers, roots, control inputs, missing coverage, and unsupported versions;
retain `unknown` and capacity rather than fall back to idle/history heuristics.
That stronger policy would leave the historical job unknown without authoritative
evidence covering its execution; a future runtime would not be retroactive proof.
The corrected session-scoped policy does not require that proof and has completed
the retained job through normal reconciliation.

### Concrete acceptance flow for the upstream contract

Use one disposable, fresh session and one harmless supplied-ID Plan prompt.
Verify the actual built-in reminder has server-authored origin and a causal chain
to that prompt (or explicit initialization), including admission and consumption.
After disconnecting the dispatcher, read the closed receipt without another POST;
repeat after runtime restart in an isolated fixture and require the same causal
identities and receipt. Verify caller-origin synthetic/control input cannot claim
internal attribution and prevents exclusive-prompt acceptance. A plugin input must
retain its distinct runtime-bound producer, and pending/background effects must
prevent the zero-tool/closed receipt from passing. These are future real-runtime
acceptance criteria, not experiments performed in this investigation.

This proposal requires no current upstream action. It is preserved for a possible
future stronger threat model, not as a prerequisite for the local queue. The
adapter uses the explicit session-scoped policy without guessing provenance.
