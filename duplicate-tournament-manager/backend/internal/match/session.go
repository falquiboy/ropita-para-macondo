package match

import (
    "errors"
    "fmt"
    "path/filepath"
    "strings"
    "sync"
    "os"
    "log"

    "github.com/domino14/macondo/board"
    mconfig "github.com/domino14/macondo/config"
    "github.com/domino14/macondo/equity"
    pb "github.com/domino14/macondo/gen/api/proto/macondo"
    "github.com/domino14/macondo/game"
    "github.com/domino14/macondo/move"
    "github.com/domino14/macondo/montecarlo"
    "github.com/domino14/word-golib/tilemapping"
    aitp "github.com/domino14/macondo/ai/turnplayer"
    "github.com/domino14/macondo/movegen"
)

// Mode of AI
type AIMode string

const (
    AIStatic AIMode = "static" // best score/equity
    AISim    AIMode = "sim"    // montecarlo simmer
)

type Session struct {
    mu sync.Mutex

    ID       string
    Ruleset  string
    Lexicon  string
    CFG      *mconfig.Config
    Game     *game.Game
    LD       *tilemapping.LetterDistribution
    KwgName  string

    // helpers
    sp  *aitp.AIStaticTurnPlayer
    csc *equity.CombinedStaticCalculator
}

// NewSession creates a new macondo game for Spanish OSPS rules
func NewSession(id, ruleset, kwgPath string) (*Session, error) {
    if strings.TrimSpace(ruleset) == "" { ruleset = "OSPS49" }
    cfg := mconfig.DefaultConfig()
    // Prefer MACONDO_DATA_PATH; otherwise try repo-relative defaults handled by macondo engine helpers
    // Assume env was set by start_go_server.sh; otherwise, game.NewBasicGameRules will fail early.

    // Stage KWG into WGL cache directory name detection
    lexName := baseLexiconName(kwgPath)
    if lexName == "" { lexName = "FISE2016_converted" }

    // Build rules and game
    rules, err := game.NewBasicGameRules(cfg, lexName, board.CrosswordGameLayout, "Spanish", game.CrossScoreAndSet, game.VarClassic)
    if err != nil {
        return nil, fmt.Errorf("rules init: %w", err)
    }
    players := []*pb.PlayerInfo{{Nickname: "You"}, {Nickname: "Macondo"}}
    g, err := game.NewGame(rules, players)
    if err != nil {
        return nil, fmt.Errorf("game init: %w", err)
    }
    ld, err := tilemapping.GetDistribution(cfg.WGLConfig(), "Spanish")
    if err != nil { return nil, err }

    // Equity calc
    csc, _ := equity.NewCombinedStaticCalculator(lexName, cfg, equity.LeavesFilename, equity.PEGAdjustmentFilename)

    sp, _ := aitp.NewAIStaticTurnPlayerFromGame(g, cfg, []equity.EquityCalculator{csc})

    s := &Session{ID: id, Ruleset: ruleset, Lexicon: lexName, CFG: cfg, Game: g, LD: ld, KwgName: lexName, sp: sp, csc: csc}
    // Initialize empty racks first to avoid nil racks in SetRandomRack
    _ = s.Game.SetRackForOnly(0, tilemapping.RackFromString("", ld.TileMapping()))
    _ = s.Game.SetRackForOnly(1, tilemapping.RackFromString("", ld.TileMapping()))
    s.Game.SetPlayerOnTurn(0)
    // Deal random racks for both players from the Spanish bag
    // This respects the current bag state and tile mapping (incl. digraphs)
    if _, err := s.Game.SetRandomRack(0, nil); err != nil { return nil, fmt.Errorf("deal p0: %w", err) }
    if _, err := s.Game.SetRandomRack(1, nil); err != nil { return nil, fmt.Errorf("deal p1: %w", err) }
    return s, nil
}

func baseLexiconName(kwgPath string) string {
    if kwgPath == "" { return "" }
    base := filepath.Base(kwgPath)
    if i := strings.LastIndexByte(base, '.'); i >= 0 { base = base[:i] }
    return base
}

type Coords struct { Row, Col int; Dir string }

