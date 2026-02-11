#!/usr/bin/env bash
#
# opik-logger.sh — Claude Code → Opik observability bridge
#
# Captures Claude Code sessions as Opik traces with spans for thinking,
# text responses, tool calls, and subagents. Uses idempotent upserts
# via deterministic UUIDs derived from transcript entries.
#
# Configuration (env vars override ~/.opik.config):
#   OPIK_BASE_URL          — Opik API base URL (required)
#   OPIK_PROJECT           — Project name (default: claude-code)
#   OPIK_API_KEY           — API key for Opik Cloud
#   OPIK_WORKSPACE         — Workspace for Opik Cloud
#   OPIK_CC_DEBUG          — Enable debug logging (default: false)
#   OPIK_CC_TRUNCATE_FIELDS — Mask long fields in Edit/Read/Write (default: true)
#
set -u

# ─────────────────────────────────────────────────────────────────────────────
# Config
# ─────────────────────────────────────────────────────────────────────────────

cfg() { [[ -f ~/.opik.config ]] && sed -n "s/^$1 *= *//p" ~/.opik.config | head -1 | tr -d ' '; }

URL="${OPIK_BASE_URL:-$(cfg url_override)}"
[[ -z "$URL" ]] && exit 0

readonly URL="${URL%/}/v1/private"
_proj="${OPIK_PROJECT:-$(cfg project_name)}"
readonly PROJECT="${_proj:-claude-code}"
readonly API_KEY="${OPIK_API_KEY:-$(cfg api_key)}"
readonly WORKSPACE="${OPIK_WORKSPACE:-$(cfg workspace)}"
readonly DEBUG="${OPIK_CC_DEBUG:-false}"
readonly TRUNCATE="${OPIK_CC_TRUNCATE_FIELDS:-true}"
readonly TRUNC_MSG='[ TRUNCATED -- set OPIK_CC_TRUNCATE_FIELDS=false ]'

command -v jq &>/dev/null || exit 0
command -v curl &>/dev/null || exit 0

# ─────────────────────────────────────────────────────────────────────────────
# Utilities
# ─────────────────────────────────────────────────────────────────────────────

log() {
    [[ "$DEBUG" != "true" ]] && return
    local f="/tmp/opik-debug.log"
    # Rotate if > 10000 lines
    if [[ -f "$f" ]] && (( $(wc -l < "$f") > 10000 )); then
        tail -1000 "$f" > "${f}.tmp" && mv "${f}.tmp" "$f"
    fi
    echo "[$(date +%H:%M:%S)] $*" >> "$f"
}

uuid7() {
    local ts hex rand var
    ts=$(($(date +%s) * 1000 + RANDOM % 1000))
    hex=$(printf '%012x' "$ts")
    rand=$(od -An -tx1 -N10 /dev/urandom | tr -d ' \n')
    var=$(printf '%02x' $((0x${rand:3:2} & 0x3F | 0x80)))
    echo "${hex:0:8}-${hex:8:4}-7${rand:0:3}-${var}${rand:5:2}-${rand:7:12}"
}

# Cross-platform md5 (macOS: md5, Linux: md5sum)
md5hash() { command -v md5sum &>/dev/null && md5sum | cut -d' ' -f1 || md5; }

# Cross-platform reverse file (macOS: tail -r, Linux: tac)
reverse_file() { command -v tac &>/dev/null && tac "$1" 2>/dev/null || tail -r "$1" 2>/dev/null; }

# Deterministic UUIDv7 from any UUID (for idempotent upserts)
to_v7() {
    local h b6 b8
    h=$(echo -n "$1" | md5hash | tr -d ' \n-')
    b6=$(printf '%02x' $((0x${h:12:2} & 0x0F | 0x70)))
    b8=$(printf '%02x' $((0x${h:16:2} & 0x3F | 0x80)))
    echo "${h:0:8}-${h:8:4}-${b6}${h:14:2}-${b8}${h:18:2}-${h:20:12}"
}

# ISO8601 with milliseconds (uses perl for cross-platform ms precision)
iso() {
    if command -v perl &>/dev/null; then
        perl -MTime::HiRes=gettimeofday -MPOSIX=strftime -e \
            'my($s,$us)=gettimeofday;print strftime("%Y-%m-%dT%H:%M:%S",gmtime($s)).sprintf(".%03dZ",$us/1000)'
    else
        date -u +%Y-%m-%dT%H:%M:%SZ
    fi
}
jv() { jq -r "$2 // \"${3:-}\"" <<< "$1"; }

