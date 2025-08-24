Duplicate Tournament Manager (Hybrid)

Overview
- Purpose: Manage official Duplicate Scrabble tournaments (in‑person), with rounds, shared racks, master move selection, player submissions, scoring, standings, and projector view.
- Architecture: Hybrid. Go backend with Macondo for move generation; optional Wolges/WASM utilities on the client. Minimal static UI served by the backend.

Repositories to use
- Macondo (engine, Go): generates all legal moves, scores, and best move. Integrate later.
- Wolges (Rust): build ES lexicon (KWG/KLV2) from your word list, for engine use and optional client utilities.
- wolges-wasm (optional, client): dictionary utilities in the browser.

Project layout (this repo)
- backend/: Go HTTP API (in‑memory for now) + static file server for UI.
- frontend/: minimal UI (index.html + app.js), no bundler required.
- lexicon/: place your `es.kwg` (and optional `es.klv2`) here.

Note: In this repo layout the Spanish FILE2017 assets live under `DUPMAN/lexica/`:
- `DUPMAN/lexica/FILE2017.kwg`
- `DUPMAN/lexica/FILE2017.klv2`

Both the Go server and the Python dev server auto-detect those files if you don’t pass `lexicon_path`.

Quick start (MVP without Macondo)
1) Requirements: Go 1.21+, make (optional). Rust and Node are not required for the MVP.
2) Build & run:
   - cd backend
   - go mod tidy
   - go run ./cmd/server
3) Open http://localhost:8080
4) Health check: curl -s http://localhost:8080/health

Dev server using real Macondo (simple path)
- Build the wrapper once:
  - cd backend && source ../../tools/go/activate_go.sh && go mod tidy && go build -o bin/macondo-wrapper ./cmd/macondo-wrapper
- Run the Python dev server with the wrapper:
  - MACONDO_WRAPPER=/absolute/path/to/duplicate-tournament-manager/backend/bin/macondo-wrapper \\
    PORT=8081 python3 duplicate-tournament-manager/devserver.py
- Open http://localhost:8081 and use the UI; “Close Round” will use Macondo to choose and apply the master move.

Core API (MVP stub)
- POST /tournaments { name, ruleset, lexicon_path }
- GET  /tournaments/{tid}/ → tournament JSON (includes board_rows)
- POST /tournaments/{tid}/players { name }
- POST /tournaments/{tid}/rounds { rack, deadline_at? }
- POST /tournaments/{tid}/rounds/{num}/submit { player_id, move }
- POST /tournaments/{tid}/rounds/{num}/close  → computes master (stub) and scores
- GET  /tournaments/{tid}/standings
- POST /moves { board, rack, kwg, ruleset } → stub; replace with Macondo
- GET  /health → 200

Sample sequence (curl)
1) Create a tournament:
   curl -s -X POST http://localhost:8080/tournaments \
     -H 'Content-Type: application/json' \
     -d '{"name":"Torneo Test","ruleset":"FISE-ES","lexicon_path":"./lexicon/es.kwg"}' | jq

2) Add two players (replace TID):
   curl -s -X POST http://localhost:8080/tournaments/TID/players -H 'Content-Type: application/json' -d '{"name":"Ana"}' | jq
   curl -s -X POST http://localhost:8080/tournaments/TID/players -H 'Content-Type: application/json' -d '{"name":"Luis"}' | jq

3) Start round 1:
   curl -s -X POST http://localhost:8080/tournaments/TID/rounds -H 'Content-Type: application/json' -d '{"rack":"AEIRST?"}' | jq

4) Players submit:
   curl -s -X POST http://localhost:8080/tournaments/TID/rounds/1/submit -H 'Content-Type: application/json' -d '{"player_id":"PID_Ana","move":{"word":"ARIEST","row":7,"col":8,"dir":"H"}}' | jq
   curl -s -X POST http://localhost:8080/tournaments/TID/rounds/1/submit -H 'Content-Type: application/json' -d '{"player_id":"PID_Luis","move":{"word":"SATIRE","row":8,"col":8,"dir":"H"}}' | jq

5) Close round 1 (stub picks highest player score; integrate Macondo next):
   curl -s -X POST http://localhost:8080/tournaments/TID/rounds/1/close | jq

6) Standings:
   curl -s http://localhost:8080/tournaments/TID/standings | jq

Integrating Macondo (replace stub)
1) Install Macondo per its README.
2) This scaffold includes `internal/engine/macondo.go` which shells out to an external binary.
   - Provide a wrapper that accepts JSON `MovesRequest` on stdin and returns `MovesResponse` on stdout.
   - Configure via env:
     - `ENGINE=macondo`
     - `MACONDO_BIN=/absolute/path/to/macondo-wrapper`
     - `MACONDO_TIMEOUT_MS=5000` (optional)
3) Start the server with the engine enabled:
   - `ENGINE=macondo MACONDO_BIN=/path/to/wrapper KLV2_DIR=../../lexica PORT=8081 go run ./cmd/server`
4) The `/moves` endpoint will call your wrapper; wire the same in round-closing logic when ready.

Board input (engine wrapper)
- The wrapper accepts a board grid as 15 strings with 15 characters each:
  - Use space `' '` for empty squares (`.` is accepted and normalized to space).
  - Example:
    {
      "board": {"rows": [
        "               ",
        "   CAT         ",
        "               ",
        "               ",
        "               ",
        "               ",
        "               ",
        "               ",
        "               ",
        "               ",
        "               ",
        "               ",
        "               ",
        "               ",
        "               "
      ]},
      "rack":"AEIRST?",
      "ruleset":"NWL23"
    }

Round closure now uses the engine
- When you POST `/tournaments/{tid}/rounds/{num}/close`, the server:
  - Calls the configured engine with the tournament's current board and the round's rack.
  - Picks the best move from the engine response as the master move.
  - Applies the master move to the tournament board (only fills empty squares; cross letters are preserved).

Building ES lexicon with Wolges (offline)
1) Prepare `ES_WORDLIST.txt` (una palabra por línea, tu norma FISE).
2) In wolges repo:
   cargo run --release --bin buildlex -- english-kwg  ES_WORDLIST.txt  es.kwg
   cargo run --release --bin buildlex -- english-klv2 ES_LEAVES.csv     es.klv2
3) Copy `es.kwg` to `DUPMAN/lexica/` and optionally `es.klv2` as well.

Next steps
- Swap stub Engine for Macondo.
- Add SQLite/Postgres persistence (see models in code for tables).
- Add WebSocket channel for timer/broadcast.
- Flesh out UI (projector view, player panel, referee console).
