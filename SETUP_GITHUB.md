# GitHub Setup Instructions

This guide will help you publish DUPMAN to GitHub as a new, independent repository.

## Pre-upload Checklist

✅ **Documentation Created:**
- [x] README.md - Comprehensive project overview
- [x] LICENSE - GPL-3.0 (consistent with Macondo)
- [x] CONTRIBUTING.md - Contribution guidelines
- [x] CHANGELOG.md - Release notes and version history
- [x] docs/LEXICONS.md - Lexicon setup guide
- [x] .gitignore - Comprehensive ignore rules

✅ **Code Cleanup:**
- [x] Removed temporary files (*.log, *.pid, *.stderr, *.stdout)
- [x] Removed absolute paths from source code
- [x] Cleaned build artifacts
- [x] No sensitive information in code

✅ **Submodules Configured:**
- [x] Macondo (git submodule)
- [x] Wolges (git submodule)

## Step 1: Create GitHub Repository

1. Go to https://github.com/new
2. Repository name: `DUPMAN` (or `dupman`, `duplicate-tournament-manager`)
3. Description: "A modern web-based interface for the Macondo Scrabble engine with live analysis and Monte Carlo simulation"
4. Visibility: **Public** (to share with community)
5. **DO NOT** initialize with README, license, or .gitignore (we already have these)
6. Click **"Create repository"**

## Step 2: Prepare Local Repository

```bash
cd /Users/isaacfalconer/DB_sources/gaddag/DUPMAN

# Verify git status
git status

# Stage all files
git add .

# Create initial commit (if not already done)
git commit -m "Initial commit: DUPMAN v0.1.0

A comprehensive web-based interface for the Macondo Scrabble engine featuring:
- Interactive play vs bot (Hasty/Sim modes)
- Live analysis mode with manual rack definition
- Monte Carlo simulation for move evaluation
- Full Spanish (OSPS) support with digraphs
- GCG import/export
- Post-game analysis and turn navigation

Built collaboratively by Isaac Falconer and Claude (Anthropic).

Integrates:
- Macondo engine by César Del Solar (domino14)
- Optional Wolges engine by Andy Kurnia
"
```

## Step 3: Link to GitHub Remote

```bash
# Add GitHub remote (replace YOUR_USERNAME)
git remote add origin https://github.com/YOUR_USERNAME/DUPMAN.git

# Verify remote
git remote -v

# Push to GitHub
git push -u origin main
```

If you're using a different branch name (like `master`):
```bash
git push -u origin master
```

## Step 4: Initialize Submodules on GitHub

The repository uses git submodules for Macondo and Wolges. After pushing:

```bash
# Verify submodules are tracked
git submodule status

# If submodules aren't initialized, do:
git submodule update --init --recursive

# Push submodule references
git push --recurse-submodules=on-demand
```

## Step 5: Configure GitHub Repository Settings

### Topics (Tags)
Add relevant topics to help discovery:
- `scrabble`
- `macondo`
- `word-games`
- `scrabble-ai`
- `monte-carlo-simulation`
- `go`
- `golang`
- `javascript`
- `spanish`
- `osps`
- `game-analysis`

### About Section
Description: "Modern web UI for Macondo Scrabble engine with live analysis, Monte Carlo sim, and Spanish (OSPS) support"

Website: Leave blank or add deployment URL later

### Features to Enable
- ☑️ Issues
- ☑️ Discussions (for community Q&A)
- ☑️ Projects (optional, for roadmap)
- ☑️ Wiki (optional, for extended docs)

### Branch Protection (Optional)
For `main` branch:
- Require pull request reviews before merging
- Require status checks to pass (when CI is added)

## Step 6: Create Initial Release

