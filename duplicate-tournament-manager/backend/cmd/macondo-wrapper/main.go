package main

import (
    "encoding/json"
    "fmt"
    "log"
    "os"
    "path/filepath"
    "strings"
    "io"
    "math"

    dapi "dupman/backend/internal/api"

    "github.com/domino14/macondo/board"
    "github.com/domino14/macondo/config"
    "github.com/domino14/macondo/cross_set"
    "github.com/domino14/macondo/game"
    "github.com/domino14/macondo/move"
    "github.com/domino14/macondo/movegen"
    pb "github.com/domino14/macondo/gen/api/proto/macondo"
    "github.com/domino14/macondo/equity"
    "github.com/domino14/macondo/montecarlo"

    wglkwg "github.com/domino14/word-golib/kwg"
    "github.com/domino14/word-golib/tilemapping"
)

// tokenizeRow splits a row string into 15 cell tokens, honoring [..] blocks.
func tokenizeRow(s string) []string {
    out := make([]string, 0, 15)
    rs := []rune(s)
    for i := 0; i < len(rs) && len(out) < 15; {
        ch := rs[i]
        if ch == '[' {
            j := i + 1
            for j < len(rs) && rs[j] != ']' { j++ }
            if j < len(rs) && rs[j] == ']' {
                out = append(out, string(rs[i:j+1]))
                i = j + 1
                continue
            }
        }
        out = append(out, string(ch))
        i++
    }
    for len(out) < 15 { out = append(out, " ") }
    return out
}

// normalizeBracketToken uppercases inside [...] so [ch]/[ll]/[rr] become
// [CH]/[LL]/[RR]. Macondo's Spanish tilemapping expects uppercase bracket
// tokens on the board; blanks on existing tiles do not affect scoring.
func normalizeBracketToken(tk string) string {
    if len(tk) >= 4 && strings.HasPrefix(tk, "[") && strings.HasSuffix(tk, "]") {
        inner := tk[1:len(tk)-1]
        return "[" + strings.ToUpper(inner) + "]"
    }
    return tk
}

// rackToBracket converts natural Spanish digraphs in a rack into
// bracket-coded tokens understood by Macondo's Spanish tile mapping.
// e.g. CH->"[CH]", LL->"[LL]", RR->"[RR]". Preserva '?' y demás letras.
func rackToBracket(rack string) string {
    s := strings.TrimSpace(rack)
    // Replace in order to avoid overlapping issues (use uppercase for matching)
    // Work rune-wise with a small state machine
    rs := []rune(s)
    var out []rune
    for i := 0; i < len(rs); i++ {
        // Peek next for digraphs
        if i+1 < len(rs) {
            a := rs[i]
            b := rs[i+1]
            au, bu := a, b
            if au >= 'a' && au <= 'z' { au = au - 32 }
            if bu >= 'a' && bu <= 'z' { bu = bu - 32 }
            if au == 'C' && bu == 'H' { out = append(out, '[', 'C', 'H', ']'); i++; continue }
            if au == 'L' && bu == 'L' { out = append(out, '[', 'L', 'L', ']'); i++; continue }
            if au == 'R' && bu == 'R' { out = append(out, '[', 'R', 'R', ']'); i++; continue }
        }
        out = append(out, rs[i])
    }
    return string(out)
}

