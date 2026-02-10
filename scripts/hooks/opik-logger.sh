#!/usr/bin/env bash
#
# Opik Logger - Claude Code Hook Handler
#
# Logs Claude Code sessions to Opik for LLM observability.
# Each conversation turn becomes a trace; each tool call becomes a span.
#
# Configuration (env vars > ~/.opik.config > defaults):
#   OPIK_BASE_URL              - Opik API base URL (required)
#   OPIK_API_KEY               - API key for Opik Cloud
#   OPIK_WORKSPACE             - Workspace for Opik Cloud
#   OPIK_PROJECT               - Project name (default: claude-code)
#   OPIK_CC_DEBUG              - Enable debug logging (default: false)
#   OPIK_CC_TRUNCATE_CONTENT   - Truncate long content (default: true)
#   OPIK_CC_FLUSH_INTERVAL     - Seconds between flushes (default: 5)
#

set -uo pipefail

# ─────────────────────────────────────────────────────────────────────────────
# Configuration
# ─────────────────────────────────────────────────────────────────────────────

read_config() {
    local key=$1 config="$HOME/.opik.config"
    [[ -f "$config" ]] && grep -E "^${key}\s*=" "$config" 2>/dev/null | head -1 | sed 's/^[^=]*=\s*//' | tr -d ' '
}

# Load config: env vars take precedence over ~/.opik.config
_base_url="${OPIK_BASE_URL:-$(read_config url_override)}"
_base_url="${_base_url%/}"  # Strip trailing slash

[[ -z "$_base_url" ]] && { echo "Error: OPIK_BASE_URL or url_override in ~/.opik.config required" >&2; exit 1; }
declare -r OPIK_URL="${_base_url}/v1/private"
_project="${OPIK_PROJECT:-$(read_config project_name)}"
declare -r OPIK_PROJECT="${_project:-claude-code}"
declare -r OPIK_API_KEY="${OPIK_API_KEY:-$(read_config api_key)}"
declare -r OPIK_WORKSPACE="${OPIK_WORKSPACE:-$(read_config workspace)}"

declare -r DEBUG="${OPIK_CC_DEBUG:-false}"
declare -r TRUNCATE="${OPIK_CC_TRUNCATE_CONTENT:-true}"
declare -r FLUSH_INTERVAL="${OPIK_CC_FLUSH_INTERVAL:-5}"

# Validate cloud config
if [[ -z "$OPIK_API_KEY" && "$OPIK_URL" =~ (cloud\.opik|comet) ]]; then
    echo "Error: OPIK_API_KEY required for Opik Cloud" >&2
    exit 1
fi

# ─────────────────────────────────────────────────────────────────────────────
# Debug & Dependencies
# ─────────────────────────────────────────────────────────────────────────────

declare -r DEBUG_LOG="/tmp/opik-hook-debug.log"
debug() { [[ "$DEBUG" == "true" ]] && echo "[$(date '+%H:%M:%S')] $*" >> "$DEBUG_LOG"; }

command -v jq &>/dev/null || { echo "Error: jq required" >&2; exit 1; }
command -v curl &>/dev/null || { echo "Error: curl required" >&2; exit 1; }

# ─────────────────────────────────────────────────────────────────────────────
# Parse Hook Input
# ─────────────────────────────────────────────────────────────────────────────

declare -r INPUT=$(cat)
declare -r EVENT=$(jq -r '.hook_event_name // ""' <<< "$INPUT")
declare -r SESSION=$(jq -r '.session_id // ""' <<< "$INPUT")
declare -r TRANSCRIPT=$(jq -r '.transcript_path // ""' <<< "$INPUT")

declare -r STATE_FILE="/tmp/opik-${SESSION}.json"
declare -r BUFFER_FILE="/tmp/opik-${SESSION}-spans.jsonl"
declare -r LOCK_FILE="/tmp/opik-${SESSION}.lock"

debug "=== $EVENT ==="

