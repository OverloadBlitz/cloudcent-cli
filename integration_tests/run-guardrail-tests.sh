#!/usr/bin/env bash
#
# Integration tests for the cost guardrail (`--budget`, `--baseline`,
# `--max-increase`, `--max-increase-pct`, `--no-fail`) and its exit-code
# contract (0 = pass, 2 = breach).
#
# Runs `cloudcent pulumi estimate` against the aws-py-s3-folder test project
# (~$1.24/mo) with various guardrail flag combinations and checks the exit
# code plus the "guardrail" block in the JSON output.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
PROJECT="$SCRIPT_DIR/testdata/pulumi-examples/aws-py-s3-folder"

# ── defaults ────────────────────────────────────────────────────────────────
BINARY=""

# ── parse args ──────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --binary) BINARY="$2"; shift 2 ;;
    *) echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done

# ── build binary if not provided ────────────────────────────────────────────
CLEANUP_BIN=""
if [[ -z "$BINARY" ]]; then
  echo "Building cloudcent binary…"
  go build -o "$REPO_ROOT/cloudcent-guardrail-test-bin" "$REPO_ROOT"
  BINARY="$REPO_ROOT/cloudcent-guardrail-test-bin"
  CLEANUP_BIN="$BINARY"
fi

if [[ ! -x "$BINARY" ]]; then
  echo "Error: binary not found or not executable: $BINARY" >&2
  exit 1
fi

if [[ ! -d "$PROJECT" ]]; then
  echo "Error: test project not found: $PROJECT" >&2
  exit 1
fi

if ! command -v jq &>/dev/null; then
  echo "Error: jq is required but not installed. Install with: brew install jq" >&2
  exit 1
fi

# ── workdir & cleanup ───────────────────────────────────────────────────────
WORKDIR="$(mktemp -d)"
cleanup() {
  rm -rf "$WORKDIR"
  [[ -n "$CLEANUP_BIN" ]] && rm -f "$CLEANUP_BIN"
}
trap cleanup EXIT

OUT_JSON="$WORKDIR/out.json"
ERR_LOG="$WORKDIR/stderr.log"

# Synthetic baseline estimates (only totals.monthly_total is read by --baseline).
BASELINE_LOW="$WORKDIR/baseline_low.json"
BASELINE_HIGH="$WORKDIR/baseline_high.json"
echo '{"totals":{"monthly_total":"0.50"}}'    > "$BASELINE_LOW"
echo '{"totals":{"monthly_total":"1000.00"}}' > "$BASELINE_HIGH"

# ── counters ────────────────────────────────────────────────────────────────
PASS=0
FAIL=0
FAILURES=()

# ── helpers ─────────────────────────────────────────────────────────────────

# run_estimate <extra args...>
# Runs the binary against the test project, writes JSON to $OUT_JSON and sets
# $EXIT_CODE. Stderr is captured to $ERR_LOG for diagnostics on failure.
run_estimate() {
  "$BINARY" pulumi estimate "$PROJECT" "$@" -o json >"$OUT_JSON" 2>"$ERR_LOG"
  EXIT_CODE=$?
}

# assert_exit <expected_exit>
assert_exit() {
  local expected="$1"
  if [[ "$EXIT_CODE" == "$expected" ]]; then
    echo "    ✓ exit code: $EXIT_CODE"
    return 0
  else
    echo "    ✗ exit code: expected=$expected actual=$EXIT_CODE"
    sed 's/^/      /' "$ERR_LOG" >&2
    return 1
  fi
}

# assert_jq <label> <jq_filter> <expected>
assert_jq() {
  local label="$1" filter="$2" expected="$3"
  local actual
  actual=$(jq -r "$filter" "$OUT_JSON" 2>/dev/null)
  if [[ "$actual" == "$expected" ]]; then
    echo "    ✓ $label: $actual"
    return 0
  else
    echo "    ✗ $label: expected=$expected actual=$actual"
    return 1
  fi
}

# assert_contains <label> <jq_filter> <needle>
# Checks that the jq result (a string, e.g. breaches joined together) contains
# the given substring.
assert_contains() {
  local label="$1" filter="$2" needle="$3"
  local actual
  actual=$(jq -r "$filter" "$OUT_JSON" 2>/dev/null)
  if [[ "$actual" == *"$needle"* ]]; then
    echo "    ✓ $label contains \"$needle\""
    return 0
  else
    echo "    ✗ $label does not contain \"$needle\": $actual"
    return 1
  fi
}

# assert_gt <label> <jq_filter> <threshold>
assert_gt() {
  local label="$1" filter="$2" threshold="$3"
  local actual
  actual=$(jq -r "$filter" "$OUT_JSON" 2>/dev/null)
  if python3 -c "import sys; sys.exit(0 if float('$actual') > float('$threshold') else 1)" 2>/dev/null; then
    echo "    ✓ $label ($actual) > $threshold"
    return 0
  else
    echo "    ✗ $label ($actual) <= $threshold"
    return 1
  fi
}