api() {
    local method=$1 endpoint=$2 data=$3 async=${4:-false}
    local -a h=(-H "Content-Type: application/json")
    [[ -n "$API_KEY" ]] && h+=(-H "authorization: $API_KEY")
    [[ -n "$WORKSPACE" ]] && h+=(-H "Comet-Workspace: $WORKSPACE")

    if [[ "$async" == "true" ]]; then
        (curl -sS -X "$method" "${URL}${endpoint}" "${h[@]}" -d "$data" &>/dev/null) &
        disown 2>/dev/null
    elif [[ "$DEBUG" == "true" ]]; then
        local out code
        out=$(curl -sS -w "\n%{http_code}" -X "$method" "${URL}${endpoint}" "${h[@]}" -d "$data" 2>&1)
        code=$(tail -1 <<< "$out")
        [[ "$code" != 2* ]] && log "API $method $endpoint failed ($code): $(head -n -1 <<< "$out")"
    else
        curl -sS -X "$method" "${URL}${endpoint}" "${h[@]}" -d "$data" &>/dev/null
    fi
}

# ─────────────────────────────────────────────────────────────────────────────
# Input & State
# ─────────────────────────────────────────────────────────────────────────────

readonly INPUT=$(cat)
readonly EVENT=$(jq -r '.hook_event_name // ""' <<< "$INPUT")
readonly SESSION=$(jq -r '.session_id // ""' <<< "$INPUT")
readonly TRANSCRIPT=$(jq -r '.transcript_path // ""' <<< "$INPUT")
readonly STATE="/tmp/opik-${SESSION}.json"
readonly AGENTS="/tmp/opik-${SESSION}-agents.json"

log "=== $EVENT ==="

state() { [[ -f "$STATE" ]] && cat "$STATE" || echo ""; }

# ─────────────────────────────────────────────────────────────────────────────
# Span Builder
# ─────────────────────────────────────────────────────────────────────────────

span() {
    local id=$1 trace=$2 name=$3 type=$4 ts=$5 input=$6 output=$7 usage=${8:-'{}'} parent=${9:-}
    local s
    s=$(jq -nc --arg id "$id" --arg trace "$trace" --arg name "$name" --arg type "$type" \
        --arg ts "$ts" --arg proj "$PROJECT" --argjson in "$input" --argjson out "$output" \
        --argjson usage "$usage" \
        '{id:$id,trace_id:$trace,name:$name,type:$type,start_time:$ts,end_time:$ts,
          project_name:$proj,input:$in,output:$out,usage:$usage}')
    [[ -n "$parent" ]] && s=$(jq -c --arg p "$parent" '. + {parent_span_id:$p}' <<< "$s")
    echo "$s"
}

# ─────────────────────────────────────────────────────────────────────────────
# Transcript → Spans
# ─────────────────────────────────────────────────────────────────────────────

