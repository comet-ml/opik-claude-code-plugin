# Shared skills are vendored — don't hand-edit them

`skills/opik/` and `skills/agent-ops/` are **vendored from the canonical source**,
`comet-ml/opik-skills` (OPIK-7471). Do not edit them here — edit them in
`opik-skills` and re-sync:

```bash
CANON_REF=<commit-or-tag> bash scripts/sync-shared-skills.sh
```

A CI drift check (`.github/workflows/skills-drift.yml`) fails if these directories
diverge from the pinned canonical ref. This repo still owns everything else:
`commands/`, `agents/`, `hooks/`, and the session logger.
