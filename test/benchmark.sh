#!/bin/bash
set -e

# Babadook Benchmark for /opik:instrument
# Clones adversarial agent, instruments it, runs it, evaluates traces via Opik SDK

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
BABADOOK_REPO="git@github.com:comet-ml/adversarial-benchmark-agent.git"
BABADOOK_DIR="/tmp/babadook-benchmark-$$"
PROJECT_NAME="babadook-benchmark-$(date +%s)"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

log() { echo -e "${GREEN}[BENCH]${NC} $1"; }
info() { echo -e "${CYAN}[INFO]${NC} $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
fail() { echo -e "${RED}[FAIL]${NC} $1"; exit 1; }

cleanup() {
    if [[ "${BENCH_PASSED:-0}" == "1" ]]; then
        log "Cleaning up..."
        rm -rf "$BABADOOK_DIR"
        rm -f /tmp/bench-instrument-output-$$.txt
        rm -f /tmp/bench-agent-output-$$.txt
    else
        log "Keeping $BABADOOK_DIR for debugging (benchmark failed)"
        log "Instrument output: /tmp/bench-instrument-output-$$.txt"
    fi
}
trap cleanup EXIT

# ── Phase 1: Prerequisites ─────────────────────────────────────────

check_prereqs() {
    log "Checking prerequisites..."

    # Claude CLI
    if ! command -v claude &>/dev/null; then
        fail "Claude CLI not found. Install from: https://claude.ai/download"
    fi
    info "Claude CLI: $(which claude)"

    # Python + opik
    if ! command -v python3 &>/dev/null; then
        fail "python3 not found"
    fi
    if ! python3 -c "import opik" 2>/dev/null; then
        fail "opik package not installed. Run: pip install opik"
    fi
    info "Python + opik: OK"

    # OPENAI_API_KEY
    if [[ -z "$OPENAI_API_KEY" ]]; then
        fail "OPENAI_API_KEY not set (babadook needs it)"
    fi
    info "OPENAI_API_KEY: set"

    # Opik config
    if [[ ! -f "$HOME/.opik.config" ]]; then
        fail "Missing ~/.opik.config - run 'opik configure' first"
    fi
    OPIK_URL=$(grep -E "^url_override\s*=" "$HOME/.opik.config" | sed 's/.*=\s*//' | tr -d ' ')
    OPIK_API_KEY=$(grep -E "^api_key\s*=" "$HOME/.opik.config" | sed 's/.*=\s*//' | tr -d ' ')
    OPIK_WORKSPACE=$(grep -E "^workspace\s*=" "$HOME/.opik.config" | sed 's/.*=\s*//' | tr -d ' ')

    if [[ -z "$OPIK_URL" ]]; then
        fail "No url_override in ~/.opik.config"
    fi
    info "Opik URL: $OPIK_URL"

    # Git
    if ! command -v git &>/dev/null; then
        fail "git not found"
    fi
    info "git: OK"

    # Node/npm
    if ! command -v node &>/dev/null; then
        fail "node not found"
    fi
    if ! command -v npm &>/dev/null; then
        fail "npm not found"
    fi
    info "node/npm: OK"
}

# ── Phase 2: Clone babadook ────────────────────────────────────────

clone_babadook() {
    log "Cloning babadook..."
    git clone --depth 1 "$BABADOOK_REPO" "$BABADOOK_DIR"
    rm -f "$BABADOOK_DIR/README.md"  # Don't give away the patterns
    info "Cloned to $BABADOOK_DIR"
}

# ── Phase 3: Run /opik:instrument ──────────────────────────────────

run_instrument() {
    log "Running /opik:instrument against babadook..."
    info "This may take several minutes..."

    cd "$BABADOOK_DIR"
    claude -p \
        --dangerously-skip-permissions \
        --max-turns 50 \
        --model sonnet \
        --plugin-dir "$ROOT_DIR" \
        --output-format stream-json \
        --verbose \
        -- "/opik:instrument" \
        2>&1 | tee /tmp/bench-instrument-output-$$.txt

    log "Instrument command completed"
}

# ── Phase 4: Install deps & run agent ──────────────────────────────

run_agent() {
    log "Installing dependencies and running agent..."

    cd "$BABADOOK_DIR"

    # Install deps (instrument command should have done this, but ensure)
    uv sync 2>/dev/null || pip install -r requirements.txt 2>/dev/null || true
    npm install 2>/dev/null || true

    # Set Opik env vars so traces go to the right place
    export OPIK_PROJECT_NAME="$PROJECT_NAME"
    export OPIK_URL_OVERRIDE="$OPIK_URL"
    export OPIK_HOST="$OPIK_URL"  # TS SDK uses this
    export OPIK_API_KEY="$OPIK_API_KEY"
    export OPIK_WORKSPACE="$OPIK_WORKSPACE"

    info "Project: $PROJECT_NAME"

    # Run the agent
    uv run python cli.py "What are the benefits of renewable energy?" \
        --provider=openai -v \
        2>&1 | tee /tmp/bench-agent-output-$$.txt
    AGENT_EXIT_CODE=${PIPESTATUS[0]}

    info "Agent exit code: $AGENT_EXIT_CODE"

    # Wait for async trace flush
    log "Waiting for trace flush..."
    sleep 10
}

# ── Phase 5: Run evaluation ────────────────────────────────────────

run_eval() {
    log "Running evaluation..."

    python3 "$SCRIPT_DIR/benchmark_eval.py" \
        --project-name "$PROJECT_NAME" \
        --experiment-name "instrument-$(date +%Y%m%d-%H%M%S)" \
        --babadook-dir "$BABADOOK_DIR" \
        --agent-exit-code "$AGENT_EXIT_CODE"
    EVAL_EXIT_CODE=$?

    return $EVAL_EXIT_CODE
}

# ── Main ───────────────────────────────────────────────────────────

main() {
    echo ""
    echo "=========================================="
    echo "  Babadook Benchmark"
    echo "  /opik:instrument adversarial test"
    echo "=========================================="
    echo ""

    check_prereqs
    clone_babadook
    run_instrument
    run_agent

    echo ""

    if run_eval; then
        echo ""
        BENCH_PASSED=1
        log "${GREEN}Benchmark PASSED${NC}"
        BASE_URL="${OPIK_URL%/api/}"
        BASE_URL="${BASE_URL%/api}"
        echo ""
        echo "View experiment at: ${BASE_URL}/${OPIK_WORKSPACE}/experiments"
        exit 0
    else
        echo ""
        log "${RED}Benchmark FAILED${NC}"
        exit 1
    fi
}

main "$@"
