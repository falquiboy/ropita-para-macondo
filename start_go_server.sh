#!/usr/bin/env bash
# Simple starter for the Duplicate Tournament Manager Go server
# Usage: ./start_go_server.sh [PORT]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$SCRIPT_DIR"

# Locate backend dir regardless of where this script is placed
if [ -d "$ROOT/duplicate-tournament-manager/backend" ]; then
  BACKEND_DIR="$ROOT/duplicate-tournament-manager/backend"
else
  # If the script lives under duplicate-tournament-manager/, adjust ROOT
  if [ -d "$SCRIPT_DIR/backend" ]; then
    ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
    BACKEND_DIR="$ROOT/duplicate-tournament-manager/backend"
  else
    echo "Could not find duplicate-tournament-manager/backend next to this script." >&2
    exit 1
  fi
fi

# Args: first non-flag is PORT; flags:
#   --watch / -w        : reinicio automático al detectar cambios
#   --fg                : correr en foreground (logs en stdout)
#   --validate-span     : validar la palabra principal contra el KWG en generación
#   --no-rack-collapse  : no colapsar dígrafos del rack (deja que el engine lo haga)
#   --sim-timeout-ms=N  : timeout de simulación del wrapper (por si se usa sim)
WATCH=0
FG=0
PORT=""
VALIDATE_SPAN=0
RACK_COLLAPSE=1
SIM_TO_MS=""
for arg in "$@"; do
  case "$arg" in
    --watch) WATCH=1 ;;
    -w) WATCH=1 ;;
    --fg) FG=1 ;;
    --validate-span) VALIDATE_SPAN=1 ;;
    --no-rack-collapse) RACK_COLLAPSE=0 ;;
    --sim-timeout-ms=*) SIM_TO_MS="${arg#*=}" ;;
    [0-9][0-9][0-9][0-9]) PORT="$arg" ;;
  esac
done
PORT="${PORT:-8090}"
# Enforce fixed port; kill any previous servers listening on it
if lsof -nPi :"$PORT" >/dev/null 2>&1; then
  echo "Freeing port $PORT …"
  # Kill known server process on that port
  PIDS=$(lsof -t -nPi :"$PORT" 2>/dev/null || true)
  for PID in $PIDS; do
    kill "$PID" 2>/dev/null || true
  done
  sleep 0.4
fi

# Prefer a system Go >= 1.24 if available; fall back to vendored Go
pick_go() {
  if command -v go >/dev/null 2>&1; then
    ver="$(go version 2>/dev/null | awk '{print $3}' | sed 's/go//')"
    maj="${ver%%.*}"; rest="${ver#*.}"; min="${rest%%.*}"
    if [ -n "$maj" ] && [ -n "$min" ] && [ "$maj" -ge 1 ] && [ "$min" -ge 24 ]; then
      echo "Using system Go $(go version)"; return 0
    fi
  fi
  if [ -f "$ROOT/tools/go/activate_go.sh" ]; then
    # shellcheck disable=SC1091
    source "$ROOT/tools/go/activate_go.sh" >/dev/null 2>&1 || true
    echo "Using vendored $(go version 2>/dev/null || echo go-not-found)"
  else
    echo "No Go toolchain found. Install Go from https://go.dev/dl/ (1.24+) or keep repo's tools/go." >&2
  fi
}
pick_go

# Keep all Go caches inside the repo to avoid permission prompts; prefer local toolchain if vendored
if go version 2>/dev/null | grep -q "go1.22."; then
  export GOTOOLCHAIN=local
else
  export GOTOOLCHAIN=auto
fi
export GOCACHE="$ROOT/.gocache"
export GOMODCACHE="$ROOT/.gomodcache"
mkdir -p "$GOCACHE" "$GOMODCACHE"

# Build/update macondo-wrapper automatically
WRAPPER="$BACKEND_DIR/bin/macondo-wrapper"
echo "Ensuring macondo-wrapper is built…"
(
  cd "$BACKEND_DIR"
  # Always rebuild to pick up mapping changes; it's fast enough
  go build -o "$WRAPPER" ./cmd/macondo-wrapper
) || { echo "Failed to build macondo-wrapper" >&2; exit 1; }

# Ensure FILE2017.kwg is accessible under DUPMAN/lexica if present at repo root
if [ -f "$ROOT/FILE2017.kwg" ] && [ ! -f "$ROOT/lexica/FILE2017.kwg" ]; then
  echo "Seeding lexica/FILE2017.kwg from repo root…"
  mkdir -p "$ROOT/lexica"
  cp "$ROOT/FILE2017.kwg" "$ROOT/lexica/FILE2017.kwg"
fi

export PORT
export ENGINE=macondo
export MACONDO_BIN="$WRAPPER"
export MACONDO_DATA_PATH="$ROOT/macondo/data"
# Point KLV2_DIR to lexica directory for Spanish leaves
export KLV2_DIR="$ROOT/lexica"
# Allow longer simulation requests (ms)
export MACONDO_SIM_TIMEOUT_MS=${MACONDO_SIM_TIMEOUT_MS:-60000}
# Optional wrapper tuning
export VALIDATE_SPAN
export RACK_COLLAPSE
if [ -n "$SIM_TO_MS" ]; then export MACONDO_SIM_TIMEOUT_MS="$SIM_TO_MS"; fi
# Debug flag to trace match plays (see internal/match/session.go)
export DEBUG_MATCH=${DEBUG_MATCH:-0}