# run_test <name> <test_fn>
run_test() {
  local name="$1" fn="$2"
  echo ""
  echo "── guardrail: $name ──────────────────────────────────────"
  if "$fn"; then
    echo "  PASS"
    ((PASS++)) || true
  else
    echo "  FAIL"
    FAILURES+=("$name")
    ((FAIL++)) || true
  fi
}

# ── test scenarios ──────────────────────────────────────────────────────────
# All thresholds use 80x+ margins around the ~$1.24/mo actual cost to avoid
# flakiness from live pricing fluctuations.

test_budget_pass() {
  run_estimate --budget 100
  local ok=true
  assert_exit 0 || ok=false
  assert_jq "guardrail.passed" '.guardrail.passed' "true" || ok=false
  $ok
}

test_budget_breach() {
  run_estimate --budget 0.50
  local ok=true
  assert_exit 2 || ok=false
  assert_jq "guardrail.passed" '.guardrail.passed' "false" || ok=false
  assert_contains "guardrail.breaches" '.guardrail.breaches | join("; ")' "exceeds budget" || ok=false
  $ok
}

test_budget_breach_no_fail() {
  run_estimate --budget 0.50 --no-fail
  local ok=true
  assert_exit 0 || ok=false
  assert_jq "guardrail.passed" '.guardrail.passed' "false" || ok=false
  $ok
}

test_no_guardrail_flags() {
  run_estimate
  local ok=true
  assert_exit 0 || ok=false
  assert_jq "guardrail" '.guardrail' "null" || ok=false
  $ok
}

test_baseline_max_increase_pass() {
  run_estimate --baseline "$BASELINE_HIGH" --max-increase 100
  local ok=true
  assert_exit 0 || ok=false
  assert_jq "guardrail.passed" '.guardrail.passed' "true" || ok=false
  $ok
}

test_baseline_max_increase_breach() {
  run_estimate --baseline "$BASELINE_LOW" --max-increase 0.01
  local ok=true
  assert_exit 2 || ok=false
  assert_jq "guardrail.passed" '.guardrail.passed' "false" || ok=false
  assert_gt "guardrail.delta" '.guardrail.delta' 0 || ok=false
  assert_contains "guardrail.breaches" '.guardrail.breaches | join("; ")' "max increase" || ok=false
  $ok
}

test_baseline_max_increase_pct_pass() {
  run_estimate --baseline "$BASELINE_HIGH" --max-increase-pct 500
  local ok=true
  assert_exit 0 || ok=false
  assert_jq "guardrail.passed" '.guardrail.passed' "true" || ok=false
  $ok
}

test_baseline_max_increase_pct_breach() {
  run_estimate --baseline "$BASELINE_LOW" --max-increase-pct 10
  local ok=true
  assert_exit 2 || ok=false
  assert_jq "guardrail.passed" '.guardrail.passed' "false" || ok=false
  assert_contains "guardrail.breaches" '.guardrail.breaches | join("; ")' "%" || ok=false
  $ok
}

test_combined_all_breach() {
  run_estimate --budget 0.01 --baseline "$BASELINE_LOW" --max-increase 0.01 --max-increase-pct 1
  local ok=true
  assert_exit 2 || ok=false
  assert_jq "guardrail.passed" '.guardrail.passed' "false" || ok=false
  local n_breaches
  n_breaches=$(jq '.guardrail.breaches | length' "$OUT_JSON" 2>/dev/null)
  if [[ "${n_breaches:-0}" -ge 2 ]]; then
    echo "    ✓ multiple breaches: $n_breaches"
  else
    echo "    ✗ expected >=2 breaches, got ${n_breaches:-0}"
    ok=false
  fi
  $ok
}

# ── run tests ───────────────────────────────────────────────────────────────
echo "=== Guardrail tests (project: aws-py-s3-folder) ==="

run_test "budget-pass"                       test_budget_pass
run_test "budget-breach"                     test_budget_breach
run_test "budget-breach-no-fail"             test_budget_breach_no_fail
run_test "no-guardrail-flags"                test_no_guardrail_flags
run_test "baseline-max-increase-pass"        test_baseline_max_increase_pass
run_test "baseline-max-increase-breach"      test_baseline_max_increase_breach
run_test "baseline-max-increase-pct-pass"    test_baseline_max_increase_pct_pass
run_test "baseline-max-increase-pct-breach"  test_baseline_max_increase_pct_breach
run_test "combined-all-breach"               test_combined_all_breach

# ── summary ──────────────────────────────────────────────────────────────────
TOTAL=$(( PASS + FAIL ))
echo ""
echo "════════════════════════════════════════"
echo "Results: ${PASS} passed, ${FAIL} failed (of ${TOTAL})"
if [[ ${#FAILURES[@]} -gt 0 ]]; then
  echo "Failed:"
  for f in "${FAILURES[@]}"; do
    echo "  • $f"
  done
fi
echo "════════════════════════════════════════"

[[ $FAIL -eq 0 ]]
