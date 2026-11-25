# Product

<!-- impeccable:product-schema 1 -->

## Platform

Native desktop · Herdr

## Users and purpose

Developers use OCQ to give Main a goal, open Main to discuss or redirect it,
and visit the exact conversations Main assigns. Machines lead to projects,
and projects lead to conversations. Selecting a machine or project never
starts or opens a session. Setup uses the machine owner's existing role defaults.

Main owns worker and review orchestration. Queue, agent mail, and audit history
are retained agent and core capabilities, not panel destinations. Meaningful
session transitions use Herdr notifications; there is no panel activity feed.

## Boundaries

SQLite owns queue history, OpenCode owns sessions and permissions, and Herdr
owns tabs. Open uses exact recorded ownership and first checks for an existing
matching tab client. A prepared goal remains unsent until a person explicitly
sends it. No uncertain mutation is automatically retried.

Open is the only conversation action. People send updates in the existing
OpenCode tab; OCQ does not add a chat surface. Delete is a separate confirmed
action for owned idle sessions. Completed job history and reviews remain
historical when a person continues the native conversation.
