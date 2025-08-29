Duplicate Tournament Manager (Hybrid)

Overview
- Purpose: Manage Duplicate Scrabble tournaments and provide a vs‑bot (Macondo) play mode with proper Spanish rules (OSPS). Includes racks/rounds (duplicate), head‑to‑head vs bot (training/analysis), scoring, standings, and a minimal UI.
- Architecture: Go backend with Macondo engine; optional Wolges/WASM utilities. Static UI is embedded and served by the backend.

Spanish mode highlights
- Rules: OSPS with digraph tiles [CH]/[LL]/[RR], SINGLE/VOID challenge, end after 4 consecutive scoreless turns (2 por jugador).
- Input normalization: natural typing; blanks as lowercase; [CH]/[LL]/[RR] accepted; Ç/K/W mapped when needed.
- Unseen Tiles Mapping: sidebar shows unseen counts (bag + opponent rack), grouped by category with vowels/consonants/comodines summary.
- Endgame UX: winner, final score, cierre reason banner and modal.
- Post‑mortem: turn navigation via engine history (/events + /position).

Repo layout (this repo)
- `duplicate-tournament-manager/backend`: Go HTTP API + static UI (index.html).
- `lexica`: place Spanish lexica files; this repo uses `DUPMAN/lexica/FILE2017.kwg` and optional `DUPMAN/lexica/FILE2017.klv2`.
- `macondo`: Macondo data directory (used via `MACONDO_DATA_PATH`).

Quick start (recommended)
- Use the helper script which sets envs, builds the wrapper, frees the port, and starts the server:
  - `PORT=8090 DUPMAN/start_go_server.sh`
- Open `http://localhost:8090`. Health: `GET /health` → 200.

Manual start (alternative)
- From `DUPMAN/duplicate-tournament-manager/backend`:
  - Build: `go build -o server ./cmd/server && go build -o bin/macondo-wrapper ./cmd/macondo-wrapper`
  - Run: `PORT=8090 ENGINE=macondo MACONDO_BIN=$(pwd)/bin/macondo-wrapper MACONDO_DATA_PATH=$(pwd)/../../macondo/data KLV2_DIR=$(pwd)/../../lexica ./server`

Vs‑Bot endpoints (Macondo)
- `POST /matches` → create a match. Optional JSON: `{ "kwg": "path", "challenge": "single|void" }`
- `GET  /matches/{id}` → snapshot: `board_rows`, `bonus_rows`, `rack`, `bag`, `score`, `play_state`, `final_score`, `winner`.
- `POST /matches/{id}/play` → `{ word, row, col, dir }`
- `POST /matches/{id}/exchange` → `{ tiles }`
- `POST /matches/{id}/pass`
- `POST /matches/{id}/ai_move` → `{ mode: "static"|"sim", sim?: { iters, plies, topK } }`
- `GET  /matches/{id}/unseen` → unseen tiles: `bag_remaining`, `opp_rack`, `total_unseen`, `tiles: [[letter,count],...]`.
- `GET  /matches/{id}/scoresheet` → event rows (includes `PlayedTiles` when available).
- `GET  /matches/{id}/events` → compact engine events (`ply, player, type, row, col, dir, word`).
- `GET  /matches/{id}/position?turn=N` → snapshot at turn N + racks per player.
- `POST /matches/{id}/abort` → end match immediately (testing flow; present in some branches).

Duplicate mode (tournaments)
- Legacy MVP endpoints under `/tournaments/*` are kept for the Duplicate tournament flow (create tournament, players, rounds, submit, close); see code for details. New work focuses on vs‑bot.

UI features at a glance
- Board: arrow typing indicator (space toggles direction), last‑play highlight (only placed tiles), blanks shown in red lowercase.
- Rack: drag‑and‑drop reorder, selection for exchange, recall last/all.
- Tiles Mapping: right sidebar (`Tiles Mapping`), unseen counts; disables exchange in endgame.
- Endgame: banner+modal; board “Finalizada” badge.
- Post‑mortem: navigation ⏮ ◀ ▶ ⏭ with racks display after GAME_OVER.

Troubleshooting
- Ensure `MACONDO_DATA_PATH=DUPMAN/macondo/data` is set (the start script does this). Missing this causes `/matches` to fail.
- If a port is busy: the start script frees it automatically; otherwise `lsof -i :8090 -t | xargs kill`.
- If Tiles Mapping is empty: check `GET /matches/{id}/unseen` from the browser/network tab and server logs.

How to re‑establish context with the assistant
- After a new session or chat compaction, include:
  - Project/area: “We’re in DUPMAN (Duplicate Tournament Manager) at commit f0520d03 (or current branch).”
  - Paths: “Backend is at `DUPMAN/duplicate-tournament-manager/backend`, UI is embedded in `internal/api/static/index.html`.”
  - Run script: “Use `DUPMAN/start_go_server.sh` on port 8090.”
  - Engine/env: “ENGINE=macondo, MACONDO_BIN points to backend/bin/macondo-wrapper, MACONDO_DATA_PATH=DUPMAN/macondo/data, KLV2_DIR=DUPMAN/lexica.”
  - Scope/features: “Spanish OSPS rules with digraphs, vs‑bot endpoints (/matches, /unseen, /scoresheet, /events, /position), Tiles Mapping sidebar, endgame UX.”
  - Current goal/issue: describe exact task and any errors (UI console/server stderr snippets). 
- Short template you can paste:
  - Repo: DUPMAN
  - Branch/commit: <name/hash>
  - Start server: `PORT=8090 DUPMAN/start_go_server.sh`
  - Focus: Spanish vs‑bot UI, Tiles Mapping, history/turn nav
  - Problem: <what you see>
  - Desired outcome: <what to change>

Notes on lexica
- The repo expects Spanish FILE2017 assets under `DUPMAN/lexica/`:
  - `FILE2017.kwg` (required) and `FILE2017.klv2` (optional for leaves).
- The backend auto‑detects these when not explicitly provided in requests.
