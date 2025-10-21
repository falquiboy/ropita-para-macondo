# Contributing to DUPMAN

First off, thank you for considering contributing to DUPMAN! It's people like you that make DUPMAN such a great tool for the Scrabble community.

## Code of Conduct

This project and everyone participating in it is governed by our commitment to fostering an open and welcoming environment. We pledge to make participation in our project a harassment-free experience for everyone.

## How Can I Contribute?

### Reporting Bugs

Before creating bug reports, please check the existing issues to avoid duplicates. When you create a bug report, include as many details as possible:

- **Use a clear and descriptive title**
- **Describe the exact steps to reproduce the problem**
- **Provide specific examples** (game state, rack, board position, etc.)
- **Describe the behavior you observed and what you expected**
- **Include screenshots or GIFs** if applicable
- **Include server logs** (`duplicate-tournament-manager/backend/server.stderr`)
- **Include browser console logs** (F12 → Console tab)

**Template for Bug Reports:**

```markdown
**Description:**
A clear description of what the bug is.

**Steps to Reproduce:**
1. Start a new game in analysis mode
2. Define rack as "HELIONA"
3. Click "Actualizar atril"
4. See error

**Expected Behavior:**
The rack should be set to HELIONA.

**Actual Behavior:**
Error message: "tiles not available in bag"

**Environment:**
- OS: macOS 14.0
- Browser: Chrome 120
- DUPMAN commit: abc1234

**Logs:**
```
[SetRack] Player 1, desired rack: HELIONA...
```

### Suggesting Enhancements

Enhancement suggestions are tracked as GitHub issues. When creating an enhancement suggestion, include:

- **Use a clear and descriptive title**
- **Provide a detailed description** of the suggested enhancement
- **Explain why this enhancement would be useful** to most DUPMAN users
- **List some examples** of how it would work
- **Include mockups or sketches** if applicable

### Pull Requests

1. **Fork the repo** and create your branch from `main`
2. **Make your changes** following the coding conventions below
3. **Test your changes** thoroughly
4. **Update documentation** if needed
5. **Commit your changes** with clear commit messages
6. **Push to your fork** and submit a pull request

## Development Setup

```bash
# Clone your fork
git clone https://github.com/YOUR_USERNAME/DUPMAN.git
cd DUPMAN

# Initialize submodules
git submodule update --init --recursive

# Start development server with auto-reload
./start_go_server.sh --watch --fg

# Make your changes...

# Test your changes
./start_go_server.sh --fg
# Then manually test in browser
```

## Coding Conventions

### Go Code

- **Follow standard Go conventions**: Use `gofmt` and `go vet`
- **Comments**: Add comments for exported functions and complex logic
- **Error handling**: Always handle errors explicitly
- **Logging**: Use descriptive log messages with `log.Printf`
- **Naming**: Use camelCase for unexported, PascalCase for exported

```go
// Good
func (m *MatchHandlers) SetRack(w http.ResponseWriter, r *http.Request) {
    log.Printf("[SetRack] Player %d, desired rack: %s", p, desired)
    if err := s.Game.SetRackForOnly(p, desiredRack); err != nil {
        log.Printf("[SetRack] Error: %v", err)
        writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
        return
    }
}
```

### JavaScript Code

- **Use modern JS features**: const/let, arrow functions, template literals
- **Naming**: Use camelCase for variables and functions
- **Comments**: Add comments for complex UI logic
- **Console logging**: Use descriptive prefixes like `[anApplyRacks]`

```javascript
// Good
async function genTurnMovesSim() {
    const perspective = currentUnseenPerspective();
    const qs = new URLSearchParams({ mode: 'sim', threads: String(threads) });
    if (perspective) qs.set('player', perspective === 'onturn' ? 'onturn' : 'you');

    const r = await fetch(`/matches/${matchId}/moves?turn=${tnTurn}&${qs.toString()}`);
    const j = await r.json();

    log.Printf('[genTurnMovesSim] Generated %d moves', j.all.length);
}
```

### Commit Messages

Follow the conventional commits format:

```
type(scope): subject

body (optional)

footer (optional)
```

**Types:**
- `feat`: New feature
- `fix`: Bug fix
- `docs`: Documentation changes
- `style`: Code style changes (formatting, etc.)
- `refactor`: Code refactoring
- `test`: Adding or updating tests
- `chore`: Maintenance tasks

**Examples:**

```
feat(analysis): add support for manual rack definition

Allows users to define custom racks for any player in analysis mode.
This enables exploring hypothetical positions.

Closes #42
```

```
fix(rack): prevent opponent rack regeneration on SetRack

Previously, SetRack used SetRackFor which regenerated the opponent's
rack. Now uses SetRackForOnly to preserve opponent's rack.

Fixes #58
```

## Testing

Currently, DUPMAN relies on manual testing. When contributing:

1. **Test the happy path** - Verify your feature works as expected
2. **Test edge cases** - Empty racks, full board, endgame, etc.
3. **Test error handling** - Invalid input, network errors, etc.
4. **Test both modes** - vs-bot and analysis mode
5. **Test rack perspectives** - Player 0 and player 1

### Manual Test Checklist

```markdown
- [ ] Feature works in vs-bot mode
- [ ] Feature works in analysis mode
- [ ] Feature works when player 0 is on turn
- [ ] Feature works when player 1 is on turn
- [ ] Error messages are clear and helpful
- [ ] No console errors in browser
- [ ] No errors in server logs
- [ ] UI updates correctly
- [ ] Racks display correctly with dígrafos
```

## Documentation

When adding new features:

1. **Update README.md** with new features or API endpoints
2. **Add code comments** for complex logic
3. **Update API documentation** in README if endpoints change
4. **Add examples** for non-obvious usage

## Project Structure

Understanding the codebase:

```
duplicate-tournament-manager/backend/
├── cmd/
│   ├── server/          # HTTP server entry point
│   │   └── main.go      # Server initialization
│   └── macondo-wrapper/ # Macondo wrapper for tile mapping
│       └── main.go      # Engine wrapper
├── internal/
│   ├── api/
│   │   ├── match_handlers.go  # Main game logic
│   │   ├── handlers.go         # HTTP routing
│   │   └── static/
│   │       └── index.html      # Embedded UI (single file!)
│   └── match/
│       └── session.go          # Game session state
└── go.mod
```

**Key files to understand:**

- `match_handlers.go`: All game operations (play, analyze, rack setting)
- `index.html`: Entire frontend UI in one file
- `macondo-wrapper/main.go`: Handles Spanish dígrafo mapping

## Areas Needing Help

We especially welcome contributions in these areas:

1. **Testing**: Automated tests for backend and frontend
2. **UI/UX**: Mobile-responsive design, accessibility
3. **Performance**: Optimizing simulation speed
4. **Documentation**: Tutorials, videos, screenshots
5. **Localization**: Support for more languages
6. **Bug fixes**: Check the issues page

## Getting Help

- **GitHub Issues**: For bugs and feature requests at https://github.com/falquiboy/ropita-para-macondo/issues
- **GitHub Discussions**: For questions and general discussion at https://github.com/falquiboy/ropita-para-macondo/discussions
- **Code Comments**: Read inline documentation in the code

## Recognition

Contributors will be recognized in:
- The README.md contributors section
- Git commit history
- Release notes for significant contributions

Thank you for contributing to DUPMAN! 🎉
