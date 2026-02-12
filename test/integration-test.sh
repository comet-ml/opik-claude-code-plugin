#!/bin/bash
set -e

# Integration Test for Opik Claude Plugin
# Runs actual Claude Code with the plugin and verifies traces in Opik

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
TEST_DIR="/tmp/opik-integration-test-$$"

# Get actual Go temp dir (on macOS this is /var/folders/.../T/, not /tmp)
_tmpfile=$(mktemp)
GO_TMPDIR=$(dirname "$_tmpfile")
rm -f "$_tmpfile"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

log() { echo -e "${GREEN}[TEST]${NC} $1"; }
info() { echo -e "${CYAN}[INFO]${NC} $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
fail() { echo -e "${RED}[FAIL]${NC} $1"; exit 1; }

cleanup() {
    rm -rf "$TEST_DIR"
}
trap cleanup EXIT

# Check prerequisites
check_prereqs() {
    log "Checking prerequisites..."

    # Check Claude CLI
    if ! command -v claude &>/dev/null; then
        fail "Claude CLI not found. Install from: https://claude.ai/download"
    fi
    info "Claude CLI: $(which claude)"

    # Check Opik config
    if [[ ! -f "$HOME/.opik.config" ]]; then
        fail "Missing ~/.opik.config - run 'opik configure' first"
    fi

    # Extract config
    OPIK_URL=$(grep -E "^url_override\s*=" "$HOME/.opik.config" | sed 's/.*=\s*//' | tr -d ' ')
    OPIK_API_KEY=$(grep -E "^api_key\s*=" "$HOME/.opik.config" | sed 's/.*=\s*//' | tr -d ' ')
    OPIK_WORKSPACE=$(grep -E "^workspace\s*=" "$HOME/.opik.config" | sed 's/.*=\s*//' | tr -d ' ')

    if [[ -z "$OPIK_URL" ]]; then
        fail "No url_override in ~/.opik.config"
    fi
    info "Opik URL: $OPIK_URL"

    # Check binaries exist
    if ! ls "$ROOT_DIR/bin/opik-logger-"* &>/dev/null; then
        fail "No binaries found in bin/. Run 'make build' first."
    fi
    info "Binaries found in bin/"

    # Check plugin is installed
    if ! claude plugin list 2>/dev/null | grep -q "opik@opik"; then
        warn "Plugin not installed. Installing..."
        claude plugin marketplace add "$ROOT_DIR" 2>/dev/null || true
        claude plugin install opik@opik 2>/dev/null || fail "Could not install plugin"
    fi
    info "Plugin installed: opik@opik"
}

# Setup test project
setup() {
    log "Setting up test project..."
    mkdir -p "$TEST_DIR/.claude"

    # Enable tracing for this project
    echo "true" > "$TEST_DIR/.claude/.opik-tracing-enabled"

    # Create a simple file for Claude to read
    echo "Hello from the integration test!" > "$TEST_DIR/test-file.txt"

    info "Test directory: $TEST_DIR"
}

# Run Claude with plugin
run_claude() {
    log "Running Claude Code with plugin..."

    # Clear debug log
    > "${GO_TMPDIR}/opik-debug.log" 2>/dev/null || true

    # Simple prompt that completes quickly
    local prompt="Read the file test-file.txt and tell me what it says. Be brief."

    info "Prompt: $prompt"
    info "This may take a moment..."

    # Run Claude (plugin must be installed: claude plugin install opik@opik)
    # Use --print for single-shot mode, --dangerously-skip-permissions to avoid prompts
    cd "$TEST_DIR"

    local output
    # Enable debug logging for the plugin
    export OPIK_CC_DEBUG=true

    if output=$(echo "$prompt" | claude \
        --print \
        --dangerously-skip-permissions \
        --max-turns 3 \
        2>&1); then

        log "Claude completed successfully"
        echo ""
        echo "--- Claude Output ---"
        echo "$output" | head -20
        echo "---"
        echo ""
        return 0
    else
        local exit_code=$?
        warn "Claude exited with code $exit_code"
        echo "$output" | tail -10
        return $exit_code
    fi
}

# Show debug log
show_debug() {
    DEBUG_LOG="${GO_TMPDIR}/opik-debug.log"
    if [[ -f "$DEBUG_LOG" ]] && [[ -s "$DEBUG_LOG" ]]; then
        log "Debug log:"
        echo "---"
        cat "$DEBUG_LOG"
        echo "---"
    else
        warn "No debug log found (tracing may not have triggered)"
    fi
}

# Verify trace in Opik
verify_trace() {
    log "Verifying trace in Opik..."

    DEBUG_LOG="${GO_TMPDIR}/opik-debug.log"

    # Extract trace ID from debug log
    if [[ ! -f "$DEBUG_LOG" ]]; then
        warn "No debug log found"
        return 1
    fi

    TRACE_ID=$(grep -o 'trace=[^ ]*' "$DEBUG_LOG" 2>/dev/null | head -1 | cut -d'=' -f2)

    if [[ -z "$TRACE_ID" ]]; then
        warn "Could not extract trace ID from debug log"
        return 1
    fi

    info "Trace ID: $TRACE_ID"

    # Query Opik API
    API_URL="${OPIK_URL%/}/v1/private/traces/$TRACE_ID"

    local response http_code body
    response=$(curl -s -w "\n%{http_code}" \
        -H "Authorization: $OPIK_API_KEY" \
        -H "Comet-Workspace: $OPIK_WORKSPACE" \
        "$API_URL" 2>/dev/null)

    http_code=$(echo "$response" | tail -1)
    body=$(echo "$response" | sed '$d')

    if [[ "$http_code" == "200" ]]; then
        log "Trace verified in Opik!"

        local trace_name span_count model
        trace_name=$(echo "$body" | jq -r '.name // "unnamed"' 2>/dev/null)
        span_count=$(echo "$body" | jq -r '.span_count // 0' 2>/dev/null)
        model=$(echo "$body" | jq -r '.metadata.model // "unknown"' 2>/dev/null)

        info "  Name: $trace_name"
        info "  Spans: $span_count"
        info "  Model: $model"

        # Verify we got meaningful data
        if [[ "$span_count" -gt 0 ]]; then
            return 0
        else
            warn "Trace has no spans"
            return 1
        fi
    else
        warn "Could not verify trace (HTTP $http_code)"
        return 1
    fi
}

# Main
main() {
    echo ""
    echo "=========================================="
    echo "  Opik Plugin Integration Test"
    echo "  (Full Claude Code E2E)"
    echo "=========================================="
    echo ""

    check_prereqs
    setup

    # Run Claude and capture result
    local claude_ok=true
    if ! run_claude; then
        claude_ok=false
    fi

    echo ""
    show_debug
    echo ""

    # Verify trace
    if verify_trace; then
        echo ""
        log "${GREEN}Integration Test PASSED${NC}"
        echo ""
        BASE_URL="${OPIK_URL%/api/}"
        BASE_URL="${BASE_URL%/api}"
        echo "View trace at: ${BASE_URL}/projects"
        exit 0
    else
        echo ""
        if [[ "$claude_ok" == "true" ]]; then
            warn "Claude ran but trace verification failed"
            warn "Check if Opik is running and accessible"
        fi
        log "${RED}Integration Test FAILED${NC}"
        exit 1
    fi
}

main "$@"
