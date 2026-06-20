#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SNAPSHOTS_PULUMI="$SCRIPT_DIR/snapshots/pulumi"
SNAPSHOTS_DRAWIO="$SCRIPT_DIR/snapshots/drawio"
TESTDATA_PULUMI="$SCRIPT_DIR/testdata/pulumi-examples"
TESTDATA_DRAWIO="$SCRIPT_DIR/testdata/drawio-diagrams"

# ── whitelists ───────────────────────────────────────────────────────────────

PULUMI_WHITELIST=(
  aws-py-webserver
  aws-py-appsync
  aws-py-fargate
  aws-py-s3-folder
  aws-py-apigatewayv2-http-api-quickcreate
  # aws-py-resources
  aws-py-voting-app
  azure-py-webserver
)

DRAWIO_WHITELIST=(
  #aws-saas-example
  #aws-simple-architecture
)

# ── defaults ────────────────────────────────────────────────────────────────
BINARY=""
TOLERANCE=20

# ── parse args ──────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --binary)    BINARY="$2";    shift 2 ;;
    --tolerance) TOLERANCE="$2"; shift 2 ;;
    *) echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done

# ── build binary if not provided ────────────────────────────────────────────
if [[ -z "$BINARY" ]]; then
  echo "Building cloudcent binary…"
  go build -o "$REPO_ROOT/cloudcent-test-bin" "$REPO_ROOT"
  BINARY="$REPO_ROOT/cloudcent-test-bin"
  trap 'rm -f "$REPO_ROOT/cloudcent-test-bin"' EXIT
fi

if [[ ! -x "$BINARY" ]]; then
  echo "Error: binary not found or not executable: $BINARY" >&2
  exit 1
fi

# ── dependency check ────────────────────────────────────────────────────────
if ! command -v jq &>/dev/null; then
  echo "Error: jq is required but not installed. Install with: brew install jq" >&2
  exit 1
fi

# ── counters ────────────────────────────────────────────────────────────────
PASS=0
FAIL=0
SKIP=0
FAILURES=()

# ── helpers ─────────────────────────────────────────────────────────────────

# compare_field <label> <snapshot_val> <actual_val>
# Returns 0 (pass) or 1 (fail), prints a result line.
compare_field() {
  local label="$1" snap="$2" actual="$3"
  if [[ "$snap" == "$actual" ]]; then
    echo "    ✓ $label: $actual"
    return 0
  else
    echo "    ✗ $label: expected=$snap actual=$actual"
    return 1
  fi
}

# compare_price <label> <snapshot_rate> <actual_rate> <tolerance_pct>
# Allows ±tolerance% difference. Returns 0 (pass) or 1 (fail).
compare_price() {
  local label="$1" snap="$2" actual="$3" tol="$4"
  # Use python3 for float arithmetic (available on macOS/Linux by default).
  local result
  result=$(python3 - <<EOF
snap = float("$snap") if "$snap" else 0.0
actual = float("$actual") if "$actual" else 0.0
tol = float("$tol") / 100.0
if snap == 0 and actual == 0:
    print("pass")
elif snap == 0:
    print("fail:snap=0,actual=%.10f" % actual)
else:
    diff = abs(actual - snap) / abs(snap)
    if diff <= tol:
        print("pass")
    else:
        print("fail:diff=%.1f%%" % (diff * 100))
EOF
)
  if [[ "$result" == "pass" ]]; then
    echo "    ✓ $label: $actual (snapshot: $snap, within ${tol}%)"
    return 0
  else
    echo "    ✗ $label: $actual (snapshot: $snap, $result)"
    return 1
  fi
}

