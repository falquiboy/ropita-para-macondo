Macondo GUI (Prototype)

Goal
- Web UI to play against the Macondo engine. This prototype provides a minimal static UI and a stub API so you can iterate on UX immediately. Replace the stub with a Go service that calls Macondo for real move generation/validation.

Structure
- static/index.html: Board + rack UI (no bundler).
- devserver.py: Python dev server with in‑memory game state and stubbed engine moves.

Run (dev)
- PORT=8082 python3 macondo-gui/devserver.py
- Open http://localhost:8082

Replace stub with Go+Macondo
- Create a Go microservice exposing:
  - POST /api/game/new { ruleset, lexicon_path } → { game_id, state }
  - POST /api/game/{id}/play { word, row, col, dir } → validates via Macondo, updates state
  - POST /api/game/{id}/engine → calls Macondo (best move), applies update
  - GET  /api/game/{id} → current state (board, racks, scores, turn, history)
- Point the frontend fetch URLs to /api/... and remove the dev server routes.

