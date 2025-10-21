# Ropita para Macondo 🧥

**A modern web-based Scrabble analysis and play interface powered by Macondo**

_Also known as DUPMAN (Duplicate Tournament Manager)_

[![License: GPL v3](https://img.shields.io/badge/License-GPLv3-blue.svg)](https://www.gnu.org/licenses/gpl-3.0)

## Overview

DUPMAN (Duplicate Tournament Manager) is a comprehensive web-based interface for Scrabble game analysis and play, built on top of the [Macondo](https://github.com/domino14/macondo) engine. It provides an intuitive, feature-rich UI for playing against the bot, analyzing positions, and exploring move possibilities using both static equity calculations and Monte Carlo simulations.

### Key Features

- **🎮 Play vs Bot**: Interactive play against Macondo with multiple difficulty levels (Hasty, Sim)
- **📊 Live Analysis Mode**: Define custom board positions and racks to analyze any game state
- **🎲 Monte Carlo Simulation**: Run simulations to evaluate move strength by win probability
- **🔍 Move Generation**: Generate and explore top moves with detailed equity and leave values
- **📈 Tiles Tracking**: Real-time tracking of unseen tiles (bag + opponent rack)
- **🌍 Multi-language Support**: Full support for Spanish (OSPS rules with digraphs [CH], [LL], [RR])
- **💾 GCG Import/Export**: Load and save games in standard GCG format
- **⏱️ Post-game Analysis**: Navigate through game history with turn-by-turn position review
- **🎯 Endgame Solver**: Perfect endgame and pre-endgame algorithms for optimal play

### What Makes It Unique

- **Manual Rack Definition**: Define any rack for any player to explore hypothetical positions
- **Perspective Switching**: View the game from either player's perspective, including bot's rack
- **Free Input Mode**: Manually place any word on the board with flexible rack assignment
- **Robust Digraph Support**: Seamless handling of Spanish digraphs (CH, LL, RR) in input and display
- **Context-Aware Analysis**: Simulations always use the correct rack for the current player on turn
- **Live Overwrite**: Accept and apply invalid moves with explicit confirmation in analysis mode

## Screenshots

*[Screenshots to be added: game board, analysis mode, move list, tiles tracking]*

## Quick Start

### Prerequisites

- **Go 1.24+** (or use the vendored Go toolchain included in `tools/go/`)
- **Git** with submodules support
- Optional: **Rust/Cargo** for Wolges hybrid engine support

### Installation

```bash
# Clone the repository with submodules
git clone --recursive https://github.com/falquiboy/ropita-para-macondo.git
cd ropita-para-macondo

# Start the server (handles all setup automatically)
./start_go_server.sh 8090

# Open your browser
open http://localhost:8090
```

The start script will automatically:
- Build the Macondo wrapper
- Set up required environment variables
- Free the port if needed
- Start the server

### Manual Setup (Advanced)

```bash
cd duplicate-tournament-manager/backend

# Build binaries
go build -o server ./cmd/server
go build -o bin/macondo-wrapper ./cmd/macondo-wrapper

# Set environment variables
export PORT=8090
export ENGINE=macondo
export MACONDO_BIN=$(pwd)/bin/macondo-wrapper
export MACONDO_DATA_PATH=$(pwd)/../../macondo/data
export KLV2_DIR=$(pwd)/../../lexica

# Run the server
./server
```

## Usage

### Playing vs Bot

1. Click **"Nueva partida (Sim)"** to start a game with Monte Carlo AI
2. Type words on the board using arrow keys (space toggles direction)
3. Drag and drop tiles from your rack to reorder
4. Click **"Jugar"** to submit your move
5. Click **"AI mueve"** for the bot to make its move

### Live Analysis Mode

1. Click **"Nueva partida (Análisis)"** to start in analysis mode
2. Use **"Atril en turno"** input to define custom racks
3. Click **"Actualizar atril"** to apply the rack
4. Generated moves appear automatically
5. Click **"Simular"** to run Monte Carlo simulation on the top moves
6. Navigate with arrow keys or click moves to preview them on the board

### Free Input Mode

1. Enable **"Input libre"** checkbox
2. Type any word directly on the board
3. Optionally provide the exact tiles used in brackets
4. Accept invalid moves with explicit confirmation

### GCG Import

1. Click **"Cargar GCG"** button
2. Select a .gcg file from your computer
3. The game loads with full history
4. Use navigation controls to review moves

## Architecture

```
DUPMAN/
├── duplicate-tournament-manager/
│   └── backend/
│       ├── cmd/
│       │   ├── server/          # Main HTTP server
│       │   └── macondo-wrapper/ # Macondo engine wrapper
│       └── internal/
│           ├── api/            # HTTP handlers and routing
│           │   └── static/     # Embedded web UI (index.html)
│           └── match/          # Game session management
├── macondo/                    # Macondo engine (git submodule)
├── wolges/                     # Wolges engine (git submodule)
├── lexica/                     # Lexicon files (.kwg, .klv2)
└── start_go_server.sh         # Convenience startup script
```

### Technology Stack

- **Backend**: Go 1.24+
- **Engine**: Macondo (Go), optional Wolges (Rust)
- **Frontend**: Vanilla JavaScript + HTML/CSS
- **Communication**: REST API + Server-Sent Events (for bot logs)

## API Endpoints

### Match Management

- `POST /matches` - Create a new match
- `GET /matches/{id}` - Get match state
- `POST /matches/{id}/abort` - Abort current match

### Gameplay

- `POST /matches/{id}/play` - Make a move
- `POST /matches/{id}/exchange` - Exchange tiles
- `POST /matches/{id}/pass` - Pass turn
- `POST /matches/{id}/ai_move` - Request AI move

### Analysis

- `POST /matches/{id}/rack` - Set custom rack for a player
- `GET /matches/{id}/moves?turn=N&mode=static` - Generate static equity moves
- `GET /matches/{id}/moves?turn=N&mode=sim` - Run Monte Carlo simulation
- `GET /matches/{id}/position?turn=N` - Get board position at turn N
- `GET /matches/{id}/events` - Get game event history

### Information

- `GET /matches/{id}/unseen` - Get unseen tiles (bag + opponent rack)
- `GET /matches/{id}/scoresheet` - Get detailed score sheet
- `GET /health` - Server health check

## Configuration

### Environment Variables

- `PORT` - Server port (default: 8090)
- `ENGINE` - Engine to use: "macondo" or "wolges"
- `MACONDO_BIN` - Path to macondo-wrapper binary
- `MACONDO_DATA_PATH` - Path to macondo/data directory
- `KLV2_DIR` - Path to lexica directory for leaves files
- `DEBUG_MATCH` - Enable match debugging logs (0 or 1)
- `MACONDO_SIM_TIMEOUT_MS` - Simulation timeout in milliseconds (default: 60000)

### Lexicon Files

Place your lexicon files in the `lexica/` directory:

- `FILE2017.kwg` - Spanish GADDAG (required)
- `FILE2017.klv2` - Spanish leaves (optional, improves static evaluation)

## Development

### Running with Auto-reload

```bash
./start_go_server.sh --watch
```

### Running in Foreground (see logs)

```bash
./start_go_server.sh --fg
```

### Building for Production

```bash
cd duplicate-tournament-manager/backend
go build -o server ./cmd/server
go build -o bin/macondo-wrapper ./cmd/macondo-wrapper
```

## Known Issues and Limitations

- **Dígrafo Handling**: When only one L remains unseen and it's on the board as an anchor, the system may not allow defining racks with L or [LL]. This is a constraint of the tile availability checking logic.
- **Analysis Mode State**: Manually set racks are preserved during analysis, but may be reset if the game state changes significantly.

## Contributing

We welcome contributions from the Scrabble and open-source communities! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

### Areas for Contribution

- UI/UX improvements
- Additional language support
- Performance optimizations
- Bug fixes and testing
- Documentation improvements

## Acknowledgments

This project builds upon the excellent work of:

- **[Macondo](https://github.com/domino14/macondo)** by César Del Solar (domino14) - The powerful Scrabble engine that powers this interface
- **[Wolges](https://github.com/andy-k/wolges)** by Andy Kurnia - Alternative high-performance engine
- **[Quackle](https://quackle.org/)** - Inspiration for analysis features
- **[MAGPIE](https://github.com/domino14/macondo-magpie)** - The community that drives Scrabble AI development

Special thanks to the creators and maintainers of these projects for their dedication to open-source Scrabble tools.

## License

This project is licensed under the GNU General Public License v3.0 - see the [LICENSE](LICENSE) file for details.

This is consistent with [Macondo's license](https://github.com/domino14/macondo/blob/master/LICENSE.md) (GPL-3.0), as DUPMAN is a derivative work that integrates and extends Macondo.

## Contact

- **Issues**: Please report bugs and feature requests via [GitHub Issues](https://github.com/falquiboy/ropita-para-macondo/issues)
- **Discussions**: Join the conversation in [GitHub Discussions](https://github.com/falquiboy/ropita-para-macondo/discussions)

## Roadmap

- [ ] Tournament management features (duplicate mode)
- [ ] Multi-player online support
- [ ] Mobile-responsive UI
- [ ] Docker containerization
- [ ] Automated testing suite
- [ ] CI/CD pipeline
- [ ] Additional lexicon support (English, French, etc.)
- [ ] Opening book integration
- [ ] Game database and statistics

---

Made with ❤️ for the Scrabble community