# run_pulumi_test <snapshot_file> <project_dir> <project_name>
run_pulumi_test() {
  local snapshot_file="$1"
  local project_dir="$2"
  local name="$3"
  local ok=true

  echo ""
  echo "── pulumi: $name ──────────────────────────────────────"

  if [[ ! -d "$project_dir" ]]; then
    echo "  SKIP: project directory not found: $project_dir"
    ((SKIP++)) || true
    return
  fi

  # Run estimate, capture JSON on stdout, log on stderr.
  local actual_json
  if ! actual_json=$("$BINARY" pulumi estimate "$project_dir" --output json 2>/tmp/cloudcent-stderr.txt); then
    echo "  FAIL: cloudcent exited with error"
    cat /tmp/cloudcent-stderr.txt | sed 's/^/    /' >&2
    FAILURES+=("$name")
    ((FAIL++)) || true
    return
  fi

  # Validate JSON.
  if ! echo "$actual_json" | jq empty 2>/dev/null; then
    echo "  FAIL: output is not valid JSON"
    FAILURES+=("$name")
    ((FAIL++)) || true
    return
  fi

  local snap_json
  snap_json=$(cat "$snapshot_file")

  # 1. Resource count.
  local snap_count actual_count
  snap_count=$(echo "$snap_json"  | jq '.resources | length')
  actual_count=$(echo "$actual_json" | jq '.resources | length')
  if ! compare_field "resource_count" "$snap_count" "$actual_count"; then ok=false; fi

  # 2. Resource names (sorted, including sub_label for 1:N resources).
  local snap_names actual_names
  snap_names=$(echo "$snap_json"  | jq -r '[.resources[] | .name + (if .sub_label? and .sub_label != "" then "/" + .sub_label else "" end)] | sort | join(",")')
  actual_names=$(echo "$actual_json" | jq -r '[.resources[] | .name + (if .sub_label? and .sub_label != "" then "/" + .sub_label else "" end)] | sort | join(",")')
  if ! compare_field "resource_names" "$snap_names" "$actual_names"; then ok=false; fi

  # 3. Per-resource checks.
  local n_resources
  n_resources=$(echo "$snap_json" | jq '.resources | length')
  for i in $(seq 0 $((n_resources - 1))); do
    local res_name res_sub_label res_label snap_status actual_status snap_is_usage actual_selector
    res_name=$(echo "$snap_json" | jq -r ".resources[$i].name")
    res_sub_label=$(echo "$snap_json" | jq -r ".resources[$i].sub_label // \"\"")

    snap_status=$(echo "$snap_json"  | jq -r ".resources[$i].status // \"\"")
    snap_is_usage=$(echo "$snap_json" | jq -r ".resources[$i].is_usage_based // false")

    # Build a jq selector that disambiguates same-name resources by combining
    # name, sub_label, status presence, and is_usage_based.
    if [[ -n "$res_sub_label" ]]; then
      res_label="${res_name}/${res_sub_label}"
      actual_selector="first(.resources[] | select(.name == \"$res_name\" and (.sub_label // \"\") == \"$res_sub_label\"))"
    elif [[ -n "$snap_status" ]]; then
      # Status resource: match by name + same status value to disambiguate duplicates.
      res_label="$res_name"
      actual_selector="first(.resources[] | select(.name == \"$res_name\" and (.sub_label // \"\") == \"\" and (.status // \"\") == \"$snap_status\"))"
    elif [[ "$snap_is_usage" == "true" ]]; then
      # Usage-based resource with no sub_label: match by name + is_usage_based.
      res_label="$res_name"
      actual_selector="first(.resources[] | select(.name == \"$res_name\" and (.sub_label // \"\") == \"\" and (.is_usage_based // false) == true))"
    else
      res_label="$res_name"
      actual_selector="first(.resources[] | select(.name == \"$res_name\" and (.sub_label // \"\") == \"\"))"
    fi

    actual_status=$(echo "$actual_json" | jq -r "$actual_selector | .status // \"\"")

    # 3a. Status exact match.
    if ! compare_field "[$res_label] status" "$snap_status" "$actual_status"; then ok=false; fi

    # 3b. Price check — only when status is empty (i.e. resource has real pricing).
    if [[ -z "$snap_status" ]]; then
      local snap_rate actual_rate

      # For usage-based resources, compare unit_rate and cost_monthly.
      if [[ "$snap_is_usage" == "true" ]]; then
        snap_unit_rate=$(echo "$snap_json" | jq -r ".resources[$i].unit_rate // \"\"")
        if [[ -n "$snap_unit_rate" ]]; then
          actual_unit_rate=$(echo "$actual_json" | jq -r "$actual_selector | .unit_rate // \"\"")
          if ! compare_price "[$res_label] unit_rate" "$snap_unit_rate" "$actual_unit_rate" "$TOLERANCE"; then ok=false; fi
        fi
        snap_rate=$(echo "$snap_json"  | jq -r ".resources[$i].cost_monthly // \"0\"")
        actual_rate=$(echo "$actual_json" | jq -r "$actual_selector | .cost_monthly // \"0\"")
        if ! compare_price "[$res_label] cost_monthly" "$snap_rate" "$actual_rate" "$TOLERANCE"; then ok=false; fi
      else
        snap_rate=$(echo "$snap_json"  | jq -r ".resources[$i].on_demand_rate // \"0\"")
        actual_rate=$(echo "$actual_json" | jq -r "$actual_selector | .on_demand_rate // \"0\"")
        if ! compare_price "[$res_label] on_demand_rate" "$snap_rate" "$actual_rate" "$TOLERANCE"; then ok=false; fi
      fi
    fi
  done

  # 4. Totals.
  local snap_total actual_total
  snap_total=$(echo "$snap_json"  | jq -r '.totals.monthly_total // "0"')
  actual_total=$(echo "$actual_json" | jq -r '.totals.monthly_total // "0"')
  if ! compare_price "totals.monthly_total" "$snap_total" "$actual_total" "$TOLERANCE"; then ok=false; fi

  if $ok; then
    echo "  PASS"
    ((PASS++)) || true
  else
    echo "  FAIL"
    FAILURES+=("$name")
    ((FAIL++)) || true
  fi
}

