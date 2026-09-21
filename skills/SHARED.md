# These skills are vendored — do not hand-edit them

Everything under `skills/` is **vendored from the source of truth**,
[`comet-ml/opik-mcp`](https://github.com/comet-ml/opik-mcp) (`src/opik_mcp/skills`,
OPIK-7471) — the same files published as the `opik-skills` pack. Edit them there, get
the change merged, then bump `CANON_REF` in `scripts/sync-shared-skills.sh` and re-run:

```bash
bash scripts/sync-shared-skills.sh
```

`.github/workflows/skills-drift.yml` fails a pull request whose vendored copy differs
from the pinned source. This repo still owns everything else: `commands/`, `agents/`,
`hooks/`, and the session logger.
