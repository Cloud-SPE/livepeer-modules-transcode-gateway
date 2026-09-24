# Technical debt

Beads is the sole tracker for debt and deferred work. Run `bd list --label legacy-debt --json` for migrated debt, `bd ready` for available work, and `bd blocked` for dependencies. Create new debt with `bd create --deps discovered-from:<current-id>` and a concrete description.

The [pre-v2 register](completed/pre-v2-debt-history.md) is an immutable historical rationale archive, not a current task list.
