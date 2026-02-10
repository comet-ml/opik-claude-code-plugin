#!/usr/bin/env bash
#
# Opik Logger - Claude Code Hook Handler
#
# Logs Claude Code sessions to Opik for LLM observability.
# Batches span uploads with periodic + final flush for efficiency.
#

set -euo pipefail

command -v jq &>/dev/null || { echo "Error: jq required" >&2; exit 1; }
command -v curl &>/dev/null || { echo "Error: curl required" >&2; exit 1; }

# Read value from ~/.opik.config (INI format)
read_opik_config() {
    local key=$1
    local config="$HOME/.opik.config"
    [[ -f "$config" ]] && grep -E "^${key}\s*=" "$config" 2>/dev/null | head -1 | sed 's/^[^=]*=\s*//' | tr -d ' '
}

# Configuration: env vars > ~/.opik.config > defaults
cfg_url=${OPIK_BASE_URL:-$(read_opik_config "url_override")}
cfg_key=${OPIK_API_KEY:-$(read_opik_config "api_key")}
cfg_workspace=${OPIK_WORKSPACE:-$(read_opik_config "workspace")}
cfg_project=${OPIK_PROJECT:-$(read_opik_config "project_name")}

# Apply defaults where appropriate
readonly OPIK_BASE_URL="${cfg_url:-http://localhost:5173}/api/v1/private"
readonly OPIK_PROJECT="${cfg_project:-claude-code}"
readonly OPIK_API_KEY="${cfg_key:-}"
readonly OPIK_WORKSPACE="${cfg_workspace:-}"
readonly FLUSH_INTERVAL="${OPIK_FLUSH_INTERVAL:-5}"

# Validate: must have either local URL or API key for cloud
if [[ -z "$OPIK_API_KEY" ]] && [[ "$OPIK_BASE_URL" == *"cloud.opik"* || "$OPIK_BASE_URL" == *"comet"* ]]; then
    echo "Error: OPIK_API_KEY required for cloud Opik. Set env var or ~/.opik.config" >&2
    exit 1
fi

readonly INPUT=$(cat)
readonly EVENT=$(jq -r '.hook_event_name // ""' <<< "$INPUT")
readonly SESSION=$(jq -r '.session_id // ""' <<< "$INPUT")
readonly TRANSCRIPT=$(jq -r '.transcript_path // ""' <<< "$INPUT")
readonly STATE_FILE="/tmp/opik-${SESSION}.json"
readonly BUFFER_FILE="/tmp/opik-${SESSION}-spans.jsonl"
readonly LOCK_FILE="/tmp/opik-${SESSION}.lock"

# ─────────────────────────────────────────────────────────────────────────────
# Core Utilities
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

jget() { jq -r "${2} // \"${3:-}\"" <<< "$1"; }
jgetc() { jq -c "${2} // ${3:-{}}" <<< "$1"; }

api() {
    local method=$1 endpoint=$2 data=$3 bg=${4:-false}
    local -a h=(-H "Content-Type: application/json")
    [[ -n "$OPIK_API_KEY" ]] && h+=(-H "authorization: $OPIK_API_KEY")
    [[ -n "$OPIK_WORKSPACE" ]] && h+=(-H "Comet-Workspace: $OPIK_WORKSPACE")

    if [[ $bg == true ]]; then
        (curl -sS -X "$method" "${OPIK_BASE_URL}${endpoint}" "${h[@]}" -d "$data" &>/dev/null) &
        disown 2>/dev/null
    else
        curl -sS -X "$method" "${OPIK_BASE_URL}${endpoint}" "${h[@]}" -d "$data" 2>/dev/null
    fi
}

state() { [[ -f "$STATE_FILE" ]] && cat "$STATE_FILE" || echo ""; }

usage_json() {
    jq -nc --argjson i "${1:-0}" --argjson o "${2:-0}" \
        '{prompt_tokens:$i, completion_tokens:$o, total_tokens:($i+$o)}'
}

# ─────────────────────────────────────────────────────────────────────────────
# Span Building
# ─────────────────────────────────────────────────────────────────────────────

