# Work tracking

Beads is the sole tracker for every change, including small fixes, debt,
acceptance, dependencies, and cross-session handoffs. Read the checked-in
[Beads skill](.agents/skills/beads/SKILL.md), copied with its references and
license from `~/git-repos/personal_brand/beads-skill`.

Start or recover a session with `bd prime`, `bd ready`, and
`bd list --status in_progress --json`. Read the selected issue, claim it with
`bd update <id> --claim`, and record findings with `bd note`. Create described
beads before edits. Large work uses an epic with children and real blocking
edges (`bd dep add <dependent> <blocker>`). Close completed work with a reason
that records validation. `bd blocked` shows what cannot progress.

The gateway uses the `vgw` prefix. The runners repository has its own Beads
workspace; run commands from the repository whose work is being tracked.
The Modules v2 migration is epic `vgw-av1`. Existing debt was migrated as
individual beads labeled `legacy-debt`; live status must be read from Beads.
The old execution-plan documents are immutable historical rationale, not an
active work queue. New design decisions belong in `docs/design-docs/` and
reference their bead IDs, without duplicating task status or checklists.

## Installation and persistence

The local database is initialized under `.beads/`. The project-scoped custom
skill is in `.agents/skills/beads/`; `.codex/config.toml` and
`.codex/hooks.json` load Beads at session start and restore context after
compaction. Restart an already running Codex session to load newly installed
hooks. On a new machine, install the `bd` version recorded by the skill and
run `bd prime` and `bd where`. The installed embedded-mode build reports
`bd doctor` as unsupported; `bd prime`, `bd list --json` and the database
location establish local usability. `bd setup codex --check` compares against
the CLI-bundled skill and reports this intentionally customized skill as
stale; do not overwrite the requested skill merely to clear that warning.

Git contains the instructions, configuration, and design rationale. Issue
state lives in Dolt, not `.beads/issues.jsonl`. Use `bd dolt pull` / `bd dolt push`
for cross-machine issue synchronization when authorized; the configured
remote still needs a successful first push before another clone can recover
this local migration. Do not treat an optional export or a Git commit as
issue synchronization. Never invoke raw Dolt against the live workspace.

The session close contract is printed by `bd prime`. Record validation and
remaining blockers in Beads, inspect `git status`, and report the handoff.
Commits, Git pushes, and Dolt pushes require the authority provided by the
active session; none are implied by completing a bead.