// normalizeWordToBrackets converts any Macondo move word into
// bracket-coded per-tile tokens so the UI can apply them cell-by-cell.
// Handles cases:
//  - Internal digraph letters (FISE2016 mapping): Ç/ç -> [CH]/[ch], K/k -> [LL]/[ll], W/w -> [RR]/[rr]
//  - Natural digraph sequences: CH/ch, LL/ll, RR/rr -> bracket-coded
//  - Preserves existing bracket tokens as-is.
func normalizeWordToBrackets(s string) string {
    if s == "" { return s }
    rs := []rune(s)
    var out strings.Builder
    for i := 0; i < len(rs); {
        r := rs[i]
        if r == '[' {
            // copy bracket token through
            j := i+1
            for j < len(rs) && rs[j] != ']' { j++ }
            if j < len(rs) && rs[j] == ']' {
                out.WriteString(string(rs[i:j+1]))
                i = j+1
                continue
            }
            // malformed, fallthrough
        }
        switch r {
        case 'Ç': out.WriteString("[CH]"); i++; continue
        case 'ç': out.WriteString("[ch]"); i++; continue
        case 'K': out.WriteString("[LL]"); i++; continue
        case 'k': out.WriteString("[ll]"); i++; continue
        case 'W': out.WriteString("[RR]"); i++; continue
        case 'w': out.WriteString("[rr]"); i++; continue
        }
        if i+1 < len(rs) {
            a, b := rs[i], rs[i+1]
            if a == 'C' && b == 'H' { out.WriteString("[CH]"); i += 2; continue }
            if a == 'L' && b == 'L' { out.WriteString("[LL]"); i += 2; continue }
            if a == 'R' && b == 'R' { out.WriteString("[RR]"); i += 2; continue }
            if a == 'c' && b == 'h' { out.WriteString("[ch]"); i += 2; continue }
            if a == 'l' && b == 'l' { out.WriteString("[ll]"); i += 2; continue }
            if a == 'r' && b == 'r' { out.WriteString("[rr]"); i += 2; continue }
            if (a == 'c' && b == 'H') || (a == 'C' && b == 'h') { out.WriteString("[CH]"); i += 2; continue }
            if (a == 'l' && b == 'L') || (a == 'L' && b == 'l') { out.WriteString("[LL]"); i += 2; continue }
            if (a == 'r' && b == 'R') || (a == 'R' && b == 'r') { out.WriteString("[RR]"); i += 2; continue }
        }
        out.WriteRune(r)
        i++
    }
    return out.String()
}

// normalizeTokenForDict uppercases bracket tokens and single letters for dict lookup.
func normalizeTokenForDict(tk string) string {
    if tk == "" { return tk }
    if strings.HasPrefix(tk, "[") && strings.HasSuffix(tk, "]") {
        inner := tk[1:len(tk)-1]
        return "[" + strings.ToUpper(inner) + "]"
    }
    // single letter
    if len([]rune(tk)) == 1 {
        return strings.ToUpper(tk)
    }
    return tk
}

// wordExistsInKWG returns true if the tokenized word exists in the KWG.
func wordExistsInKWG(gd *wglkwg.KWG, tm *tilemapping.TileMapping, tokens []string) bool {
    // Convert tokens to machine letters
    var mw tilemapping.MachineWord
    for _, tk := range tokens {
        t := normalizeTokenForDict(tk)
        // ToMachineLetters returns 1 element for [CH]/letters in Spanish mapping
        if arr, err := tilemapping.ToMachineLetters(t, tm); err == nil && len(arr) == 1 {
            mw = append(mw, arr[0])
        } else if err == nil && len(arr) > 0 {
            mw = append(mw, arr[0])
        } else {
            return false
        }
    }
    // Traverse KWG
    node := gd.GetRootNodeIndex()
    for i := 0; i < len(mw); i++ {
        node = gd.NextNodeIdx(node, mw[i])
        if node == 0 { return false }
    }
    return gd.Accepts(node)
}

// buildMainWordTokens builds the main word tokens after applying mv onto the
// provided board rows (tokenized per cell). It returns the full contiguous
// token span in the move direction.
func buildMainWordTokens(rows []string, mv dapi.Move) []string {
    // Tokenize board into 15x15 grid
    grid := make([][]string, 15)
    for r := 0; r < 15; r++ {
        tks := tokenizeRow(strings.ReplaceAll(rows[r], ".", " "))
        line := make([]string, 15)
        copy(line, tks)
        for c := 0; c < 15; c++ { if line[c] == "" { line[c] = " " } }
        grid[r] = line
    }
    // Apply move tokens (only on empty cells)
    mtoks := tokenizeRow(normalizeWordToBrackets(mv.Word))
    r, c := mv.Row, mv.Col
    dr, dc := 0, 1
    if strings.ToUpper(mv.Dir) == "V" { dr, dc = 1, 0 }
    for _, tk := range mtoks {
        if r < 0 || r >= 15 || c < 0 || c >= 15 { break }
        if grid[r][c] == " " { grid[r][c] = tk }
        r += dr; c += dc
    }
    if strings.ToUpper(mv.Dir) == "H" {
        // find span on row mv.Row scanning outwards from placed area
        row := mv.Row
        // left bound
        start := mv.Col
        for start > 0 && strings.TrimSpace(grid[row][start-1]) != "" { start-- }
        // right bound: start from last placed index and extend while non-space
        lastPlaced := mv.Col + len(mtoks) - 1
        if lastPlaced > 14 { lastPlaced = 14 }
        end := lastPlaced
        for end < 14 && strings.TrimSpace(grid[row][end+1]) != "" { end++ }
        out := make([]string, 0, end-start+1)
        for cc := start; cc <= end; cc++ { out = append(out, grid[row][cc]) }
        return out
    }
    // vertical span on column mv.Col
    col := mv.Col
    start := mv.Row
    for start > 0 && strings.TrimSpace(grid[start-1][col]) != "" { start-- }
    lastPlaced := mv.Row + len(mtoks) - 1
    if lastPlaced > 14 { lastPlaced = 14 }
    end := lastPlaced
    for end < 14 && strings.TrimSpace(grid[end+1][col]) != "" { end++ }
    out := make([]string, 0, end-start+1)
    for rr := start; rr <= end; rr++ { out = append(out, grid[rr][col]) }
    return out
}