process() {
    local trace=$1 file=$2 start=${3:-0} parent=${4:-}
    [[ ! -f "$file" ]] && return

    local entries
    entries=$(tail -n +"$((start + 1))" "$file" 2>/dev/null)
    [[ -z "$entries" ]] && return

    # Parse assistant messages
    local parsed
    parsed=$(jq -c 'select(.type=="assistant") | select(.message.content[0]|type=="object") |
        {uuid,ts:.timestamp,ct:.message.content[0].type,c:.message.content[0],u:.message.usage}' <<< "$entries" 2>/dev/null)

    # Tool results lookup
    local results tasks
    results=$(jq -sc '[.[]|select(.type=="user")|select(.message.content[0].type=="tool_result")|
        {key:.message.content[0].tool_use_id,value:.message.content[0].content}]|from_entries' <<< "$entries" 2>/dev/null)
    tasks=$(jq -sc '[.[]|select(.type=="user")|select(.toolUseResult)|
        {key:.message.content[0].tool_use_id,value:.toolUseResult}]|from_entries' <<< "$entries" 2>/dev/null)
    [[ -z "$results" || "$results" == "null" ]] && results='{}'
    [[ -z "$tasks" || "$tasks" == "null" ]] && tasks='{}'

    while IFS= read -r e; do
        [[ -z "$e" ]] && continue
        local uuid ts ct sid
        uuid=$(jq -r '.uuid' <<< "$e")
        ts=$(jq -r '.ts' <<< "$e")
        ct=$(jq -r '.ct' <<< "$e")
        sid=$(to_v7 "$uuid")

        case "$ct" in
            thinking)
                local th usage inp out tot
                th=$(jq -r '.c.thinking//"" ' <<< "$e")
                usage=$(jq -c '.u//{}' <<< "$e")
                inp=$(jq '(.input_tokens//0)+(.cache_creation_input_tokens//0)' <<< "$usage")
                out=$(jq '.output_tokens//0' <<< "$usage")
                tot=$((inp + out))
                span "$sid" "$trace" "Thinking" "llm" "$ts" '{}' \
                    "$(jq -nc --arg t "$th" '{thinking:$t}')" \
                    "{\"prompt_tokens\":$inp,\"completion_tokens\":$out,\"total_tokens\":$tot}" "$parent"
                ;;
            text)
                local txt
                txt=$(jq -r '.c.text//""' <<< "$e")
                span "$sid" "$trace" "Text" "general" "$ts" '{}' \
                    "$(jq -nc --arg t "$txt" '{text:$t}')" '{}' "$parent"
                ;;
            tool_use)
                local name tid tin tout tusage
                name=$(jq -r '.c.name//"Tool"' <<< "$e")
                tid=$(jq -r '.c.id//""' <<< "$e")
                tusage='{}'

                # Mask file operation fields
                case "$name" in
                    Edit)
                        [[ "$TRUNCATE" == "true" ]] \
                            && tin=$(jq -c --arg m "$TRUNC_MSG" '.c.input|.old_string=$m|.new_string=$m' <<< "$e") \
                            || tin=$(jq -c '.c.input//{}' <<< "$e")
                        tout=$(jq -nc --arg m "$TRUNC_MSG" '{result:$m}')
                        ;;
                    Write)
                        [[ "$TRUNCATE" == "true" ]] \
                            && tin=$(jq -c --arg m "$TRUNC_MSG" '.c.input|.content=$m' <<< "$e") \
                            || tin=$(jq -c '.c.input//{}' <<< "$e")
                        tout=$(jq -nc --arg m "$TRUNC_MSG" '{result:$m}')
                        ;;
                    Read)
                        tin=$(jq -c '.c.input//{}' <<< "$e")
                        tout=$(jq -nc --arg m "$TRUNC_MSG" '{result:$m}')
                        ;;
                    Task)
                        local stype prompt tr resp tokens
                        stype=$(jq -r '.c.input.subagent_type//"Task"' <<< "$e")
                        name="${stype} Subagent"
                        prompt=$(jq -r '.c.input.prompt//""' <<< "$e")
                        tin=$(jq -nc --arg p "$prompt" '{prompt:$p}')
                        tr=$(jq -c --arg id "$tid" '.[$id]//null' <<< "$tasks")
                        if [[ -n "$tr" && "$tr" != "null" ]]; then
                            resp=$(jq -r '.content[0].text//""' <<< "$tr")
                            tout=$(jq -nc --arg r "$resp" '{response:$r}')
                            tokens=$(jq '.totalTokens//0' <<< "$tr")
                            [[ "$tokens" != "0" ]] && tusage="{\"total_tokens\":$tokens}"
                        else
                            tout='{}'
                        fi
                        ;;
                    *)
                        tin=$(jq -c '.c.input//{}' <<< "$e")
                        local res
                        res=$(jq -r --arg id "$tid" '.[$id]//""' <<< "$results")
                        [[ -n "$res" ]] && tout=$(jq -nc --arg r "$res" '{result:$r}') || tout='{}'
                        ;;
                esac
                span "$sid" "$trace" "$name" "tool" "$ts" "$tin" "$tout" "$tusage" "$parent"
                ;;
        esac
    done <<< "$parsed"
}

flush() {
    local s trace start spans n
    s=$(state); [[ -z "$s" ]] && return
    trace=$(jv "$s" '.trace_id')
    start=$(jv "$s" '.start_line' '0')
    spans=$(process "$trace" "$TRANSCRIPT" "$start")
    [[ -z "$spans" ]] && return
    n=$(wc -l <<< "$spans" | tr -d ' ')
    log "flush: $n spans"
    api POST "/spans/batch" "$(jq -sc '{spans:.}' <<< "$spans")"
}

# ─────────────────────────────────────────────────────────────────────────────
# Event Handlers
# ─────────────────────────────────────────────────────────────────────────────

on_prompt() {
    local prompt trace ts start now
    prompt=$(jv "$INPUT" '.prompt')
    trace=$(uuid7)
    ts=$(iso)
    start=0; [[ -f "$TRANSCRIPT" ]] && start=$(wc -l < "$TRANSCRIPT" | tr -d ' ')
    now=$(date +%s)

    jq -n --arg trace "$trace" --arg ts "$ts" --arg sess "$SESSION" \
        --arg trans "$TRANSCRIPT" --argjson start "$start" --argjson flush "$now" \
        '{trace_id:$trace,start_time:$ts,session_id:$sess,transcript:$trans,start_line:$start,last_flush:$flush}' > "$STATE"

    log "trace=$trace start=$start"
    # Sync call - trace must exist before spans arrive
    api POST "/traces" "$(jq -nc --arg id "$trace" --arg ts "$ts" --arg proj "$PROJECT" \
        --arg sess "$SESSION" --arg p "$prompt" \
        '{id:$id,name:"claude-code",start_time:$ts,project_name:$proj,
          thread_id:$sess,tags:["claude-code"],input:{text:$p}}')"
}