# ─────────────────────────────────────────────────────────────────────────────
# Utilities
# ─────────────────────────────────────────────────────────────────────────────

uuid7() {
    local ts=$(($(date +%s) * 1000 + RANDOM % 1000))
    local hex=$(printf '%012x' "$ts")
    local rand=$(od -An -tx1 -N10 /dev/urandom | tr -d ' \n')
    local var=$(printf '%02x' $(( 0x${rand:3:2} & 0x3F | 0x80 )))
    echo "${hex:0:8}-${hex:8:4}-7${rand:0:3}-${var}${rand:5:2}-${rand:7:12}"
}

now_iso() { date -u +"%Y-%m-%dT%H:%M:%SZ"; }
now_unix() { date +%s; }

jval() { jq -r "${2} // \"${3:-}\"" <<< "$1"; }
jobj() { jq -c "${2} // ${3:-null}" <<< "$1"; }

json_usage() {
    jq -nc --argjson i "${1:-0}" --argjson o "${2:-0}" \
        '{prompt_tokens:$i, completion_tokens:$o, total_tokens:($i+$o)}'
}

truncate() {
    local json="$1"
    [[ "$TRUNCATE" != "true" ]] && { echo "$json"; return; }
    jq -c 'walk(if type == "string" and length > 100 then
        "[TRUNCATED - set OPIK_CC_TRUNCATE_CONTENT=false]"
    else . end)' <<< "$json" 2>/dev/null || echo "$json"
}

state_read() { [[ -f "$STATE_FILE" ]] && cat "$STATE_FILE" || echo ""; }

# ─────────────────────────────────────────────────────────────────────────────
# API Client
# ─────────────────────────────────────────────────────────────────────────────

api() {
    local method=$1 endpoint=$2 data=$3 async=${4:-false}
    local -a headers=(-H "Content-Type: application/json")
    [[ -n "$OPIK_API_KEY" ]] && headers+=(-H "authorization: $OPIK_API_KEY")
    [[ -n "$OPIK_WORKSPACE" ]] && headers+=(-H "Comet-Workspace: $OPIK_WORKSPACE")

    if [[ "$async" == "true" ]]; then
        (curl -sS -X "$method" "${OPIK_URL}${endpoint}" "${headers[@]}" -d "$data" &>/dev/null) &
        disown 2>/dev/null
    else
        curl -sS -X "$method" "${OPIK_URL}${endpoint}" "${headers[@]}" -d "$data" 2>/dev/null
    fi
}

# ─────────────────────────────────────────────────────────────────────────────
# Buffer & Flush
# ─────────────────────────────────────────────────────────────────────────────

buffer() { [[ -n "$1" && "$1" != "jq:"* ]] && echo "$1" >> "$BUFFER_FILE"; }

should_flush() {
    local s=$(state_read)
    [[ -z "$s" ]] && return 1
    (( $(now_unix) - $(jval "$s" '.last_flush' '0') >= FLUSH_INTERVAL ))
}

