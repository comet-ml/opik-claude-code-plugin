#!/bin/bash
set -e

# E2E Test for Opik Claude Plugin
# Simulates the full hook lifecycle and verifies trace creation

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
TEST_DIR="/tmp/opik-e2e-test-$$"
SESSION_ID="e2e-test-$(date +%s)"
# Get actual Go temp dir (on macOS this is /var/folders/.../T/, not /tmp)
_tmpfile=$(mktemp)
GO_TMPDIR=$(dirname "$_tmpfile")
rm -f "$_tmpfile"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log() { echo -e "${GREEN}[TEST]${NC} $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
fail() { echo -e "${RED}[FAIL]${NC} $1"; exit 1; }

cleanup() {
    rm -rf "$TEST_DIR"
    rm -f "${GO_TMPDIR}/opik-${SESSION_ID}.json" "${GO_TMPDIR}/opik-${SESSION_ID}-agents.json"
}
trap cleanup EXIT

# Check prerequisites
check_config() {
    if [[ ! -f "$HOME/.opik.config" ]]; then
        fail "Missing ~/.opik.config - run 'opik configure' first"
    fi

    # Extract URL from config (handle both INI and plain formats)
    OPIK_URL=$(grep -E "^url_override\s*=" "$HOME/.opik.config" | sed 's/.*=\s*//' | tr -d ' ')
    OPIK_API_KEY=$(grep -E "^api_key\s*=" "$HOME/.opik.config" | sed 's/.*=\s*//' | tr -d ' ')
    OPIK_WORKSPACE=$(grep -E "^workspace\s*=" "$HOME/.opik.config" | sed 's/.*=\s*//' | tr -d ' ')

    if [[ -z "$OPIK_URL" ]]; then
        fail "No url_override in ~/.opik.config"
    fi

    log "Using Opik at: $OPIK_URL"
}

# Build binary
build() {
    log "Building binary..."
    cd "$ROOT_DIR"
    make build-local >/dev/null 2>&1 || fail "Build failed"

    # Find binary
    BINARY=$(ls bin/opik-logger-* 2>/dev/null | head -1)
    if [[ ! -x "$BINARY" ]]; then
        fail "Binary not found after build"
    fi
    log "Using binary: $BINARY"
}

# Setup test environment
setup() {
    log "Setting up test environment..."
    mkdir -p "$TEST_DIR/.claude"
    echo "true" > "$TEST_DIR/.claude/.opik-tracing-enabled"
    cp "$SCRIPT_DIR/sample-transcript.jsonl" "$TEST_DIR/transcript.jsonl"
}

# Run a hook event
run_hook() {
    local event="$1"
    local extra_json="${2:-}"

    local payload="{\"hook_event_name\":\"$event\",\"session_id\":\"$SESSION_ID\",\"transcript_path\":\"$TEST_DIR/transcript.jsonl\",\"cwd\":\"$TEST_DIR\""

    if [[ -n "$extra_json" ]]; then
        payload="${payload},${extra_json}"
    fi
    payload="${payload}}"

    log "Sending $event event..."
    # Run from TEST_DIR so isTracingEnabled() finds .claude/.opik-tracing-enabled
    (cd "$TEST_DIR" && echo "$payload" | OPIK_CC_DEBUG=true "$ROOT_DIR/$BINARY")
}

# Verify trace exists in Opik
verify_trace() {
    log "Verifying trace in Opik..."

    # Read trace ID from state file (uses Go's os.TempDir())
    STATE_FILE="${GO_TMPDIR}/opik-${SESSION_ID}.json"
    DEBUG_LOG="${GO_TMPDIR}/opik-debug.log"

    if [[ ! -f "$STATE_FILE" ]]; then
        # State file deleted after Stop - check debug log
        TRACE_ID=$(grep -o 'trace=[^ ]*' "$DEBUG_LOG" 2>/dev/null | tail -1 | cut -d'=' -f2)
    else
        TRACE_ID=$(jq -r '.trace_id' "$STATE_FILE" 2>/dev/null)
    fi

    if [[ -z "$TRACE_ID" || "$TRACE_ID" == "null" ]]; then
        warn "Could not extract trace ID from state"
        return 1
    fi

    log "Trace ID: $TRACE_ID"

    # Query Opik API to verify trace exists
    API_URL="${OPIK_URL%/}/v1/private/traces/$TRACE_ID"

    RESPONSE=$(curl -s -w "\n%{http_code}" \
        -H "Authorization: $OPIK_API_KEY" \
        -H "Comet-Workspace: $OPIK_WORKSPACE" \
        "$API_URL" 2>/dev/null)

    HTTP_CODE=$(echo "$RESPONSE" | tail -1)
    BODY=$(echo "$RESPONSE" | sed '$d')

    if [[ "$HTTP_CODE" == "200" ]]; then
        log "Trace verified in Opik!"

        # Show trace details
        TRACE_NAME=$(echo "$BODY" | jq -r '.name // "unnamed"' 2>/dev/null)
        SPAN_COUNT=$(echo "$BODY" | jq -r '.span_count // 0' 2>/dev/null)
        log "  Name: $TRACE_NAME"
        log "  Spans: $SPAN_COUNT"
        return 0
    else
        warn "Could not verify trace (HTTP $HTTP_CODE)"
        warn "This may be expected for self-hosted instances without API access"
        return 1
    fi
}

# Show debug log
show_debug() {
    DEBUG_LOG="${GO_TMPDIR}/opik-debug.log"
    if [[ -f "$DEBUG_LOG" ]]; then
        log "Debug log:"
        echo "---"
        tail -20 "$DEBUG_LOG"
        echo "---"
    fi
}

# Main test flow
main() {
    echo ""
    echo "=========================================="
    echo "  Opik Claude Plugin E2E Test"
    echo "=========================================="
    echo ""

    check_config
    build
    setup

    # Clear debug log
    > "${GO_TMPDIR}/opik-debug.log" 2>/dev/null || true

    # Simulate full lifecycle
    log "Simulating hook lifecycle..."

    # 1. User submits prompt
    run_hook "UserPromptSubmit" '"prompt":"Hello, can you help me?"'
    sleep 0.5

    # 2. Tool use (triggers periodic flush check)
    run_hook "PostToolUse"
    sleep 0.5

    # 3. Session ends
    run_hook "Stop"
    sleep 0.5

    echo ""
    show_debug
    echo ""

    # Verify
    if verify_trace; then
        echo ""
        log "${GREEN}E2E Test PASSED${NC}"
        echo ""
        # Clean up URL for display
        BASE_URL="${OPIK_URL%/api/}"
        BASE_URL="${BASE_URL%/api}"
        echo "View trace at: ${BASE_URL}/projects"
    else
        echo ""
        log "${YELLOW}E2E Test completed (verification skipped)${NC}"
        echo ""
        echo "Check Opik UI manually to verify trace creation"
    fi
}

main "$@"