// PlayHuman validates and applies a human move. Returns applied move and score.
func (s *Session) PlayHuman(word string, c Coords) (*move.Move, error) {
    s.mu.Lock(); defer s.mu.Unlock()
    // Normalize UI token encoding: UI may send digraphs/blank letters wrapped
    // in brackets like "[CH]" or "[a]". Strip brackets to match macondo's
    // TilesString() output (which uses plain letters like CH/LL/RR and lowercase
    // for blanks). Case-insensitive comparison below handles blank casing.
    word = normalizeWord(word)
    // Generate all legal plays for current rack and pick the one matching word/coords
    rack := s.Game.RackFor(s.Game.PlayerOnTurn())
    if os.Getenv("DEBUG_MATCH") == "1" {
        log.Printf("PlayHuman want=%q at r=%d c=%d d=%s rack=%s", word, c.Row, c.Col, c.Dir, rack.String())
    }
    s.sp.MoveGenerator().GenAll(rack, true)
    plays := s.sp.MoveGenerator().(*movegen.GordonGenerator).Plays()
    if os.Getenv("DEBUG_MATCH") == "1" {
        // Log up to 30 candidate plays: word (tiles only), coords, score
        max := len(plays); if max > 30 { max = 30 }
        for i := 0; i < max; i++ { pm := plays[i];
            if pm.Action() != move.MoveTypePlay { continue }
            r, col, v := pm.CoordsAndVertical(); dir := "H"; if v { dir = "V" }
            tiles := strings.ReplaceAll(pm.TilesString(), ".", "")
            log.Printf("cand[%d] %s @ (%d,%d,%s) score=%d", i, tiles, r, col, dir, pm.Score())
        }
        log.Printf("total candidates: %d", len(plays))
    }
    targetDir := strings.ToUpper(c.Dir)
    // First, try exact match on word and coordinates
    for _, pm := range plays {
        if pm.Action() != move.MoveTypePlay { continue }
        r, col, v := pm.CoordsAndVertical()
        dir := "H"; if v { dir = "V" }
        if r == c.Row && col == c.Col && dir == targetDir {
            // TilesString includes anchors; compare word ignoring '.'
            tiles := strings.ReplaceAll(pm.TilesString(), ".", "")
            if equalWord(tiles, word) {
                if err := s.Game.PlayMove(pm, false, 0); err != nil { return nil, err }
                return pm, nil
            }
        }
    }
    // Fallback: if exactly one candidate exists at those coordinates/direction, accept it
    var only *move.Move
    for _, pm := range plays {
        if pm.Action() != move.MoveTypePlay { continue }
        r, col, v := pm.CoordsAndVertical()
        dir := "H"; if v { dir = "V" }
        if r == c.Row && col == c.Col && dir == targetDir {
            if only != nil { only = nil; break }
            only = pm
        }
    }
    if only != nil {
        if os.Getenv("DEBUG_MATCH") == "1" { log.Printf("fallback accepting candidate at coords; want=%q", word) }
        if err := s.Game.PlayMove(only, false, 0); err != nil { return nil, err }
        return only, nil
    }
    if os.Getenv("DEBUG_MATCH") == "1" {
        log.Printf("no match for %q at (%d,%d,%s)", word, c.Row, c.Col, c.Dir)
    }
    return nil, errors.New("illegal move")
}

// equalWord compares move tile strings but normalizes Spanish digraphs and brackets.
// Treats [CH]/Ç equivalent to CH, [LL]/K to LL, [RR]/W to RR; ignores case.
func equalWord(a, b string) bool { return canon(a) == canon(b) }