flush() {
    [[ ! -s "$BUFFER_FILE" ]] && return 0
    [[ -f "$LOCK_FILE" ]] && (( $(now_unix) - $(cat "$LOCK_FILE") < 30 )) && return 0

    echo "$(now_unix)" > "$LOCK_FILE"
    local payload=$(jq -nc --slurpfile spans "$BUFFER_FILE" '{spans:$spans}')
    local resp=$(api POST "/spans/batch" "$payload")

    [[ -z "$resp" || ! "$resp" =~ \"errors\" ]] && {
        rm -f "$BUFFER_FILE"
        local s=$(state_read)
        [[ -n "$s" ]] && jq --argjson t "$(now_unix)" '.last_flush=$t' <<< "$s" > "$STATE_FILE"
    }
    rm -f "$LOCK_FILE"
}

# ─────────────────────────────────────────────────────────────────────────────
# Span Builder
# ─────────────────────────────────────────────────────────────────────────────

span() {
    local id=$1 trace_id=$2 name=$3 type=$4 model=$5 ts=$6
    local input=$7 output=$8 usage=$9
    local metadata="${10}"
    local parent="${11:-}"
    [[ -z "$metadata" || "$metadata" == "null" ]] && metadata='{}'

    jq -nc \
        --arg id "$id" --arg trace_id "$trace_id" --arg parent_span_id "$parent" \
        --arg name "$name" --arg type "$type" \
        --arg start_time "$ts" --arg end_time "$ts" \
        --arg project_name "$OPIK_PROJECT" --arg model "$model" --arg provider "anthropic" \
        --argjson input "$input" --argjson output "$output" \
        --argjson usage "$usage" --argjson metadata "$metadata" \
        '$ARGS.named | if .parent_span_id == "" then del(.parent_span_id) else . end'
}

# ─────────────────────────────────────────────────────────────────────────────
# Transcript Parsers
# ─────────────────────────────────────────────────────────────────────────────

parse_tool_usage() {
    local path=$1 tool_id=$2
    [[ ! -f "$path" ]] && { echo '{}'; return; }
    jq -c --arg id "$tool_id" '
        select(.type=="assistant") | select(.message.content[0].id==$id) |
        {model:.message.model, input_tokens:.message.usage.input_tokens, output_tokens:.message.usage.output_tokens}
    ' < "$path" 2>/dev/null | tail -1 || echo '{}'
}

parse_turn_usage() {
    local path=$1 start=$2
    [[ ! -f "$path" ]] && { echo '{}'; return; }
    jq -s --arg start "$start" '
        [.[] | select(.type=="assistant") | select(.message.usage) | select(.timestamp>=$start)] |
        {model:(.[0].message.model//"unknown"), input:(map(.message.usage.input_tokens//0)|add),
         output:(map(.message.usage.output_tokens//0)|add), calls:length}
    ' < "$path" 2>/dev/null || echo '{}'
}

parse_response() {
    local path=$1 start=$2
    [[ ! -f "$path" ]] && return
    jq -rs --arg start "$start" '
        [.[] | select(.type=="assistant") | select(.timestamp>=$start) |
         .message.content[]? | select(.type=="text") | .text] | join("\n\n")
    ' < "$path" 2>/dev/null | head -c 10000
}

parse_subagent_info() {
    local path=$1 agent_id=$2
    [[ ! -f "$path" ]] && return
    local pid=$(grep -o "\"agentId\":\"${agent_id}\".*\"parentToolUseID\":\"[^\"]*\"" "$path" 2>/dev/null \
        | head -1 | grep -o '"parentToolUseID":"[^"]*"' | cut -d'"' -f4)
    [[ -z "$pid" ]] && return
    jq -c --arg id "$pid" 'select(.type=="assistant") | select(.message.content[0].id==$id) | .message.content[0].input' < "$path" 2>/dev/null | head -1
}

parse_subagent_transcript() {
    local path=$1
    [[ ! -f "$path" ]] && { echo '{"model":"unknown","input":0,"output":0,"tools":[],"response":""}'; return; }
    jq -sc '
        {
            model: ([.[] | select(.type=="assistant") | .message.model][0] // "unknown"),
            input: ([.[] | select(.type=="assistant") | .message.usage.input_tokens // 0] | add),
            output: ([.[] | select(.type=="assistant") | .message.usage.output_tokens // 0] | add),
            response: ([.[] | select(.type=="assistant") | .message.content[]? | select(.type=="text") | .text] | join("\n\n")),
            tools: (
                ([.[] | select(.type=="user") | select(.message.content | type=="array") |
                  select(.message.content[0].type?=="tool_result") |
                  {key:.message.content[0].tool_use_id, value:.message.content[0].content}] | from_entries) as $r |
                [.[] | select(.type=="assistant") | select(.message.content | type=="array") |
                 select(.message.content[0].type?=="tool_use") |
                 {name:.message.content[0].name, input:.message.content[0].input, output:($r[.message.content[0].id]//null),
                  model:.message.model, input_tokens:(.message.usage.input_tokens//0), output_tokens:(.message.usage.output_tokens//0)}]
            )
        }
    ' < "$path" 2>/dev/null || echo '{"model":"unknown","input":0,"output":0,"tools":[],"response":""}'
}

# ─────────────────────────────────────────────────────────────────────────────
# Event Handlers
# ─────────────────────────────────────────────────────────────────────────────

on_prompt() {
    local prompt=$(jval "$INPUT" '.prompt')
    local cwd=$(jval "$INPUT" '.cwd')
    local project=$(basename "$cwd" 2>/dev/null || echo "unknown")
    local trace_id=$(uuid7) ts=$(now_iso)

    jq -n --arg trace_id "$trace_id" --arg start_time "$ts" --arg session_id "$SESSION" \
          --arg transcript_path "$TRANSCRIPT" --arg cwd "$cwd" --arg project "$project" \
          --argjson last_flush "$(now_unix)" '$ARGS.named' > "$STATE_FILE"
    rm -f "$BUFFER_FILE"

    api POST "/traces" "$(jq -nc \
        --arg id "$trace_id" --arg name "claude-code-turn" --arg start_time "$ts" \
        --arg project_name "$OPIK_PROJECT" --arg thread_id "$SESSION" \
        --arg prompt "$prompt" --arg cwd "$cwd" --arg project "$project" \
        '{id:$id, name:$name, start_time:$start_time, project_name:$project_name,
          thread_id:$thread_id, tags:["claude-code","project:"+$project],
          input:{prompt:$prompt}, metadata:{cwd:$cwd,project:$project}}')" true
}

on_tool() {
    local s=$(state_read); [[ -z "$s" ]] && return 0

    local trace_id=$(jval "$s" '.trace_id')
    local tool=$(jval "$INPUT" '.tool_name' 'unknown')
    local tool_id=$(jval "$INPUT" '.tool_use_id')
    local input=$(truncate "$(jobj "$INPUT" '.tool_input')")
    local output=$(truncate "$(jobj "$INPUT" '.tool_response')")
    local span_id=$(uuid7) ts=$(now_iso)

    local u=$(parse_tool_usage "$(jval "$s" '.transcript_path')" "$tool_id")
    local model=$(jval "$u" '.model' 'unknown')
    local itok=$(jval "$u" '.input_tokens' '0')
    local otok=$(jval "$u" '.output_tokens' '0')

    local meta=$(jq -nc --arg model "$model" --arg tool_use_id "$tool_id" \
        --argjson input_tokens "${itok:-0}" --argjson output_tokens "${otok:-0}" '$ARGS.named')

    buffer "$(span "$span_id" "$trace_id" "$tool" "tool" "$model" "$ts" \
        "$input" "$output" "$(json_usage "${itok:-0}" "${otok:-0}")" "$meta")"
    should_flush && flush
}

on_stop() {
    local s=$(state_read); [[ -z "$s" ]] && return 0
    flush

    local trace_id=$(jval "$s" '.trace_id')
    local transcript=$(jval "$s" '.transcript_path')
    local start=$(jval "$s" '.start_time')
    local ts=$(now_iso)

    local u=$(parse_turn_usage "$transcript" "$start")
    local model=$(jval "$u" '.model' 'unknown')
    local itok=$(jval "$u" '.input' '0')
    local otok=$(jval "$u" '.output' '0')
    local calls=$(jval "$u" '.calls' '0')
    local response=$(parse_response "$transcript" "$start")

    api PATCH "/traces/${trace_id}" "$(jq -nc \
        --arg project_name "$OPIK_PROJECT" --arg end_time "$ts" --arg model "$model" --arg response "$response" \
        --argjson itok "$itok" --argjson otok "$otok" --argjson calls "$calls" \
        '{project_name:$project_name, end_time:$end_time, model:$model,
          metadata:{model:$model, input_tokens:$itok, output_tokens:$otok, api_calls:$calls},
          usage:{prompt_tokens:$itok, completion_tokens:$otok, total_tokens:($itok+$otok)},
          output:{response:$response}}')" true

    rm -f "$STATE_FILE" "$LOCK_FILE"
}

on_subagent() {
    local s=$(state_read); [[ -z "$s" ]] && return 0

    local trace_id=$(jval "$s" '.trace_id')
    local agent_id=$(jval "$INPUT" '.agent_id')
    local agent_path=$(jval "$INPUT" '.agent_transcript_path')
    local span_id=$(uuid7) ts=$(now_iso)

    local info=$(parse_subagent_info "$(jval "$s" '.transcript_path')" "$agent_id")
    local atype=$(jval "$info" '.subagent_type' '')
    local prompt=$(jval "$info" '.prompt' '')
    local desc=$(jval "$info" '.description' '')
    local name="subagent:${atype:-$agent_id}"

    local data=$(parse_subagent_transcript "$agent_path")
    local model=$(jval "$data" '.model' 'unknown')
    local itok=$(jval "$data" '.input' '0')
    local otok=$(jval "$data" '.output' '0')
    local response=$(jval "$data" '.response' '' | head -c 5000)
    local tools=$(jq -c '.tools // []' <<< "$data" 2>/dev/null || echo '[]')

    local meta=$(jq -nc --arg agent_id "$agent_id" --arg agent_type "$atype" --arg model "$model" \
        --argjson input_tokens "${itok:-0}" --argjson output_tokens "${otok:-0}" '$ARGS.named')
    local input=$(jq -nc --arg prompt "$prompt" --arg description "$desc" '$ARGS.named')
    local output=$(jq -nc --arg response "$response" '$ARGS.named')

    buffer "$(span "$span_id" "$trace_id" "$name" "llm" "$model" "$ts" \
        "$input" "$output" "$(json_usage "${itok:-0}" "${otok:-0}")" "$meta")"

    local n=$(jq 'length' <<< "$tools" 2>/dev/null || echo 0)
    for ((i=0; i<n; i++)); do
        local t=$(jq -c ".[$i]" <<< "$tools")
        local tname=$(jval "$t" '.name' 'unknown')
        local tinput=$(truncate "$(jq -c '.input // {}' <<< "$t")")
        local toutraw=$(jq -c '.output // null' <<< "$t")
        local toutput=$(truncate "$(jq -nc --argjson r "$toutraw" '{result:$r}' 2>/dev/null || echo '{"result":null}')")
        local tmodel=$(jval "$t" '.model' 'unknown')
        local ti=$(jval "$t" '.input_tokens' '0')
        local to=$(jval "$t" '.output_tokens' '0')

        buffer "$(span "$(uuid7)" "$trace_id" "$tname" "tool" "$tmodel" "$ts" \
            "$tinput" "$toutput" "$(json_usage "${ti:-0}" "${to:-0}")" "{}" "$span_id")"
    done

    should_flush && flush
}

on_compact() {
    local s=$(state_read); [[ -z "$s" ]] && return 0

    local trace_id=$(jval "$s" '.trace_id')
    local trigger=$(jval "$INPUT" '.trigger' 'auto')
    local span_id=$(uuid7) ts=$(now_iso)

    local input=$(jq -nc --arg trigger "$trigger" '{trigger:$trigger}')
    local output=$(jq -nc '{status:"context_compacted"}')

    buffer "$(span "$span_id" "$trace_id" "context:compaction" "general" "n/a" "$ts" \
        "$input" "$output" "$(json_usage 0 0)" "{}")"
    flush
}

# ─────────────────────────────────────────────────────────────────────────────
# Main
# ─────────────────────────────────────────────────────────────────────────────

case "$EVENT" in
    UserPromptSubmit) on_prompt ;;
    PostToolUse)      on_tool ;;
    SubagentStop)     on_subagent ;;
    PreCompact)       on_compact ;;
    Stop)             on_stop ;;
esac