on_tool() {
    local s now last
    s=$(state); [[ -z "$s" ]] && return
    now=$(date +%s)
    last=$(jv "$s" '.last_flush' '0')
    if ((now - last >= 5)); then
        log "flushing ($((now - last))s)"
        flush
        # Use captured state to avoid race
        jq --argjson t "$now" '.last_flush=$t' <<< "$s" > "${STATE}.tmp" && mv "${STATE}.tmp" "$STATE"
    fi
}

on_stop() {
    local s trace start ts output
    s=$(state); [[ -z "$s" ]] && return
    trace=$(jv "$s" '.trace_id')
    start=$(jv "$s" '.start_line' '0')
    ts=$(iso)

    flush

    # Get final output (last text from assistant)
    output=""
    if [[ -f "$TRANSCRIPT" ]]; then
        output=$(tail -n +"$((start + 1))" "$TRANSCRIPT" 2>/dev/null | \
            jq -r 'select(.type=="assistant")|select(.message.content[0].type=="text")|
            .message.content[0].text' 2>/dev/null | tail -1) || true
    fi

    api PATCH "/traces/$trace" "$(jq -nc --arg proj "$PROJECT" --arg ts "$ts" --arg out "$output" \
        '{project_name:$proj,end_time:$ts,output:{text:$out}}')"

    rm -f "$STATE" "$AGENTS"
    log "done"
}

on_compact() {
    local s trace ts sid
    s=$(state); [[ -z "$s" ]] && return
    trace=$(jv "$s" '.trace_id')
    ts=$(iso)
    sid=$(uuid7)
    flush
    api POST "/spans/batch" "{\"spans\":[$(span "$sid" "$trace" "Compaction" "general" "$ts" \
        '{"event":"context_compacted"}' '{"status":"compacted"}')]}"
}

on_subagent_start() {
    local aid atype task_uuid existing
    aid=$(jv "$INPUT" '.agent_id')
    atype=$(jv "$INPUT" '.agent_type')
    log "subagent_start: $aid ($atype)"
    [[ -z "$aid" ]] && return

    task_uuid=$(reverse_file "$TRANSCRIPT" | jq -r 'select(.type=="assistant")|
        select(.message.content[0].type=="tool_use")|select(.message.content[0].name=="Task")|.uuid' | head -1)
    [[ -z "$task_uuid" || "$task_uuid" == "null" ]] && return

    log "map: $aid -> $task_uuid"
    existing='{}'; [[ -f "$AGENTS" ]] && existing=$(cat "$AGENTS")
    jq --arg a "$aid" --arg u "$task_uuid" '.+{($a):$u}' <<< "$existing" > "$AGENTS"
}

on_subagent_stop() {
    local s aid atrans trace parent_uuid parent_sid spans n
    s=$(state); [[ -z "$s" ]] && return
    aid=$(jv "$INPUT" '.agent_id')
    atrans=$(jv "$INPUT" '.agent_transcript_path')
    log "subagent_stop: $aid"

    [[ -z "$aid" || -z "$atrans" || ! -f "$atrans" ]] && return
    trace=$(jv "$s" '.trace_id')

    parent_uuid=""
    [[ -f "$AGENTS" ]] && parent_uuid=$(jq -r --arg a "$aid" '.[$a]//""' "$AGENTS")
    [[ -z "$parent_uuid" ]] && return

    parent_sid=$(to_v7 "$parent_uuid")
    log "processing subagent with parent=$parent_sid"

    spans=$(process "$trace" "$atrans" 0 "$parent_sid")
    [[ -z "$spans" ]] && return
    n=$(wc -l <<< "$spans" | tr -d ' ')
    log "subagent flush: $n spans"
    api POST "/spans/batch" "$(jq -sc '{spans:.}' <<< "$spans")"
}

# ─────────────────────────────────────────────────────────────────────────────
# Main
# ─────────────────────────────────────────────────────────────────────────────

case "$EVENT" in
    UserPromptSubmit)   on_prompt ;;
    PostToolUse|PostToolUseFailure) on_tool ;;
    SubagentStart)      on_subagent_start ;;
    SubagentStop)       on_subagent_stop ;;
    Stop|SessionEnd)    on_stop ;;
    PreCompact)         on_compact ;;
    *)                  log "unknown: $EVENT" ;;
esac

exit 0