func canon(s string) string {
    if s == "" { return s }
    // Remove dots (anchors) just in case caller forgot
    s = strings.ReplaceAll(s, ".", "")
    var out strings.Builder
    rs := []rune(s)
    for i := 0; i < len(rs); i++ {
        r := rs[i]
        if r == '[' { // bracket token
            j := i+1
            for j < len(rs) && rs[j] != ']' { j++ }
            if j < len(rs) { // have closing bracket
                inner := string(rs[i+1:j])
                // map digraph tokens and blanks in brackets to their letters
                switch inner {
                case "CH", "ch": out.WriteString("CH")
                case "LL", "ll": out.WriteString("LL")
                case "RR", "rr": out.WriteString("RR")
                default:
                    if len(inner) == 1 { out.WriteString(strings.ToUpper(inner)) } else { out.WriteString(strings.ToUpper(inner)) }
                }
                i = j
                continue
            }
        }
        // Map single-letter FISE digraph symbols if present
        switch r {
        case 'Ç', 'ç': out.WriteString("CH")
        case 'K', 'k': out.WriteString("LL")
        case 'W', 'w': out.WriteString("RR")
        default:
            out.WriteString(strings.ToUpper(string(r)))
        }
    }
    return out.String()
}

// normalizeWord removes client-side bracket tokens like [CH],[LL],[RR] and
// blank-marked letters like [a] -> a. This keeps multi-letter digraphs intact
// while stripping only the brackets so that comparisons against macondo's
// TilesString (without brackets) succeed.
func normalizeWord(s string) string {
    if s == "" { return s }
    var b strings.Builder
    b.Grow(len(s))
    for i := 0; i < len(s); i++ {
        c := s[i]
        if c == '[' || c == ']' { continue }
        b.WriteByte(c)
    }
    return b.String()
}

// Exchange tiles from current rack
func (s *Session) Exchange(letters string) error {
    s.mu.Lock(); defer s.mu.Unlock()
    rack := s.Game.RackFor(s.Game.PlayerOnTurn())
    // Verify availability
    mls, err := tilemapping.ToMachineLetters(letters, s.LD.TileMapping())
    if err != nil { return err }
    for _, ml := range mls {
        if rack.CountOf(ml) == 0 { return errors.New("tile not in rack") }
    }
    // Build a specific exchange move
    em, err := s.sp.BaseTurnPlayer.NewExchangeMove(s.Game.PlayerOnTurn(), letters)
    if err != nil { return err }
    return s.Game.PlayMove(em, false, 0)
}

// Pass the turn
func (s *Session) Pass() error {
    s.mu.Lock(); defer s.mu.Unlock()
    pm, err := s.sp.BaseTurnPlayer.NewPassMove(s.Game.PlayerOnTurn())
    if err != nil { return err }
    return s.Game.PlayMove(pm, false, 0)
}

// AIMove makes an AI move using either static or simulation mode.
func (s *Session) AIMove(mode AIMode, simIters, simPlies, topK int) (*move.Move, error) {
    s.mu.Lock(); defer s.mu.Unlock()
    rack := s.Game.RackFor(s.Game.PlayerOnTurn())
    s.sp.MoveGenerator().GenAll(rack, true)
    plays := s.sp.MoveGenerator().(*movegen.GordonGenerator).Plays()
    if len(plays) == 0 { return nil, errors.New("no plays") }
    var best *move.Move
    if mode == AIStatic {
        // pick max score
        best = plays[0]
        for _, pm := range plays[1:] { if pm.Score() > best.Score() { best = pm } }
    } else {
        // Simmer
        if topK <= 0 || topK > len(plays) { topK = min(len(plays), 20) }
        cand := plays[:topK]
        // Ensure csc exists
        if s.csc == nil { c, _ := equity.NewCombinedStaticCalculator(s.KwgName, s.CFG, equity.LeavesFilename, equity.PEGAdjustmentFilename); s.csc = c }
        simmer := &montecarlo.Simmer{}
        simmer.Init(s.Game, []equity.EquityCalculator{s.csc}, s.csc, s.CFG)
        simmer.SetThreads(1)
        if simPlies <= 0 { simPlies = 2 }
        if err := simmer.PrepareSim(simPlies, cand); err != nil { return nil, err }
        if simIters <= 0 { simIters = 300 }
        simmer.SimSingleThread(simIters, simPlies)
        sp := simmer.WinningPlay()
        if sp == nil { return nil, errors.New("no sim winner") }
        best = sp.Move()
    }
    if err := s.Game.PlayMove(best, false, 0); err != nil { return nil, err }
    return best, nil
}

func min(a,b int) int { if a<b { return a }; return b }
