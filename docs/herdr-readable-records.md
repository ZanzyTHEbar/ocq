# Readable records and conversation navigation

Historical record of the previous operator panel. For the current Herdr
surface see [Goals and sessions](goals-and-sessions.md). The record renderers
described below are no longer shipped in the panel.

The panel presents domain fields, not serialized JSON—even under Technical
details or Exact evidence. Original request bytes, immutable receipts, hashes,
file imports and public CLI output remain unchanged.

`readable-record.js` is a presentation-only row/page primitive with an explicit
OCQ field vocabulary. Intake and result adapters retain their stricter
domain/schema checks before enabling confirmation. Unknown representations
show “Cannot display this record type” and supported context, never raw bytes.
Ordinary text remains text; embedded structured reports become labelled rows.
Control and invisible characters receive readable Unicode markers. Evidence
hashes still identify the original bytes, not their presentation.

Local authoring accepts ordinary fields, one-entry-per-line lists and paths to
existing retained request/policy files. File reading does not prepare, admit or
execute anything. The original UTF-8 file contents still pass through the
existing core decoder. Requests with literal newlines in list entries can use
file import rather than a line-list form. Remote intake remains unavailable.

## Renderer inventory

The original graph has 28 modules; the shared readable-record module is new.

| Module | Record presentation |
| --- | --- |
| action-review | Main/role summaries; Technical details uses readable pages |
| agent-handoff | Explicit human-labelled session/location/service fields; no raw-data renderer |
| agent-reconcile | Stored reason uses readable text; identity fields remain labelled |
| agent-submission | Existing request and policy file paths replace JSON editors; review uses command-submission |
| command-dispatcher | Retained dispatcher/HC review uses readable pages |
| command-resolution | Operator evidence and stored reason use readable text |
| command-submission | Arguments use line entries; retained request fields decode embedded records |
| compatibility | No record dump; canonical action envelopes remain internal |
| compose-controller | Message/context preview fields use readable values; original request hash retained |
| compose-form | Ordinary fields and retained IDs; lane-default display uses compose adapter |
| hc-binding | Human context summary; readable identity pages; navigation consent cannot authorize dispatcher actions |
| intake-controller | Domain-validated registration/submission sections; embedded byte hashes retained without byte pages; file import |
| main-client | Main/client Technical details uses readable pages; navigation is separate from execution state |
| main | Keyboard/project/job/manifest/mail/result/lane inspection uses readable rows; no original-manifest/raw-intake toggle |
| panel-state | No record renderer; shared visibility/context markers only |
| private-frames | No renderer; exact UTF-8 decoding and explicit local-file reading |
| push-connection | No record renderer; wire frames remain internal |
| queue | Job/event Fields and structured output use readable text; binary output has no raw-byte UI fallback |
| queued-cancel | Explicit labelled cancellation scope/effects; no raw-data renderer |
| registration | Ordinary guided fields; generated registration preview uses intake adapter |
| result-publication | Domain-validated result/review fields, findings, limitations and evidence identities; no raw toggle; file import and exclusion lines |
| runtime-project | Stored job/manifest navigation delegates to readable work pages |
| runtime-sources | Queue/Inbox/record Technical details and structured message bodies use readable records |
| stored-updates | No renderer; typed projections and identity comparisons remain internal |
| submission | Ordinary fields, line lists and existing policy-file path; core still builds canonical submission |
| work-operator | Status/receipt/output/manifest/handoff/Exact evidence use readable pages; final structured answer uses readable values |
| workflow | Main summaries and readable Technical details; no profile JSON editor |
| worktree-check | Explicit labelled filesystem snapshot/drift fields; no raw-data renderer |

## Open in your existing OpenCode tab

Open Main or Open worker routes directly to a matching
[opted-in OpenCode TUI](herdr.md#opt-in-to-exact-navigation-in-your-normal-opencode-tui).
When several clients match, the person chooses an existing tab or pane in Herdr.
The panel neither uses HC launch or keep nor selects a terminal by name or
receipt. The request validates and routes only in the selected TUI.
It never creates a session, sends a prompt, retries a worker, answers a
permission/form or alters queue capacity. Historical inspection-client receipts
are not migrated, retried or treated as proof of the current route.

## Worker bases and native skills

`project work prepare --base-ref REF_OR_COMMIT` resolves a locally available Git
commit once before retaining the work intent. The frozen full commit is used
for the payload and brief. Omitting the option preserves the binding anchor.
Main stays on its canonical branch. Review inherits the exact captured
implementation result base, not a newly resolved ref tip.

The candidate completion policy accounts for completed native `skill` loading
only on source-validated OpenCode 2.3.0/2.3.2. Loading instructions does not grant
permission to run their scripts or delegate work. `webfetch`, Code Mode
`execute`, plugins and delegation remain unsupported resource boundaries;
unknown ownership continues to hold capacity. Static review's read/glob/grep
plus catch-all deny policy is unchanged.
