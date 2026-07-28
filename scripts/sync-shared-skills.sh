#!/usr/bin/env bash
# Vendor the shared Opik skills from the canonical source (OPIK-7471).
#
# Source of truth: comet-ml/opik-mcp (src/opik_mcp/skills). The skills listed in
# SHARED are OWNED THERE — do not hand-edit them in this repo; edit the source
# and re-sync. This repo keeps its own non-shared assets (commands/, agents/,
# hooks/, the logger).
#
# Usage:  CANON_REF=<commit-or-tag> bash scripts/sync-shared-skills.sh
set -euo pipefail

CANON_REPO="${CANON_REPO:-https://github.com/comet-ml/opik-mcp.git}"
CANON_REF="${CANON_REF:-UNPINNED}"   # bump to a canonical commit/tag to actually sync
SRC="src/opik_mcp/skills"            # skills path within opik-mcp
DEST="skills"
SHARED=(opik agent-ops)

if [ "$CANON_REF" = "UNPINNED" ]; then
  echo "CANON_REF is UNPINNED — set it to an opik-mcp ref once the skills have landed"
  echo "there (OPIK-7471, opik-mcp#154). Nothing to sync yet; exiting cleanly."
  exit 0
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
git clone --quiet "$CANON_REPO" "$tmp"
git -C "$tmp" checkout --quiet "$CANON_REF"

for s in "${SHARED[@]}"; do
  if [ ! -d "$tmp/$SRC/$s" ]; then
    echo "skip '$s' — not yet in opik-mcp@$CANON_REF"
    continue
  fi
  rm -rf "${DEST:?}/$s"
  cp -R "$tmp/$SRC/$s" "$DEST/$s"
  echo "synced $s"
done
echo "Done. Vendored ${SHARED[*]} from opik-mcp@$CANON_REF"
