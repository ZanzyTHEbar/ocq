# Working on ocq

- Keep this local-first and portable. Do not embed host paths, credentials,
  private diagnostics, or machine-specific infrastructure in tracked files.
- This is a documentation-only proof-of-fit bootstrap. Do not imply that the
  queue or integration exists. Follow the architecture and reliability contract.
- Evaluate Dagu before building a scheduler. Prefer a thin Go adapter; no Python
  or JavaScript/TypeScript runtime dependency. Use Go + SQLite only after a
  documented proof-of-fit failure justifies it.
- Keep one queue authority. OpenCode owns execution and permissions; Workmux
  owns worktrees. Never modify existing live helpers as part of experiments.
- Make small, scoped changes; inspect existing work first. Never blindly retry
  ambiguous side effects or treat a permission block as successful completion.
- Validate harmless shell lifecycle jobs before live agent execution. Report
  exact checks, results, and gaps; do not invent tests or success claims.
- Keep runtime state, generated artifacts, and binaries untracked; retain useful
  fixtures. Do not choose a license, push, or create a remote without approval.
