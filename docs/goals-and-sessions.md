# Goals and sessions in Herdr

Choose a machine and a project, write a goal, and send it to Main. Open Main to
discuss or redirect the work. Goal sessions appear when Main links an admitted
worker or reviewer to the exact goal and an owned session is recorded. Older
jobs without that link remain stored; OCQ does not infer lineage from their
titles or timestamps. Older bridges can show the generic “Project goal” title.
An existing unsent goal remains available as retained text. The panel neither
submits it on navigation nor retries a send whose outcome is uncertain.

Setup uses one configured choice per role. Open verifies the exact runtime,
project, session and terminal service; it reuses a verified viewer when possible
and never guesses a replacement. Herdr notifications use committed project
result event IDs, only for newly observed transitions in the selected project
after the initial history read. Clicking a notification can Open only the same
exact session in the currently verified project. A stale action never chooses
another target. This is a foreground signal, not a durable notification
subscription. No event is acknowledged by the panel.

## Conversation and deletion

- **Open is the conversation.** Type ordinary updates in OpenCode, including
  while a project worker is running. OCQ verifies the exact original prompt
  first, then treats later native text as context from the configured local
  harness, not evidence of a particular author. The history, agent/model,
  permissions and owned-resource checks still apply. Structured inputs outside
  that bounded worker contract still require attention. Native permissions
  and forms remain human-only. Standalone queue jobs keep their single-input
  contract.
- **A finished job stays finished.** Further native conversation after OCQ
  freezes the first terminal observation is outside that job and its queue
  slot. It does not rewrite its history, reopen capacity accounting, or cover
  new edits with the prior result. New changes need a new reviewed snapshot;
  reviews and worktree verification still refer to exact snapshot digests.
- **Delete** is available for exactly goal-owned workers with proven terminal
  jobs, and the currently selected OCQ-created Main (not adopted legacy
  sessions). OCQ checks the live service, ownership, full transcript, idle
  state, requests, children, and cooperative resources. Stop an active session
  in its native OpenCode tab, then check again. OCQ sends no separate interrupt.
  Busy, unsupported, missing, or unobservable evidence cannot
  authorize deletion.
- Confirmation rechecks the frozen target and preserves a bounded private
  transcript audit before one DELETE. Success requires native 204 and a typed
  `SessionNotFoundError` with the exact session ID on the same service.
  Upstream deletion can interrupt execution and recurse into children; OCQ
  refuses either when observed and the person must leave the session idle
  during confirmation. Ambiguous attempts stay recorded and
  blocked; never repeat them. These are cooperative checks, not a server-atomic
  compare-and-delete. No result, worktree, branch or capacity state is changed.
- Main retirement is recorded against the SQL-selected setup in its existing
  journal. The original binding remains historical and no Main is selected
  after confirmed retirement. Only an explicit **Create Main** may create the
  successor and advance the existing SQL binding with its revision check.

The existing agent-side and core protocols are unchanged. This panel does not
add queue or mail update/delete primitives.

Native contract: the OpenCode 2.3.2 release
(`c0ad18b402eb2827fc1ee71c171b1177b3fc02d1`) defines session remove and
interrupt in `packages/protocol/src/groups/session.ts`; their handlers live in
`packages/server/src/handlers/session.ts`. Recursive removal is in
`packages/core/src/session.ts`. The exact not-found error is defined by
`packages/protocol/src/errors.ts` in both 2.3.0 and 2.3.2.

Historical `proof/herdr-*` runs that instrument the old queue/action controllers
need a matching historical mod tree. They are not interaction checks for this
replacement.

## Standalone goal intake

A matching bridge advertising `work-task-submit-v1` can queue an explicitly
submitted New goal as implementation work, without sending its text to Main.
It uses the owner registry's single implementation selection and the project's
saved binding revision and commit anchor. The bridge accepts bounded text over
the authenticated project connection. The UI stores the request ID and text
digest before submitting and checks the exact Work receipt if the reply is
lost. An incomplete preparation or ambiguous submission does not permit a
second attempt. No session is opened by submission.

The standalone marker binds the Work intent, goal digest, title and base. It
does not replace a queue receipt or change legacy Main goal lineage. Queued
tasks and their exact owned sessions use the existing project session list.
They have no Delete action in that list.

This slice does not deliver a terminal handoff to Main or archive the task.
The existing result-publication inbox mail is not a receipt that the exact
authorized Main consumed and processed an input. A separate handoff operation
must retain that Main identity, the terminal evidence and exact result
associations, enforce one native input attempt, and identify its matching
handled proof without altering the task's first-terminal snapshot. A native
archive operation must separately protect against a now-active or changed
task and read back archive state by exact session ID. Neither stage should be
inferred from a successful queue or result receipt.
