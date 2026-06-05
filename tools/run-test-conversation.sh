#!/bin/bash
# Run the standard test conversation against Claude Code non-interactively.
# Drives a fresh session through prompts that load skills, call MCPs, run
# bash, and spawn a subagent — exercising every code path the metadata
# extractor cares about. The session ID is printed at the end so you can
# load the thread directly in Opik.
#
# Usage:
#   ./tools/run-test-conversation.sh                # generates a fresh UUID
#   ./tools/run-test-conversation.sh <session-uuid> # uses a specific UUID
#   ./tools/run-test-conversation.sh --verify       # generates + runs verify.py
#
# Each `claude -p` call is a complete turn — hooks fire normally
# (UserPromptSubmit → PostToolUse → Stop), so the Opik engine emits the
# same traces it would for an interactive session. Limitation: no easy
# way to reproduce the merge-turns-via-interruption behavior since each
# turn here is atomic.

set -euo pipefail

verify=0
session_id=""
for arg in "$@"; do
  case "$arg" in
    --verify) verify=1 ;;
    --help|-h)
      sed -n '2,18p' "$0" | sed 's/^# \?//'
      exit 0
      ;;
    *) session_id="$arg" ;;
  esac
done

if [[ -z "$session_id" ]]; then
  session_id=$(uuidgen | tr 'A-Z' 'a-z')
fi

echo "== session: $session_id"
echo

run_prompt() {
  local label="$1"
  local prompt="$2"
  local resume_flag="$3" # "" for first turn, "--resume $session_id" otherwise

  echo "--- [$label] -----------------------------------------------"
  echo "❯ $prompt"

  # First turn uses --session-id; subsequent turns use --resume.
  # --dangerously-skip-permissions lets the Skill / MCP tool calls proceed
  # without an interactive permission prompt.
  if [[ -z "$resume_flag" ]]; then
    claude -p --session-id "$session_id" \
      --dangerously-skip-permissions \
      "$prompt"
  else
    claude -p $resume_flag \
      --dangerously-skip-permissions \
      "$prompt"
  fi
  echo
  echo
}

run_prompt "1/7  greeting" \
  "hello this conversation is solely for testing purposes. I am going to tell you to do some things and I want you to do them. None of the results really matter." \
  ""

run_prompt "2/7  load skill" \
  "load the opik FE skill" \
  "--resume $session_id"

run_prompt "3/7  read references" \
  "ok now can you read some of the references for that skill?" \
  "--resume $session_id"

run_prompt "4/7  load second skill" \
  "ok now read the claude-api skill" \
  "--resume $session_id"

run_prompt "5/7  random MCP" \
  "run a random MCP tool. Just pick any one and call it." \
  "--resume $session_id"

run_prompt "6/7  parallel bash" \
  "now I want you to run three parallel bash commands: date, uname -a, pwd" \
  "--resume $session_id"

run_prompt "7/7  subagent" \
  "ok now create a sub agent that runs three bash commands in sequence: whoami, hostname, echo Sequence complete" \
  "--resume $session_id"

echo "== done. session_id=$session_id"
echo
echo "Opik thread URL:"
echo "  https://www.comet.com/opik/comet-all/projects/019c790f-1208-7499-b10b-e7dbf763204c/logs?thread=$session_id&logsType=threads"
echo

if [[ "$verify" == "1" ]]; then
  # Give Opik a beat to ingest the final flushes.
  sleep 3
  SID="$session_id" "$(dirname "$0")/verify.py"
fi
