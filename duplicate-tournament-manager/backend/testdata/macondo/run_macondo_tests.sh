#!/usr/bin/env bash
set -euo pipefail

BIN_DIR="$(cd "$(dirname "$0")/../../bin" && pwd)"
BIN="$BIN_DIR/macondo-wrapper"
REQ_DIR="$(cd "$(dirname "$0")" && pwd)"

if [[ ! -x "$BIN" ]]; then
  echo "error: macondo-wrapper not found at $BIN" >&2
  exit 1
fi

echo "Running Macondo wrapper tests (Spanish digraph mapping)…" >&2

run_case() {
  local name="$1" req="$2"
  echo "\n== $name ==" >&2
  "$BIN" < "$req" | tee "${REQ_DIR}/${name}.out.json" | jq '{best:.best, top5: (.all|sort_by(-.score)|.[0:5])}'
}

run_case round1 "$REQ_DIR/round1_req.json"
run_case round2 "$REQ_DIR/round2_req.json"

echo "\nInspect the JSON files for any 86-pt vertical plays (row≈4,col≈10)." >&2