func main() {
    dec := json.NewDecoder(os.Stdin)
    var req dapi.MovesRequest
    if err := dec.Decode(&req); err != nil {
        log.Fatalf("bad json: %v", err)
    }

    // Defaults (can be overridden by KWGPath or ruleset)
    lexicon := "NWL23"
    distName := "English"
    // Heuristic: ruleset hint for Spanish distribution
    if strings.Contains(strings.ToLower(req.Ruleset), "osps") || strings.Contains(strings.ToLower(req.Ruleset), "spanish") {
        distName = "Spanish"
        // If no explicit KWG was provided, prefer a Spanish lexicon label
        if strings.TrimSpace(req.KWGPath) == "" {
            lexicon = "FILE2017" // common Spanish KWG bundled/available
        }
    }

    cfg := config.DefaultConfig()
    // Prefer explicit MACONDO_DATA_PATH for word-golib caches
    if dp := os.Getenv("MACONDO_DATA_PATH"); dp != "" {
        cfg.Set(config.ConfigDataPath, dp)
    } else {
        // Fallback: try to locate ../macondo/data relative to this executable
        if ex, err := os.Executable(); err == nil {
            base := filepath.Dir(ex)
            // backend/cmd/macondo-wrapper (approx) → repo root
            try := filepath.Join(base, "..", "..", "..", "..", "macondo", "data")
            if st, err := os.Stat(try); err == nil && st.IsDir() {
                cfg.Set(config.ConfigDataPath, try)
            }
        }
    }

    // If a KWG file path is provided, prefer loading from that directory.
    var leaveFile string
    if req.KWGPath != "" {
        if !filepath.IsAbs(req.KWGPath) {
            if abs, err := filepath.Abs(req.KWGPath); err == nil { req.KWGPath = abs }
        }
        if st, err := os.Stat(req.KWGPath); err == nil && !st.IsDir() {
            base := filepath.Base(req.KWGPath)
            if strings.HasSuffix(strings.ToLower(base), ".kwg") { base = strings.TrimSuffix(base, filepath.Ext(base)) }
            lexicon = base
            // Ensure KWG is accessible under DataPath/lexica/gaddag/<lexicon>.kwg
            dataDir := cfg.GetString(config.ConfigDataPath)
            if dataDir == "" { dataDir = "." }
            dstDir := filepath.Join(dataDir, "lexica", "gaddag")
            _ = os.MkdirAll(dstDir, 0o755)
            dst := filepath.Join(dstDir, base+".kwg")
            // Copy if dst does not exist or sizes differ
            needCopy := true
            if dstInfo, err := os.Stat(dst); err == nil {
                if dstInfo.Size() == st.Size() { needCopy = false }
            }
            if needCopy {
                if err := copyFile(req.KWGPath, dst); err != nil {
                    log.Fatalf("failed to stage KWG: %v", err)
                }
            }
            // If KLV2Path was provided, stage it too
            if req.KLV2Path != "" {
                klvSrc := req.KLV2Path
                if !filepath.IsAbs(klvSrc) {
                    if abs, err := filepath.Abs(klvSrc); err == nil { klvSrc = abs }
                }
                if st2, err := os.Stat(klvSrc); err == nil && !st2.IsDir() {
                    // Stage KLV2 into strategy/<lexicon>/leaves.klv2 (as Macondo expects)
                    stratDir := filepath.Join(dataDir, "strategy", base)
                    _ = os.MkdirAll(stratDir, 0o755)
                    klvDst := filepath.Join(stratDir, equity.LeavesFilename)
                    needCopy2 := true
                    if d2, err := os.Stat(klvDst); err == nil {
                        if d2.Size() == st2.Size() { needCopy2 = false }
                    }
                    if needCopy2 {
                        if err := copyFile(klvSrc, klvDst); err != nil {
                            log.Fatalf("failed to stage KLV2: %v", err)
                        }
                    }
                    // Pass just the filename; loader finds it under strategy/<lexicon>
                    leaveFile = equity.LeavesFilename
                }
            }
            // Heuristic: if likely Spanish, switch distribution
            low := strings.ToLower(lexicon)
            if strings.Contains(low, "osps") || strings.Contains(low, "fise") || strings.Contains(low, "file2017") || strings.Contains(strings.ToLower(req.Ruleset), "osps") {
                distName = "Spanish"
            }
        }
    } else {
        // No explicit KWG path; try to stage a known file into DataPath if missing
        dataDir := cfg.GetString(config.ConfigDataPath)
        if dataDir == "" { dataDir = "." }
        dstDir := filepath.Join(dataDir, "lexica", "gaddag")
        _ = os.MkdirAll(dstDir, 0o755)
        dst := filepath.Join(dstDir, lexicon+".kwg")
        if _, err := os.Stat(dst); err != nil {
            // Try to locate from common repo paths
            roots := []string{
                "../../" + lexicon + ".kwg",
                "../" + lexicon + ".kwg",
                lexicon + ".kwg",
                filepath.Join("..", "..", "lexica", lexicon+".kwg"),
                filepath.Join("lexica", lexicon+".kwg"),
            }
            for _, src := range roots {
                if _, err2 := os.Stat(src); err2 == nil {
                    _ = copyFile(src, dst)
                    break
                }
            }
        }
    }

    // Final fallback: if selected lexicon file is still missing in DataPath, try common Spanish lexica.
    {
        dataDir := cfg.GetString(config.ConfigDataPath)
        if dataDir == "" { dataDir = "." }
        dstDir := filepath.Join(dataDir, "lexica", "gaddag")
        _ = os.MkdirAll(dstDir, 0o755)
        cur := filepath.Join(dstDir, lexicon+".kwg")
        if _, err := os.Stat(cur); err != nil {
            candidates := []string{"FISE2016_converted.kwg", "FILE2017.kwg"}
            for _, cand := range candidates {
                roots := []string{
                    "../../" + cand,
                    "../" + cand,
                    cand,
                    filepath.Join("..", "..", "lexica", cand),
                    filepath.Join("lexica", cand),
                }
                for _, src := range roots {
                    if _, err2 := os.Stat(src); err2 == nil {
                        _ = copyFile(src, filepath.Join(dstDir, cand))
                        lexicon = strings.TrimSuffix(cand, ".kwg")
                        goto FALLBACK_DONE
                    }
                }
            }
        }
    }
FALLBACK_DONE:

    // Load letter distribution and KWG/gaddag
    ld, err := tilemapping.GetDistribution(cfg.WGLConfig(), distName)
    if err != nil {
        log.Fatalf("load distribution %s: %v", distName, err)
    }
    gd, err := wglkwg.GetKWG(cfg.WGLConfig(), lexicon)
    if err != nil {
        log.Fatalf("load lexicon %s: %v", lexicon, err)
    }

    // Build board. Accept optional 15x15 rows (spaces for empty; allow '.' as empty).
    bd := board.MakeBoard(board.CrosswordGameBoard)
    debug := os.Getenv("DEBUG_WRAPPER") == "1"
    var dbgRow7 []tilemapping.MachineLetter
    if len(req.Board.Rows) == 15 {
        for i := 0; i < 15; i++ {
            raw := strings.ReplaceAll(req.Board.Rows[i], ".", " ")
            // Tokenize per cell to avoid ambiguous parsing of the whole row
            toks := tokenizeRow(raw)
            mls := make([]tilemapping.MachineLetter, 15)
            for c := 0; c < 15; c++ {
                tk := " "
                if c < len(toks) && toks[c] != "" { tk = toks[c] }
                if strings.TrimSpace(tk) == "" {
                    mls[c] = 0
                    continue
                }
                // Convert single-cell token to machine letter; lowercase implies blank-as-letter
                if out, err := tilemapping.ToMachineLetters(tk, ld.TileMapping()); err == nil && len(out) == 1 {
                    mls[c] = out[0]
                } else if err == nil && len(out) > 1 {
                    // Fallback: take first
                    mls[c] = out[0]
                } else {
                    // Unrecognized; treat as empty
                    mls[c] = 0
                }
            }
            bd.SetRowMLs(i, mls)
            if debug && i == 7 {
                dbgRow7 = append([]tilemapping.MachineLetter(nil), mls...)
            }
        }
    }
    // Compute cross-sets for anchors
    cross_set.GenAllCrossSets(bd, gd, ld)

    // Rack parsing
    // By default collapse natural CH/LL/RR into bracket-coded tokens for rack parsing.
    // Allow disabling via RACK_COLLAPSE=0 to let the engine handle natural input.
    rackInput := req.Rack
    if os.Getenv("RACK_COLLAPSE") != "0" {
        rackInput = rackToBracket(rackInput)
    }
    rack := tilemapping.RackFromString(rackInput, ld.TileMapping())

    // Prepare GameRules and Game so equity calculators have bag/rack context
    rules, err := game.NewBasicGameRules(cfg, lexicon, board.CrosswordGameLayout, distName, game.CrossScoreAndSet, game.VarClassic)
    if err != nil {
        log.Fatalf("rules init: %v", err)
    }
    // minimal two-player info
    players := []*pb.PlayerInfo{{Nickname: "P1"}, {Nickname: "P2"}}
    g, err := game.NewGame(rules, players)
    if err != nil {
        log.Fatalf("game init: %v", err)
    }
    // Initialize racks safely without ThrowRacksIn
    g.SetPlayerOnTurn(0)
    _ = g.SetRackForOnly(1, tilemapping.RackFromString("", ld.TileMapping()))
    if err := g.SetRackForOnly(0, rack); err != nil {
        log.Fatalf("set rack: %v", err)
    }
    // Generate moves with equity calculators (static: score + leave value)
    mg := movegen.NewGordonGenerator(gd, bd, ld)
    var csc *equity.CombinedStaticCalculator
    if calc, err := equity.NewCombinedStaticCalculator(lexicon, cfg, leaveFile, equity.PEGAdjustmentFilename); err == nil {
        csc = calc
        mg.SetEquityCalculators([]equity.EquityCalculator{calc})
    }
    // Attach game so equity calculators can access bag/racks
    mg.SetGame(g)
    plays := mg.GenAll(rack, false)

    // Helper: convert move to API shape
    toMove := func(pm *move.Move) dapi.Move {
        if pm == nil { return dapi.Move{} }
        r, c, v := pm.CoordsAndVertical()
        dir := "H"
        if v { dir = "V" }
        lv := 0.0
        if csc != nil {
            lv = csc.LeaveValue(pm.Leave())
        } else {
            lv = pm.Equity() - float64(pm.Score())
        }
        eqFinal := pm.Equity()
        if eqFinal == 0 {
            eqFinal = float64(pm.Score()) + lv
        }
        // Macondo's TilesString can include '.' placeholders for existing board tiles.
        // Strip them so the UI applies only newly placed tiles into empty cells.
        raw := strings.ReplaceAll(pm.TilesString(), ".", "")
        word := normalizeWordToBrackets(raw)
        return dapi.Move{ Word: word, Row: r, Col: c, Dir: dir, Score: pm.Score(), Leave: pm.LeaveString(), LeaveVal: lv, Equity: eqFinal }
    }

    res := dapi.MovesResponse{}
    validateSpan := os.Getenv("VALIDATE_SPAN") == "1"

    // If simulation requested, run Monte Carlo on top-K plays
    if strings.EqualFold(req.Mode, "sim") || req.Sim != nil {
        // Defaults
        iters := 100
        plies := 2
        threads := 1
        topK := 20
        if req.Sim != nil {
            if req.Sim.Iters > 0 { iters = req.Sim.Iters }
            if req.Sim.Plies > 0 { plies = req.Sim.Plies }
            if req.Sim.Threads > 0 { threads = req.Sim.Threads }
            if req.Sim.TopK > 0 { topK = req.Sim.TopK }
        }
        if topK > len(plays) { topK = len(plays) }
        cand := plays[:topK]
        // Optional KWG span validation
        if validateSpan {
            filtered := make([]*move.Move, 0, len(cand))
            for _, pm := range cand {
                mv := toMove(pm)
                span := buildMainWordTokens(req.Board.Rows, mv)
                if wordExistsInKWG(gd, ld.TileMapping(), span) {
                    filtered = append(filtered, pm)
                }
            }
            cand = filtered
        }
        // Init simmer
        simmer := &montecarlo.Simmer{}
        // If no csc (no leaves), create a default one to avoid nil panics
        if csc == nil {
            if calc, err := equity.NewCombinedStaticCalculator(lexicon, cfg, "", equity.PEGAdjustmentFilename); err == nil {
                csc = calc
            }
        }
        simmer.Init(g, []equity.EquityCalculator{csc}, csc, cfg)
        simmer.SetThreads(threads)
        // Reasonable default stopping condition
        simmer.SetStoppingCondition(montecarlo.Stop99)
        if err := simmer.PrepareSim(plies, cand); err != nil {
            log.Fatalf("prepare sim: %v", err)
        }
        simmer.SimSingleThread(iters, plies)
        sp := simmer.PlaysByWinProb().PlaysNoLock()
        // Build response sorted by win probability
        for _, simPlay := range sp {
            pm := simPlay.Move()
            if pm == nil || pm.Action() != move.MoveTypePlay { continue }
            mv := toMove(pm)
            // Attach sim metrics
            mv.WinPct = 100.0 * simPlay.WinProb()
            // Use current ply (0) score stats if present
            stats := simPlay.ScoreStatsNoLock()
            if len(stats) > 0 {
                mv.Mean = stats[0].Mean()
                mv.Stdev = stats[0].Stdev()
            }
            res.All = append(res.All, mv)
        }
        if len(res.All) > 0 {
            res.Best = res.All[0]
            // Identify ties by winPct equality within epsilon
            eps := 1e-6
            bestWP := res.Best.WinPct
            for i := 1; i < len(res.All); i++ {
                if math.Abs(res.All[i].WinPct-bestWP) < eps {
                    res.Ties = append(res.Ties, res.All[i])
                } else {
                    break
                }
            }
        }
    } else {
        // Static equity mode (default)
        bestScore := -1
        for i, pm := range plays {
            if pm.Action() != move.MoveTypePlay { continue }
            mv := toMove(pm)
            if validateSpan {
                // Optional: validate main-word span against KWG
                span := buildMainWordTokens(req.Board.Rows, mv)
                if !wordExistsInKWG(gd, ld.TileMapping(), span) {
                    continue
                }
            }
            res.All = append(res.All, mv)
            if i == 0 || mv.Score > bestScore {
                res.Best = mv
                bestScore = mv.Score
                res.Ties = res.Ties[:0]
            } else if mv.Score == bestScore {
                res.Ties = append(res.Ties, mv)
            }
        }
    }

    enc := json.NewEncoder(os.Stdout)
    if err := enc.Encode(res); err != nil {
        fmt.Fprintf(os.Stderr, "encode error: %v\n", err)
        os.Exit(1)
    }
    if debug {
        // Dump row 8 (index 7) diagnostics for anchors: positions 7 (H) and 10 (K) 1-based
        if len(dbgRow7) == 15 {
            ml77 := dbgRow7[7]
            ml7k := dbgRow7[10]
            fmt.Fprintf(os.Stderr, "DBG row8 col8 ml=%v blank=%v label=%s; col11 ml=%v blank=%v label=%s\n",
                ml77, ml77.IsBlanked(), ld.TileMapping().Letter(ml77), ml7k, ml7k.IsBlanked(), ld.TileMapping().Letter(ml7k))
        }
    }
}

func copyFile(src, dst string) error {
    in, err := os.Open(src)
    if err != nil {
        return err
    }
    defer in.Close()
    out, err := os.Create(dst)
    if err != nil {
        return err
    }
    defer func() { _ = out.Close() }()
    if _, err := io.Copy(out, in); err != nil {
        return err
    }
    return out.Sync()
}

// normalizeBracketTokens uppercases the content inside [...] tokens so
// variants like [ch], [ll], [rr] become [CH], [LL], [RR], matching
// TileMapping labels (while leaving lowercase single letters for blanks intact).
// Note: We deliberately preserve bracket token case so [ch] remains blank-of-[CH]
// and [CH] remains a face-value digraph. Macondo's SetRow supports both forms.
