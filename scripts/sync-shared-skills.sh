#!/usr/bin/env bash
# Vendor the shared Opik skills from their source of truth (OPIK-7471).
#
# The skills under skills/ are OWNED by comet-ml/opik-mcp (src/opik_mcp/skills) and
# published from there as the opik-skills pack. Do not hand-edit them here — edit the
# source, get it merged, then bump CANON_REF and re-run this script. The drift check
# in .github/workflows/skills-drift.yml fails a pull request whose vendored copy
# differs from the pinned source.
#
# Usage:  bash scripts/sync-shared-skills.sh                # at the pinned ref
#         CANON_REF=<commit-or-tag> bash scripts/sync-shared-skills.sh
set -euo pipefail

CANON_REPO="${CANON_REPO:-https://github.com/comet-ml/opik-mcp.git}"
CANON_REF="${CANON_REF:-0baa5aecf0224701473efd968be4aa5a166ba99f}"   # opik-mcp main after #191 (nine skills)
SRC="src/opik_mcp/skills"
DEST="skills"
# Every skill the pack ships. Order matches the published index.
SHARED=(opik opik-compare opik-diagnose opik-evaluate opik-explain opik-instrument opik-online-eval opik-optimize opik-test)

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
git clone --quiet "$CANON_REPO" "$tmp"
git -C "$tmp" checkout --quiet "$CANON_REF"

for s in "${SHARED[@]}"; do
  if [ ! -d "$tmp/$SRC/$s" ]; then
    echo "skip '$s' — not in opik-mcp@$CANON_REF" >&2
    continue
  fi
  rm -rf "${DEST:?}/$s"
  # Same exclusions as the published pack: evals/ is development tooling.
  rsync -a --exclude evals --exclude __pycache__ "$tmp/$SRC/$s/" "$DEST/$s/"
  echo "synced $s"
done
echo "Done. Vendored ${#SHARED[@]} skills from opik-mcp@$CANON_REF"