### Tag the Release
```bash
# Create annotated tag
git tag -a v0.1.0 -m "DUPMAN v0.1.0 - Initial Public Release

Features:
- Interactive web UI for Macondo
- Live analysis mode
- Monte Carlo simulation
- Spanish (OSPS) support
- GCG import/export
- Post-game analysis

See CHANGELOG.md for full details."

# Push tag to GitHub
git push origin v0.1.0
```

### Create GitHub Release
1. Go to your repository on GitHub
2. Click **"Releases"** → **"Create a new release"**
3. Choose tag: `v0.1.0`
4. Release title: `DUPMAN v0.1.0 - Initial Public Release`
5. Description: Copy content from CHANGELOG.md
6. Click **"Publish release"**

## Step 7: Update README with Correct URLs

After creating the repository, update these placeholders in README.md:

```markdown
# In README.md, replace:
YOUR_USERNAME → your_actual_github_username

# Example:
https://github.com/YOUR_USERNAME/DUPMAN
↓
https://github.com/isaacfalconer/DUPMAN
```

Then commit and push:
```bash
git add README.md CHANGELOG.md
git commit -m "docs: update GitHub URLs"
git push
```

## Step 8: Announce to Community

### Notify Interested Parties
You mentioned that creators of Macondo, Quackle, and MAGPIE are interested. Consider:

1. **Open an Issue on Macondo repo** to announce DUPMAN
   ```
   Title: "New Macondo Web UI: DUPMAN"

   Hi @domino14,

   I've created a comprehensive web-based interface for Macondo called DUPMAN.
   It features live analysis, Monte Carlo simulation, and full Spanish (OSPS) support.

   Repository: https://github.com/YOUR_USERNAME/DUPMAN

   Would love feedback from the community!
   ```

2. **Share on Scrabble Discord/Forums** (if applicable)

3. **Submit to awesome-scrabble lists** (if they exist)

### Social Media
Consider sharing on:
- Twitter/X with #Scrabble hashtag
- Reddit r/scrabble
- Relevant Discord servers

## Step 9: Set Up GitHub Actions (Optional)

Create `.github/workflows/ci.yml` for automated testing:

```yaml
name: CI

on: [push, pull_request]

jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
        with:
          submodules: recursive

      - name: Set up Go
        uses: actions/setup-go@v4
        with:
          go-version: '1.24'

      - name: Build
        run: |
          cd duplicate-tournament-manager/backend
          go build ./cmd/server
          go build ./cmd/macondo-wrapper

      - name: Test
        run: |
          cd duplicate-tournament-manager/backend
          go test ./...
```

## Maintenance After Initial Upload

### Regular Updates
```bash
# Make changes, then:
git add .
git commit -m "type(scope): description"
git push
```

### Versioning
Follow semantic versioning for releases:
- `v0.x.x` - Pre-1.0 releases
- `v1.0.0` - First stable release
- `v1.1.0` - Minor features
- `v1.1.1` - Bug fixes

### Handling Issues
- Enable issue templates for bugs and features
- Respond to community feedback
- Tag issues appropriately (bug, enhancement, help wanted, good first issue)

## Notes on Lexicon Files

**IMPORTANT:** Do NOT commit lexicon files (.kwg, .klv2) to the repository unless you have explicit permission and they are freely distributable.

The `.gitignore` already excludes:
```
lexica/*.kwg
lexica/*.klv2
lexica/*.kad
```

Users should obtain lexicon files separately (see docs/LEXICONS.md).

## Final Checklist Before Push

- [ ] No sensitive information (API keys, passwords, personal paths)
- [ ] No large binary files (except intentional)
- [ ] All TODO comments addressed or documented
- [ ] README has correct GitHub URLs
- [ ] License file is present and correct
- [ ] Submodules are configured properly
- [ ] .gitignore is comprehensive
- [ ] No uncommitted changes (`git status` clean)

## Getting Help

If you encounter issues during setup:
1. Check GitHub's documentation: https://docs.github.com/
2. Verify git/submodule configuration
3. Ask Claude for help with specific errors

---

Good luck with the launch! 🚀