build_span() {
    local id=$1 trace_id=$2 name=$3 type=$4 model=$5 ts=$6
    local input=$7 output=$8 usage=$9 metadata=${10:-{}} parent=${11:-}

    jq -nc \
        --arg id "$id" \
        --arg trace_id "$trace_id" \
        --arg parent_span_id "$parent" \
        --arg name "$name" \
        --arg type "$type" \
        --arg start_time "$ts" \
        --arg end_time "$ts" \
        --arg project_name "$OPIK_PROJECT" \
        --arg model "$model" \
        --arg provider "anthropic" \
        --argjson input "$input" \
        --argjson output "$output" \
        --argjson usage "$usage" \
        --argjson metadata "$metadata" \
        'if $parent_span_id == "" then del(.parent_span_id) else . end | $ARGS.named'
}

# ─────────────────────────────────────────────────────────────────────────────
# Buffer & Flush
# ─────────────────────────────────────────────────────────────────────────────

buffer() { echo "$1" >> "$BUFFER_FILE"; }

should_flush() {
    local s=$(state)
    [[ -z "$s" ]] && return 1
    local last=$(jget "$s" '.last_flush' '0')
    (( $(now_unix) - last >= FLUSH_INTERVAL ))
}

flush() {
    [[ ! -f "$BUFFER_FILE" ]] && return 0
    [[ ! -s "$BUFFER_FILE" ]] && return 0

    # Simple lock with 30s timeout
    if [[ -f "$LOCK_FILE" ]]; then
        (( $(now_unix) - $(cat "$LOCK_FILE") < 30 )) && return 0
    fi
    echo "$(now_unix)" > "$LOCK_FILE"

    local payload=$(jq -nc --argjson spans "$(jq -sc '.' < "$BUFFER_FILE")" '{spans:$spans}')
    local resp=$(api POST "/spans/batch" "$payload") || true

    if [[ -z "$resp" ]] || ! grep -q '"errors"' <<< "$resp"; then
        rm -f "$BUFFER_FILE"
        local s=$(state)
        [[ -n "$s" ]] && jq --argjson t "$(now_unix)" '.last_flush=$t' <<< "$s" > "$STATE_FILE"
    fi

    rm -f "$LOCK_FILE"
}

# ─────────────────────────────────────────────────────────────────────────────
# Transcript Parsing
# ─────────────────────────────────────────────────────────────────────────────

