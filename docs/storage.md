# Storage and runtime contract

OCQ runs on Linux, including Linux VMs and containers. Queue storage is a separate
choice from the runtime: an ephemeral container or VM can use a retained durable
volume. OCQ does not select storage by filesystem name and does not have an unsafe
mode or alternate queue authority.

## Required storage behavior

- Keep the entire private state directory on storage retained for the lifetime of
  receipts, outputs, journals and unresolved execution ownership. Directories must
  be owned by the running UID, nonsymlink mode 0700, and files owned mode 0600.
  Container UID mappings must preserve these checks, not bypass them.
- All processes accessing one live queue must share one Linux host/kernel locking
  and memory-coherence domain, with coherent SQLite byte-range locks, mmap-backed
  WAL shared memory, and advisory `flock` locks. One queue has one dispatcher;
  concurrent submitters and readers still use the same SQLite authority.
- The backing stack must honor file and directory `fsync`, durable creation/rename
  and SQLite's write ordering. This includes the guest driver, volume/export
  configuration, host filesystem, caches and hardware. `fsync` returning success
  is necessary, not proof against storage loss or a lying implementation.
- Writable connections retain WAL, FULL synchronous mode, foreign keys and a
  5000 ms busy timeout. Receipt acceptance and terminal events stay transactional;
  launch intent and single-attempt journals precede external effects. There is no
  rollback-journal, exclusive-locking or memory-only fallback.

Normal operations enforce ownership/modes and check effective SQLite settings;
the actual lock, file/directory sync and database operations must succeed. Errors
remain errors, with the failed operation/SQLite diagnostic retained. An open is
not a durability certificate, a cross-process lock certification or an execution
readiness test. No destructive probe runs on every open. Existing writable opens
do not initialize or migrate; Reader opens remain non-initializing and read-only
(`mode=ro`, `query_only`), without write/sync/lock probes. SQLite may require or
create WAL auxiliary files under its normal read-only WAL rules; Reader does not
mean immutable media or a byte-for-byte unchanged directory.

SQLite [requires same-host shared memory for WAL][wal]. Do not open one live queue
from multiple hosts or guests, or concurrently from a VM guest and its host.
Network filesystems are not supported for live WAL state; a volume named `local`
or `persistent` is not proof of appropriate semantics. Never unlink lock files,
retarget a mounted queue or restore/replace its database while clients are using
it. Preserve unknown ownership: losing storage or a runtime is not permission to
resubmit jobs or declare their effects complete.

## VM and container setup

Use `--state-dir` or an absolute `XDG_STATE_HOME` on a retained volume. Mount the
whole state directory, not just `queue.db`; WAL, shared memory, locks, journals and
outputs belong together. Keep stable paths for workspaces and retained inputs too.
Use SQLite's online backup API for a live database, not a raw copy of `queue.db`.
State is private application data, not files to commit in a shared repository.

- **VM block disks:** retained virtual disks are suitable when the backing stack
  meets the contract. ext4, XFS or Btrfs labels alone do not establish persistence.
- **virtiofs:** suitable exports can meet the contract for access solely within
  one guest. Linux documents [persistent storage for ephemeral guests][virtiofs]
  as a use case. Verify the actual export's locks, shared memory, sync and backing
  retention; do not treat all virtiofs mounts as either durable or unsupported.
- **Containers:** use a retained bind mount or named volume with suitable backing
  storage. Docker [documents volume lifetime independently of containers][volumes];
  volume drivers may nevertheless use network storage. Do not share a live queue
  across hosts/VMs. Keep dispatcher and execution in one stable PID/boot identity
  domain; separate container PID namespaces are not interchangeable owners.
- **Overlay/writable layers:** not rejected merely by type, but a layer discarded
  with its container loses the queue. Use a retained volume for durable operation.
- **tmpfs/ephemeral disks:** operations may work, but state disappears at reboot,
  unmount or runtime deletion. Use only for explicitly disposable experiments
  whose receipts, outputs and recovery obligations need not survive that boundary;
  do not put production or externally consequential work there. FULL sync does not
  make volatile memory persistent, and OCQ cannot infer the intended retention.

Execution requirements remain independent of storage. Commands require Linux
process execution, `/proc` process/boot/memory observations and supported foreground
process lifecycle semantics. The memory floor is admission, not cgroup enforcement.
Agent features additionally require the exact supported OpenCode API/policy,
Workmux/Git, stable worktrees, dedicated sessions and tool/resource observations
described in [agent integration](agent-integration.md). Missing permissions,
resources or ownership observations still fail closed; no container/VM exception
auto-approves permissions, retries effects or releases unknown capacity.

## Managed Durable Objects are a different execution target

Cloudflare's [SQLite-backed Durable Objects][do-sql] provide strongly consistent,
transactional per-object storage through `ctx.storage.sql`, not a mountable
`queue.db` for this Go SQLite driver. Their SQL API disallows `BEGIN`/`SAVEPOINT`
in `sql.exec`, requiring runtime transaction APIs instead. This is not a drop-in
replacement for OCQ's connection PRAGMAs, IMMEDIATE transactions, file journals
and `flock` ownership protocol. Workers' [Node compatibility][workers-node] lists
`child_process` as a non-functional stub; a managed object is not the Linux
process/Workmux/OpenCode execution environment this adapter requires.

Direct Durable Object deployment is therefore **not implemented**. Supporting it
would require explicit transaction/migration and durable-intent adaptations plus
an execution/observation boundary preserving exact ownership, permission handling,
FIFO, receipt idempotency, uncertain effects and terminal-event atomicity. It
cannot mean adding a second authority, automatically replaying an alarm after an
ambiguous execution, or calling managed SQLite durable while losing execution
evidence. OCQ supplies no backend or service for that target.

## Acceptance on a chosen volume

Build serially following [development instructions](command-core.md#development).
Set `XDG_STATE_HOME` to a private retained directory on the target volume and run:

```sh
XDG_STATE_HOME="$VOLUME_ACCEPTANCE_DIR" GOMAXPROCS=2 runtime/ocq-acceptance --storage "$PWD/runtime/ocq"
```

This uses fresh isolated state and harmless foreground probes, not an existing
queue or live agent. It exercises real CLI concurrency, WAL, singleton dispatch,
receipt reopen/recovery, non-initializing reader/writer opens and fail-closed file errors.
It retains evidence in the selected directory. It does not test power loss,
hardware caches, cross-host access or full agent deployment. Run separately for
each actual mount/configuration; a pass on one export does not certify another.

[wal]: https://www.sqlite.org/wal.html
[virtiofs]: https://www.kernel.org/doc/html/latest/filesystems/virtiofs.html
[volumes]: https://docs.docker.com/engine/storage/volumes/
[do-sql]: https://developers.cloudflare.com/durable-objects/api/sqlite-storage-api/
[workers-node]: https://developers.cloudflare.com/workers/runtime-apis/nodejs/