# Build Wolges wrapper and set WOLGES_BIN if possible
if [ -d "$ROOT/wolges" ]; then
  if command -v cargo >/dev/null 2>&1; then
    echo "Ensuring wolges-wrapper is built…"
    (
      cd "$ROOT/wolges"
      cargo build --release --bin wolges-wrapper >/dev/null 2>&1 || cargo build --bin wolges-wrapper
    ) || echo "warning: could not build wolges-wrapper"
    if [ -x "$ROOT/wolges/target/release/wolges-wrapper" ]; then
      export WOLGES_BIN="$ROOT/wolges/target/release/wolges-wrapper"
    elif [ -x "$ROOT/wolges/target/debug/wolges-wrapper" ]; then
      export WOLGES_BIN="$ROOT/wolges/target/debug/wolges-wrapper"
    fi
  else
    echo "cargo not found; Hybrid/Wolges engines may not work until installed" >&2
  fi
fi

echo "Starting Duplicate Tournament Manager on http://localhost:$PORT ..."
cd "$BACKEND_DIR"

start_instance() {
  # Stop any previous instance we started
  if [ -f server.pid ] && kill -0 "$(cat server.pid)" >/dev/null 2>&1; then
    kill "$(cat server.pid)" >/dev/null 2>&1 || true
    sleep 0.3
  fi
  if [ "$FG" = "1" ]; then
    echo "Running server in foreground (logs to stdout/stderr)…"
    exec env PORT="$PORT" DEBUG_MATCH="$DEBUG_MATCH" go run -mod=mod ./cmd/server
  else
    nohup env PORT="$PORT" DEBUG_MATCH="$DEBUG_MATCH" go run -mod=mod ./cmd/server > server.stdout 2> server.stderr & echo $! > server.pid
  fi

  # Wait for health
  local tries=25 ok=0
  while [ $tries -gt 0 ]; do
    if command -v curl >/dev/null 2>&1; then
      code="$(curl -s -o /dev/null -w "%{http_code}" "http://localhost:$PORT/health" || true)"
      if [ "$code" = "200" ]; then ok=1; break; fi
    else
      (exec 3<> "/dev/tcp/127.0.0.1/$PORT") >/dev/null 2>&1 && { ok=1; break; }
    fi
    sleep 0.2; tries=$((tries-1))
  done
  if [ "$ok" = "1" ]; then
    echo "Server is up on http://localhost:$PORT (PID: $(cat server.pid))"
    echo "\nTips:"
    echo " - Crear partida vs‑bot: botón 'Nueva partida' en la UI"
    echo " - Abort: botón 'Abortar' (POST /matches/{id}/abort)"
    echo " - Post‑mortem: tras GAME_OVER, usa ⏮/◀/▶/⏭ y 'Generar' para ver jugadas (POST /moves)"
    echo " - Validación KWG en generación: VALIDATE_SPAN=$VALIDATE_SPAN (cámbialo con --validate-span)"
    echo " - Colapso de dígrafos de rack: RACK_COLLAPSE=$RACK_COLLAPSE (desactiva con --no-rack-collapse)"
  else
    echo "Server not healthy yet. See $BACKEND_DIR/server.stderr"
  fi
}

# Helper: robust directory hash across OSes
dir_hash() {
  local dir
  for dir in "$@"; do
    find "$dir" -type f \( -name '*.go' -o -name '*.html' -o -name '*.css' -o -name '*.js' \) -print0
  done | sort -z | xargs -0 shasum 2>/dev/null | shasum 2>/dev/null || \
  (for dir in "$@"; do find "$dir" -type f -name '*.go' -print; done | xargs cat | md5 2>/dev/null)
}

start_instance

if [ "$WATCH" = "1" ]; then
  echo "Watch mode enabled. Rebuilding and restarting on changes…"
  trap 'echo; echo "Stopping…"; if [ -f server.pid ]; then kill $(cat server.pid) 2>/dev/null || true; fi; exit 0' INT TERM
  last="$(dir_hash "$BACKEND_DIR" "$BACKEND_DIR/internal/api/static")"
  while true; do
    sleep 1
    cur="$(dir_hash "$BACKEND_DIR" "$BACKEND_DIR/internal/api/static")"
    if [ "${cur}" != "${last}" ]; then
      echo "Changes detected. Rebuilding wrapper and restarting server…"
      (
        cd "$BACKEND_DIR" && go build -o "$WRAPPER" ./cmd/macondo-wrapper
      ) || echo "warning: wrapper build failed"
      start_instance
      last="$cur"
    fi
  done
fi

echo
echo "Open your browser at: http://localhost:$PORT"
echo "If you need to reset in-memory state: http://localhost:$PORT/reset"
