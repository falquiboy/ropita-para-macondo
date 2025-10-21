# Lexicon Setup Guide

DUPMAN requires lexicon files (GADDAG word lists) to function. These files contain the valid words for your chosen language/ruleset.

## Required Files

For **Spanish (OSPS)** gameplay, you need:

1. **`FILE2017.kwg`** (required) - The GADDAG word list
2. **`FILE2017.klv2`** (optional) - Leave values for better static equity calculations

## Where to Place Lexicon Files

Place your lexicon files in the `lexica/` directory:

```
DUPMAN/
└── lexica/
    ├── FILE2017.kwg
    └── FILE2017.klv2  (optional)
```

The startup script (`start_go_server.sh`) automatically sets `KLV2_DIR` to point to this directory.

## Obtaining Lexicon Files

### Option 1: Build from Source (Recommended)

If you have the original lexicon data, you can build KWG files using tools from the Macondo project:

```bash
# Using Macondo's lexicon tools
cd macondo
make lexica  # or follow Macondo's lexicon building instructions
```

### Option 2: Use Existing Files

If you already have `.kwg` and `.klv2` files:

1. Copy them to `DUPMAN/lexica/`
2. Ensure they match your desired ruleset (e.g., FILE2017 for Spanish OSPS)

### Option 3: Download Pre-built Files

**Note:** Lexicon files may be subject to copyright. Ensure you have the right to use them.

Some sources for Spanish Scrabble lexicons:
- [Federación Internacional de Scrabble en Español (FISE)](https://www.fise.org/)
- Community-shared lexicons (check licensing)

## Supported Lexicons

DUPMAN is designed for Spanish (OSPS) but can work with other lexicons:

- **Spanish OSPS**: `FILE2017.kwg` / `FILE2017.klv2`
- **Spanish FISE2016**: `FISE2016_converted.kwg`
- **English**: Place English `.kwg` files in `lexica/` and select in UI
- **Other languages**: Any Macondo-compatible `.kwg` file

## Verifying Your Setup

After placing lexicon files:

```bash
# Check files exist
ls -lh lexica/*.kwg lexica/*.klv2

# Start the server
./start_go_server.sh 8090

# Create a match - if it works, lexicons are configured correctly
curl -X POST http://localhost:8090/matches
```

If you see errors like "lexicon not found", verify:
1. File exists at `lexica/FILE2017.kwg`
2. File permissions allow reading
3. `KLV2_DIR` environment variable is set correctly

## Configuring Lexicons in the UI

The UI provides dropdown presets for common lexicon locations:

- **— Auto —**: Server auto-detects from `lexica/` directory
- **FILE2017.kwg (repo root)**: `DUPMAN/FILE2017.kwg`
- **FILE2017.kwg (DUPMAN/lexica)**: `DUPMAN/lexica/FILE2017.kwg` (recommended)
- **FISE2016_converted.kwg**: Alternative Spanish lexicon

You can also manually enter absolute paths in the KWG input field.

## Building Your Own Lexicons

To build `.kwg` files from word lists:

### Using Macondo's `makegaddag` tool:

```bash
cd macondo
go build ./cmd/makegaddag

# Build from a text word list
./makegaddag -input words.txt -output custom.kwg -lexicon-name CUSTOM
```

### Using Wolges (Rust alternative):

```bash
cd wolges
cargo build --release --bin build-kwg

./target/release/build-kwg --input words.txt --output custom.kwg
```

## Generating Leave Files (.klv2)

Leave files improve static equity calculations. To generate:

```bash
cd macondo

# Generate leaves from game data
go run ./cmd/genleaves -lexicon FILE2017 -output ../lexica/FILE2017.klv2
```

## Troubleshooting

### "Lexicon not found" Error

**Problem:** Server can't find the `.kwg` file

**Solutions:**
1. Verify file exists: `ls lexica/FILE2017.kwg`
2. Check file permissions: `chmod 644 lexica/FILE2017.kwg`
3. Verify `KLV2_DIR` is set: `echo $KLV2_DIR` (should be `/path/to/DUPMAN/lexica`)
4. Use absolute path in UI as fallback

### "Invalid lexicon format" Error

**Problem:** File is corrupted or wrong format

**Solutions:**
1. Re-download or rebuild the `.kwg` file
2. Verify it's a valid GADDAG file (not plain text word list)
3. Check file size (should be several MB for complete lexicon)

### Slow Move Generation

**Problem:** Move generation takes too long

**Solutions:**
1. Ensure you have the `.klv2` leaves file
2. Use SSD storage for lexicon files
3. Keep lexicons in the `lexica/` directory (not network drives)

## Advanced: Custom Rulesets

To use custom rulesets with special tiles (like Spanish dígrafos):

1. Build lexicon with dígrafo support
2. Configure tile mapping in `macondo/data/`
3. Set `MACONDO_DATA_PATH` appropriately
4. Update board configuration if needed

See Macondo documentation for details on custom rulesets.

## File Sizes Reference

Typical lexicon file sizes:

- **FILE2017.kwg**: ~4-8 MB
- **FILE2017.klv2**: ~2-4 MB
- **FISE2016_converted.kwg**: ~5-10 MB

If your files are significantly smaller, they may be incomplete.

## License and Attribution

Lexicon files have their own licenses, separate from DUPMAN:

- **FISE lexicons**: Check with FISE for usage rights
- **Community lexicons**: Respect original creator's license
- **Personal use**: Generally permitted; check for commercial use

Always attribute lexicon sources appropriately.

---

For more help, see:
- [Macondo Lexicon Documentation](https://github.com/domino14/macondo#lexica)
- [Ropita para Macondo Issues](https://github.com/falquiboy/ropita-para-macondo/issues)
