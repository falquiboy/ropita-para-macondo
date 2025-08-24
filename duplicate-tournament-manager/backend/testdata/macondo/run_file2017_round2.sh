#!/usr/bin/env bash
set -euo pipefail

BACKEND_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="$BACKEND_DIR/bin/macondo-wrapper"
REQ="$BACKEND_DIR/testdata/macondo/file2017_round2_req.json"
ROOT_DTM="$(cd "$BACKEND_DIR/.." && pwd)"
ROOT_DUPMAN="$(cd "$ROOT_DTM/.." && pwd)"
export MACONDO_DATA_PATH="$ROOT_DUPMAN/macondo/data"

if [[ ! -x "$BIN" ]]; then
  echo "Building macondo-wrapper..." >&2
  (cd "$BACKEND_DIR" && go build -o "$BIN" ./cmd/macondo-wrapper)
fi

echo "Running FILE2017 round2 test (C?SERON with [CH]O[RR]I[LL]OS on board)..." >&2
OUT="$BACKEND_DIR/testdata/macondo/file2017_round2.out.json"
KWG_ABS="$BACKEND_DIR/lexica/gaddag/FILE2017.kwg"
if [[ ! -f "$KWG_ABS" ]]; then
  # fallback to repo root
  KWG_ABS="$(cd "$ROOT_DUPMAN" && pwd)/FILE2017.kwg"
fi
jq --arg kwg "$KWG_ABS" '.kwg=$kwg' "$REQ" | "$BIN" | tee "$OUT" | jq '{best:.best, sample:(.all|sort_by(-.score)|.[0:10])}'

echo "\n86-point vertical plays (if any):" >&2
jq -r '.all[] | select(.score==86 and (.dir|ascii_upcase)=="V") | "row=",.row,", col=",.col,", word=",.word' "$OUT" || true
