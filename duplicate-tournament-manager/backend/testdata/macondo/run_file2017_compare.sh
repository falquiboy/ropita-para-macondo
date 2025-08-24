#!/usr/bin/env bash
set -euo pipefail

BACKEND_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="$BACKEND_DIR/bin/macondo-wrapper"
ROOT_DTM="$(cd "$BACKEND_DIR/.." && pwd)"
ROOT_DUPMAN="$(cd "$ROOT_DTM/.." && pwd)"
export MACONDO_DATA_PATH="$ROOT_DUPMAN/macondo/data"

R1="$BACKEND_DIR/testdata/macondo/file2017_round1_req.json"
R2="$BACKEND_DIR/testdata/macondo/file2017_round2_req.json"
OUTDIR="$BACKEND_DIR/testdata/macondo/out"
mkdir -p "$OUTDIR"

if [[ ! -x "$BIN" ]]; then
  echo "Building macondo-wrapper..." >&2
  (cd "$BACKEND_DIR" && go build -o "$BIN" ./cmd/macondo-wrapper)
fi

KWG_ABS="$BACKEND_DIR/lexica/gaddag/FILE2017.kwg"
if [[ ! -f "$KWG_ABS" ]]; then
  KWG_ABS="$(cd "$ROOT_DUPMAN" && pwd)/FILE2017.kwg"
fi

run_case(){
  local name="$1" req="$2" collapse="$3"
  echo "\n== $name (RACK_COLLAPSE=$collapse) ==" >&2
  jq --arg kwg "$KWG_ABS" '.kwg=$kwg' "$req" | RACK_COLLAPSE="$collapse" "$BIN" | tee "$OUTDIR/${name}_collapse${collapse}.json" | jq '{best:.best, top10:(.all|sort_by(-.score)|.[0:10])}'
}

echo "Running FILE2017 comparisons (collapse on/off)..." >&2
run_case round1 "$R1" 1
run_case round1 "$R1" 0
run_case round2 "$R2" 1
run_case round2 "$R2" 0

echo "\nExtract vertical 86-pt plays (if any):" >&2
for f in "$OUTDIR"/round2_collapse*.json; do
  echo "-- $(basename "$f")" >&2
  jq -r '.all[] | select(.score==86 and (.dir|ascii_upcase)=="V") | "row=",.row,", col=",.col,", word=",.word' "$f" || true
done