# run_drawio_test <snapshot_file> <diagram_file> <name>
run_drawio_test() {
  local snapshot_file="$1"
  local diagram_file="$2"
  local name="$3"
  local ok=true

  echo ""
  echo "── drawio: $name ──────────────────────────────────────"

  if [[ ! -f "$diagram_file" ]]; then
    echo "  SKIP: diagram file not found: $diagram_file"
    ((SKIP++)) || true
    return
  fi

  local actual_json
  if ! actual_json=$("$BINARY" diagram estimate "$diagram_file" --output json 2>/tmp/cloudcent-stderr.txt); then
    echo "  FAIL: cloudcent exited with error"
    cat /tmp/cloudcent-stderr.txt | sed 's/^/    /' >&2
    FAILURES+=("$name")
    ((FAIL++)) || true
    return
  fi

  if ! echo "$actual_json" | jq empty 2>/dev/null; then
    echo "  FAIL: output is not valid JSON"
    FAILURES+=("$name")
    ((FAIL++)) || true
    return
  fi

  local snap_json
  snap_json=$(cat "$snapshot_file")

  local snap_count actual_count
  snap_count=$(echo "$snap_json"  | jq '.resources | length')
  actual_count=$(echo "$actual_json" | jq '.resources | length')
  if ! compare_field "resource_count" "$snap_count" "$actual_count"; then ok=false; fi

  local snap_total actual_total
  snap_total=$(echo "$snap_json"  | jq -r '.totals.monthly_total // "0"')
  actual_total=$(echo "$actual_json" | jq -r '.totals.monthly_total // "0"')
  if ! compare_price "totals.monthly_total" "$snap_total" "$actual_total" "$TOLERANCE"; then ok=false; fi

  if $ok; then
    echo "  PASS"
    ((PASS++)) || true
  else
    echo "  FAIL"
    FAILURES+=("$name")
    ((FAIL++)) || true
  fi
}

# ── run pulumi tests ─────────────────────────────────────────────────────────
echo "=== Pulumi tests ==="
if [[ ${#PULUMI_WHITELIST[@]} -eq 0 ]]; then
  echo "  (no projects in PULUMI_WHITELIST — add entries to run-tests.sh to enable)"
fi
for name in "${PULUMI_WHITELIST[@]+"${PULUMI_WHITELIST[@]}"}"; do
  snapshot_file="$SNAPSHOTS_PULUMI/${name}.json"
  if [[ ! -f "$snapshot_file" ]]; then
    echo ""
    echo "── pulumi: $name ──────────────────────────────────────"
    echo "  FAIL: snapshot not found at $snapshot_file"
    echo "  Generate it with:"
    echo "    cloudcent pulumi estimate $TESTDATA_PULUMI/$name --output json 2>/dev/null \\"
    echo "      | jq --sort-keys '.' > $snapshot_file"
    FAILURES+=("$name (missing snapshot)")
    ((FAIL++)) || true
    continue
  fi
  run_pulumi_test "$snapshot_file" "$TESTDATA_PULUMI/$name" "$name"
done

# ── run drawio tests ─────────────────────────────────────────────────────────
echo ""
echo "=== Drawio tests ==="
if [[ ${#DRAWIO_WHITELIST[@]} -eq 0 ]]; then
  echo "  (no diagrams in DRAWIO_WHITELIST — add entries to run-tests.sh to enable)"
fi
for name in "${DRAWIO_WHITELIST[@]+"${DRAWIO_WHITELIST[@]}"}"; do
  snapshot_file="$SNAPSHOTS_DRAWIO/${name}.json"
  if [[ ! -f "$snapshot_file" ]]; then
    echo ""
    echo "── drawio: $name ──────────────────────────────────────"
    echo "  FAIL: snapshot not found at $snapshot_file"
    FAILURES+=("$name (missing snapshot)")
    ((FAIL++)) || true
    continue
  fi
  # Find the matching .drawio file anywhere under testdata/drawio-diagrams.
  diagram_file=$(find "$TESTDATA_DRAWIO" -name "${name}.drawio" | head -1)
  run_drawio_test "$snapshot_file" "${diagram_file:-}" "$name"
done

# ── summary ──────────────────────────────────────────────────────────────────
TOTAL=$(( PASS + FAIL ))
echo ""
echo "════════════════════════════════════════"
echo "Results: ${PASS} passed, ${FAIL} failed, ${SKIP} skipped"
if [[ ${#FAILURES[@]} -gt 0 ]]; then
  echo "Failed:"
  for f in "${FAILURES[@]}"; do
    echo "  • $f"
  done
fi
echo "════════════════════════════════════════"

# Machine-readable output for CI badge generation.
# Written to a file so the workflow can pick it up without parsing stdout.
if [[ -n "${TEST_RESULTS_FILE:-}" ]]; then
  if [[ $FAIL -eq 0 ]]; then
    COLOR="brightgreen"
  elif [[ $PASS -eq 0 ]]; then
    COLOR="red"
  else
    COLOR="yellow"
  fi
  cat > "$TEST_RESULTS_FILE" <<JSON
{
  "schemaVersion": 1,
  "label": "integration tests",
  "message": "${PASS} / ${TOTAL} passed",
  "color": "${COLOR}"
}
JSON
fi

[[ $FAIL -eq 0 ]]
