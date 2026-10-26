# ocq

A pre-alpha, local-first coding-agent queue adapter proof-of-fit.

This repository currently contains design constraints only: no scheduler,
adapter, CLI, service, or working integration exists yet. Dagu is the first
candidate to evaluate, not an adopted dependency. If it fits, the intended
implementation is a thin Go adapter, without a Python or JavaScript/TypeScript
runtime requirement. A narrow Go + SQLite queue is a fallback only if evidence
shows Dagu cannot meet the contract.

The goal is reusable local orchestration, not another agent runtime. OpenCode
owns execution and permissions; Workmux owns worktrees; exactly one queue
authority owns scheduling. Existing live helpers must remain untouched.

## Start here

- [Architecture and proof-of-fit boundary](docs/architecture.md)
- [Required reliability contract](docs/reliability-contract.md)
- [Contributor instructions](AGENTS.md)

The first proof must use harmless shell jobs to establish Dagu lifecycle behavior
before any live OpenCode execution. The documents describe requirements to
validate, not capabilities already delivered.

Licensing has not been decided. No open-source license is granted by this
bootstrap.
