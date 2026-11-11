# Reliability contract

These rules describe OCQ's acceptance, ownership and recovery behavior. They do
not promise sandboxing, validated deliverables or exactly-once external effects.

## Acceptance and ordering

- Acknowledgement follows durable receipt/job acceptance in SQLite.
- Idempotency keys bind immutable requests. Matching retries recover the original
  acceptance; changed requests under the same key fail. Receipts are retained;
  there is no deletion command.
- Admission is strict FIFO. A resource-blocked head job blocks later work, even
  when later work would fit. Concurrent completion order is unspecified.
- Memory declarations and concurrency slots control admission, not resource usage.

## Ownership

- Launch intent is durable before a process or external mutation starts. SQLite
  cannot atomically spawn a process or commit an OpenCode request.
- Unresolved launching/running work becomes unknown after dispatcher restart.
  Unknown work keeps capacity and is not automatically retried.
- Exact process, session, worktree and prompt identities are used for recovery.
  Names, a closed client or an inactive UI are not ownership evidence.
- Only queued work can be cancelled. A cancellation request is not evidence that
  already-running tools stopped.
- OpenCode owns agent permissions. OCQ does not answer them or treat a pending
  request as successful completion.

## Outcomes and evidence

- Output availability, execution outcome and validation are separate. Missing
  output is an error, not an empty successful log. Exit zero and
  `agent_succeeded` do not establish validated changes.
- Agent completion requires the [dedicated-session and tool/resource checks](agent-integration.md#completion).
  Unsupported or missing resource observations retain unknown ownership.
- Stored views do not reconcile execution. Read timestamps identify database
  reads, not the freshness of every fact displayed. Herdr request reasons have no
  observation timestamp and do not distinguish permission from form.
- Imported result claims remain claims. Cooperative capture verifies a snapshot,
  not test execution or ignored/external inputs. Review approval does not authorize
  Git integration.

## Recovery and delivery

- Inspect uncertain effects before explicit reconciliation; never infer permission
  to retry from a missing response or receipt.
- Command resolution requires stopped dispatch, exact liveness checks and recorded
  evidence. It records an operator assertion and may release eligible unknown
  capacity, not certify execution success or test validation. The panel's local
  resolution requires retained review/confirmation and exact evidence/job/event
  read-back; uncertain attempts are never automatically retried.
  Agent reconciliation is lock-exclusive and API-only; it never resumes,
  prompts, replies to permissions or cancels tools.
- Completion events commit atomically with terminal transitions. They are frozen
  first-terminal observations and remain available after acknowledgement.
- Pull/ack never releases capacity or retries execution. Consumers must deduplicate
  external handling by stable event ID; OCQ cannot make consumer effects exactly once.

Storage must honor the [single-host locking, WAL/shared-memory, sync and retention
contract](storage.md). VM/container lifetime and volume lifetime are independent;
successful syscalls or filesystem names do not prove persistence. Ephemeral state
cannot retain receipts or unknown ownership beyond its lifetime. No power-loss,
hostile-process containment or unattended-agent deployment guarantee is implied.
