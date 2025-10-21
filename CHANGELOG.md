# Changelog

All notable changes to DUPMAN will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Initial Public Release - 2025-01-21

This represents the first public release of DUPMAN, a comprehensive web-based interface for the Macondo Scrabble engine.

#### Added

**Core Features:**
- Interactive web-based UI for playing Scrabble vs Macondo bot
- Live analysis mode for exploring hypothetical positions
- Manual rack definition for any player
- Monte Carlo simulation for move evaluation
- Static equity move generation
- GCG file import/export
- Post-game analysis with turn-by-turn navigation

**Spanish Language Support:**
- Full support for OSPS rules (Spanish Scrabble)
- Digraph tiles support: [CH], [LL], [RR]
- Automatic input normalization for Spanish characters
- Spanish-specific challenge rules (Single/Void)
- Endgame rules: 4 consecutive scoreless turns

**Analysis Features:**
- Free input mode: manually place any word on the board
- Perspective switching: view from player 0 or player 1 perspective
- Unseen tiles tracking (bag + opponent rack)
- Real-time tiles mapping with categorization
- Overwrite protection with explicit confirmation

**UI/UX:**
- Drag-and-drop rack tile management
- Arrow key typing with directional indicator
- Keyboard shortcuts (Space: toggle direction, Esc: recall tiles)
- Last play highlighting
- Board coordinate labels (Spanish: letters for rows, numbers for columns)
- Auto-fill rack input based on current player
- Bot logs streaming via Server-Sent Events

**Backend API:**
- RESTful API for all game operations
- Session management with in-memory state
- Rack manipulation endpoints (set, exchange)
- Move generation with configurable parameters (static/sim)
- Historical position retrieval
- Unseen tiles calculation with player perspective
- GCG export functionality

#### Fixed

**Critical Bug Fixes:**
- **Rack Preservation**: Fixed `SetRack` endpoint to preserve opponent's rack when defining manual racks
  - Previous behavior: `SetRackFor` regenerated opponent's rack randomly
  - New behavior: `SetRackForOnly` maintains opponent's rack unchanged

- **Player Perspective**: Fixed rack serialization to always show correct player on turn
  - Backend now consistently uses `PlayerOnTurn()` for rack field in both analysis and vs-bot modes

- **Simulation Rack Mismatch**: Fixed simulation using wrong rack after manual definition
  - Frontend now sends `player` parameter in simulation requests
  - `currentUnseenPerspective()` returns 'onturn' in analysis mode
  - Simulation now correctly uses manually-defined racks

- **Variable Declaration**: Fixed `tnSelPos` undefined error
  - Added missing variable declaration in global scope

- **Rack Availability**: Enhanced rack setting to handle tiles on board
  - Now returns both racks to bag before setting new rack
  - Restores opponent rack after setting desired rack
  - Handles cases where tiles are unavailable (on board)

**UI Improvements:**
- Fixed rack display showing correct player when bot is on turn
- Fixed move generation using correct player perspective
- Improved error messages for tile availability issues
- Better logging for debugging rack operations

#### Technical Improvements

**Code Quality:**
- Comprehensive logging for rack operations
- Detailed debug logs for move generation and simulation
- Clear separation between analysis and vs-bot modes
- Consistent error handling across endpoints

**Documentation:**
- Added comprehensive README.md
- Created CONTRIBUTING.md with development guidelines
- Added LEXICONS.md guide for lexicon setup
- Included code examples and API documentation

**Infrastructure:**
- Improved .gitignore for cleaner repository
- Removed hardcoded absolute paths
- GPL-3.0 license (consistent with Macondo)
- Proper project structure for GitHub

#### Known Issues

- **Dígrafo Tile Availability**: When only one L remains and it's on the board as an anchor, the system may not allow defining racks with L or [LL] due to tile availability checking logic
- **Analysis Mode State**: Manually set racks may be reset if game state changes significantly

#### Dependencies

- Go 1.24+
- Macondo engine (git submodule)
- Optional: Wolges engine (git submodule)
- Spanish lexicon files: FILE2017.kwg, FILE2017.klv2

#### Credits

This release represents extensive collaborative work between Isaac Falconer and Claude (Anthropic) to create a modern, user-friendly interface for the Macondo Scrabble engine.

Special thanks to:
- César Del Solar (domino14) for the Macondo engine
- Andy Kurnia for Wolges
- The FISE and Scrabble communities for Spanish lexicons

---

## Release Notes Format

Future releases will follow this format:

### [Version] - YYYY-MM-DD

#### Added
- New features

#### Changed
- Changes in existing functionality

#### Deprecated
- Soon-to-be removed features

#### Removed
- Removed features

#### Fixed
- Bug fixes

#### Security
- Security improvements

---

[Unreleased]: https://github.com/falquiboy/ropita-para-macondo/compare/v0.1.0...HEAD