parse_tool_usage() {
    local path=$1 tool_id=$2
    [[ ! -f "$path" ]] && echo '{"model":"unknown","input_tokens":0,"output_tokens":0}' && return

    local result=$(jq -c --arg id "$tool_id" '
        select(.type=="assistant") | select(.message.content[0].id==$id) |
        {model:.message.model, input_tokens:.message.usage.input_tokens,
         output_tokens:.message.usage.output_tokens,
         cache_read:.message.usage.cache_read_input_tokens,
         cache_creation:.message.usage.cache_creation_input_tokens}
    ' < "$path" 2>/dev/null | tail -1)

    echo "${result:-{\"model\":\"unknown\",\"input_tokens\":0,\"output_tokens\":0}}"
}

parse_turn_usage() {
    local path=$1 start=$2
    [[ ! -f "$path" ]] && return

    jq -s --arg start "$start" '
        [.[] | select(.type=="assistant") | select(.message.usage) | select(.timestamp>=$start)] |
        {model:(.[0].message.model//"unknown"),
         input:(map(.message.usage.input_tokens//0)|add),
         output:(map(.message.usage.output_tokens//0)|add),
         cache_read:(map(.message.usage.cache_read_input_tokens//0)|add),
         cache_creation:(map(.message.usage.cache_creation_input_tokens//0)|add),
         calls:length}
    ' < "$path" 2>/dev/null
}

parse_response() {
    local path=$1 start=$2 limit=${3:-10000}
    [[ ! -f "$path" ]] && return

    jq -rs --arg start "$start" '
        [.[] | select(.type=="assistant") | select(.timestamp>=$start) |
         .message.content[]? | select(.type=="text") | .text] | join("\n\n")
    ' < "$path" 2>/dev/null | head -c "$limit"
}

parse_subagent_info() {
    local path=$1 agent_id=$2
    [[ ! -f "$path" ]] && return

    local parent_id=$(grep -o "\"agentId\":\"${agent_id}\".*\"parentToolUseID\":\"[^\"]*\"" "$path" 2>/dev/null \
        | head -1 | grep -o '"parentToolUseID":"[^"]*"' | cut -d'"' -f4)

    [[ -z "$parent_id" ]] && return

    jq -c --arg id "$parent_id" '
        select(.type=="assistant") | select(.message.content[0].id==$id) | .message.content[0].input
    ' < "$path" 2>/dev/null | head -1
}

parse_subagent_transcript() {
    local path=$1
    [[ ! -f "$path" ]] && echo '{"model":"unknown","input":0,"output":0,"tools":[],"response":""}' && return

    jq -sc '
        {
            model: ([.[] | select(.type=="assistant") | .message.model][0] // "unknown"),
            input: ([.[] | select(.type=="assistant") | .message.usage.input_tokens // 0] | add),
            output: ([.[] | select(.type=="assistant") | .message.usage.output_tokens // 0] | add),
            response: ([.[] | select(.type=="assistant") | .message.content[]? | select(.type=="text") | .text] | join("\n\n")),
            tools: (
                ([.[] | select(.type=="user") | select(.message.content[0].type=="tool_result") |
                  {key:.message.content[0].tool_use_id, value:.message.content[0].content}] | from_entries) as $results |
                [.[] | select(.type=="assistant") | select(.message.content[0].type=="tool_use") |
                 {name:.message.content[0].name, input:.message.content[0].input,
                  output:($results[.message.content[0].id]//null), model:.message.model,
                  input_tokens:(.message.usage.input_tokens//0), output_tokens:(.message.usage.output_tokens//0)}]
            )
        }
    ' < "$path" 2>/dev/null || echo '{"model":"unknown","input":0,"output":0,"tools":[],"response":""}'
}

# ─────────────────────────────────────────────────────────────────────────────
# Event Handlers
# ─────────────────────────────────────────────────────────────────────────────

on_prompt() {
    local prompt=$(jget "$INPUT" '.prompt')
    local cwd=$(jget "$INPUT" '.cwd')
    local project=$(basename "$cwd" 2>/dev/null || echo "unknown")
    local trace_id=$(uuid7)
    local ts=$(now_iso)

    jq -n --arg trace_id "$trace_id" --arg start_time "$ts" --arg session_id "$SESSION" \
          --arg transcript_path "$TRANSCRIPT" --arg cwd "$cwd" --arg project "$project" \
          --argjson last_flush "$(now_unix)" '$ARGS.named' > "$STATE_FILE"

    rm -f "$BUFFER_FILE"

    api POST "/traces" "$(jq -nc \
        --arg id "$trace_id" \
        --arg name "claude-code-turn" \
        --arg start_time "$ts" \
        --arg project_name "$OPIK_PROJECT" \
        --arg thread_id "$SESSION" \
        --arg prompt "$prompt" \
        --arg session_id "$SESSION" \
        --arg cwd "$cwd" \
        --arg project "$project" \
        '{id:$id, name:$name, start_time:$start_time, project_name:$project_name,
          thread_id:$thread_id, tags:["claude-code","project:"+$project],
          input:{prompt:$prompt}, metadata:{session_id:$session_id,cwd:$cwd,project:$project}}')" true
}

on_tool() {
    local s=$(state); [[ -z "$s" ]] && return 0

    local trace_id=$(jget "$s" '.trace_id')
    local transcript=$(jget "$s" '.transcript_path')
    local tool=$(jget "$INPUT" '.tool_name' 'unknown')
    local tool_id=$(jget "$INPUT" '.tool_use_id')
    local input=$(jgetc "$INPUT" '.tool_input')
    local output=$(jgetc "$INPUT" '.tool_response')
    local ts=$(now_iso)
    local span_id=$(uuid7)

    local u=$(parse_tool_usage "$transcript" "$tool_id")
    local model=$(jget "$u" '.model' 'unknown')
    local in_tok=$(jget "$u" '.input_tokens' '0')
    local out_tok=$(jget "$u" '.output_tokens' '0')

    local meta=$(jq -nc --arg m "$model" --arg tid "$tool_id" \
        --argjson i "$in_tok" --argjson o "$out_tok" \
        '{model:$m,tool_use_id:$tid,input_tokens:$i,output_tokens:$o}')

    buffer "$(build_span "$span_id" "$trace_id" "$tool" "llm" "$model" "$ts" \
        "$input" "$output" "$(usage_json "$in_tok" "$out_tok")" "$meta")"

    should_flush && flush
}

on_stop() {
    local s=$(state); [[ -z "$s" ]] && return 0

    flush

    local trace_id=$(jget "$s" '.trace_id')
    local transcript=$(jget "$s" '.transcript_path')
    local start=$(jget "$s" '.start_time')
    local ts=$(now_iso)

    local u=$(parse_turn_usage "$transcript" "$start")
    local model=$(jget "$u" '.model' 'unknown')
    local in_tok=$(jget "$u" '.input' '0')
    local out_tok=$(jget "$u" '.output' '0')
    local calls=$(jget "$u" '.calls' '0')

    local response=$(parse_response "$transcript" "$start" 10000)

    api PATCH "/traces/${trace_id}" "$(jq -nc \
        --arg project_name "$OPIK_PROJECT" \
        --arg end_time "$ts" \
        --arg model "$model" \
        --arg response "$response" \
        --argjson in_tok "$in_tok" \
        --argjson out_tok "$out_tok" \
        --argjson calls "$calls" \
        '{project_name:$project_name, end_time:$end_time, model:$model,
          metadata:{model:$model,input_tokens:$in_tok,output_tokens:$out_tok,api_calls:$calls},
          usage:{prompt_tokens:$in_tok,completion_tokens:$out_tok,total_tokens:($in_tok+$out_tok)},
          output:{response:$response}}')" true

    rm -f "$STATE_FILE" "$LOCK_FILE"
}

on_subagent() {
    local s=$(state); [[ -z "$s" ]] && return 0

    local trace_id=$(jget "$s" '.trace_id')
    local transcript=$(jget "$s" '.transcript_path')
    local agent_id=$(jget "$INPUT" '.agent_id')
    local agent_path=$(jget "$INPUT" '.agent_transcript_path')
    local ts=$(now_iso)
    local span_id=$(uuid7)

    # Get subagent info from parent transcript
    local info=$(parse_subagent_info "$transcript" "$agent_id")
    local agent_type=$(jget "$info" '.subagent_type' '')
    local prompt=$(jget "$info" '.prompt' '')
    local desc=$(jget "$info" '.description' '')
    local name="subagent:${agent_type:-$agent_id}"

    # Parse subagent transcript
    local data=$(parse_subagent_transcript "$agent_path")
    local model=$(jget "$data" '.model' 'unknown')
    local in_tok=$(jget "$data" '.input' '0')
    local out_tok=$(jget "$data" '.output' '0')
    local response=$(jget "$data" '.response' '' | head -c 5000)
    local tools=$(jgetc "$data" '.tools' '[]')

    local meta=$(jq -nc --arg aid "$agent_id" --arg atype "$agent_type" --arg m "$model" \
        --argjson i "$in_tok" --argjson o "$out_tok" '$ARGS.named')
    local input=$(jq -nc --arg p "$prompt" --arg d "$desc" '{prompt:$p,description:$d}')
    local output=$(jq -nc --arg r "$response" '{response:$r}')

    buffer "$(build_span "$span_id" "$trace_id" "$name" "llm" "$model" "$ts" \
        "$input" "$output" "$(usage_json "$in_tok" "$out_tok")" "$meta")"

    # Buffer child tool spans
    local n=$(jq 'length' <<< "$tools")
    for ((i=0; i<n; i++)); do
        local t=$(jq -c ".[$i]" <<< "$tools")
        local tname=$(jget "$t" '.name' 'unknown')
        local tinput=$(jgetc "$t" '.input')
        local toutput=$(jq -nc --argjson r "$(jgetc "$t" '.output' 'null')" '{result:$r}')
        local tmodel=$(jget "$t" '.model' 'unknown')
        local ti=$(jget "$t" '.input_tokens' '0')
        local to=$(jget "$t" '.output_tokens' '0')

        buffer "$(build_span "$(uuid7)" "$trace_id" "$tname" "tool" "$tmodel" "$ts" \
            "$tinput" "$toutput" "$(usage_json "$ti" "$to")" "{}" "$span_id")"
    done

    should_flush && flush
}

# ─────────────────────────────────────────────────────────────────────────────
# Main
# ─────────────────────────────────────────────────────────────────────────────

case "$EVENT" in
    UserPromptSubmit) on_prompt ;;
    PostToolUse)      on_tool ;;
    SubagentStop)     on_subagent ;;
    Stop)             on_stop ;;
esac
