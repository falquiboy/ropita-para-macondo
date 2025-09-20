package match

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"runtime"
	"sync"
	"time"

	aitp "github.com/domino14/macondo/ai/turnplayer"
	"github.com/domino14/macondo/ai/bot"
	"github.com/domino14/macondo/board"
	mconfig "github.com/domino14/macondo/config"
	"github.com/domino14/macondo/equity"
	"github.com/domino14/macondo/game"
	pb "github.com/domino14/macondo/gen/api/proto/macondo"
	"github.com/domino14/macondo/montecarlo"
	"github.com/domino14/macondo/move"
	"github.com/domino14/macondo/movegen"
	"github.com/domino14/word-golib/tilemapping"
	"github.com/rs/zerolog"
)

// Mode of AI
type AIMode string

const (
	AIStatic AIMode = "static" // best score/equity
	AISim    AIMode = "sim"    // montecarlo simmer
)

// LogWriter interface for capturing bot logs
type LogWriter interface {
	io.Writer
}

type Session struct {
	mu sync.Mutex

	ID      string
	Ruleset string
	Lexicon string
	CFG     *mconfig.Config
	Game    *game.Game
	LD      *tilemapping.LetterDistribution
	KwgName string

	// helpers
	sp  *aitp.AIStaticTurnPlayer
	csc *equity.CombinedStaticCalculator

	// minimal in-memory history fallback when engine history isn't populated
	hist []HistRow

	// Bot log writer for capturing Macondo logs
	logWriter LogWriter

	// Analysis mode (manual input): free placement and manual racks/scores
	Analysis        bool
	ManualBoardRows [15]string
	ManualScore     [2]int
	ManualRack      [2]string // textual racks per player (optional/partial)

	// Analysis undo/redo stacks
	manualUndo []AnalysisSnapshot
	manualRedo []AnalysisSnapshot

	// Analysis: racks actually used per ply (tokens placed), aligned with fallback hist length
	manualPlyRacks []string

	// Analysis bag: remaining counts per token key (e.g., "A", "[CH]", "?")
	AnalysisBag map[string]int

	// Analysis turn pointer (number of events applied)
	AnalysisTurn int
}

type AnalysisSnapshot struct {
	Rows       [15]string
	Score      [2]int
	OnTurn     int
	HistLen    int
	PlyRacks   []string
	ManualRack [2]string
	Bag        map[string]int
	HistRows   []HistRow
}

// captureSnapshot captures the current manual analysis state.
func (s *Session) captureSnapshot() AnalysisSnapshot {
	snap := AnalysisSnapshot{Score: s.ManualScore, OnTurn: int(s.Game.PlayerOnTurn()), HistLen: len(s.hist)}
	copy(snap.Rows[:], s.ManualBoardRows[:])
	if len(s.manualPlyRacks) > 0 {
		tmp := make([]string, len(s.manualPlyRacks))
		copy(tmp, s.manualPlyRacks)
		snap.PlyRacks = tmp
	}
	snap.ManualRack = s.ManualRack
	if s.AnalysisBag != nil {
		bag := make(map[string]int, len(s.AnalysisBag))
		for k, v := range s.AnalysisBag {
			bag[k] = v
		}
		snap.Bag = bag
	}
	if len(s.hist) > 0 {
		hr := make([]HistRow, len(s.hist))
		copy(hr, s.hist)
		snap.HistRows = hr
	}
	return snap
}

// restoreSnapshot restores a manual analysis snapshot.
func (s *Session) restoreSnapshot(snap AnalysisSnapshot) {
	copy(s.ManualBoardRows[:], snap.Rows[:])
	s.ManualScore = snap.Score
	s.Game.SetPlayerOnTurn(snap.OnTurn)
	if snap.HistRows != nil {
		s.hist = append([]HistRow(nil), snap.HistRows...)
	} else if snap.HistLen >= 0 && snap.HistLen <= len(s.hist) {
		s.hist = s.hist[:snap.HistLen]
	}
	if snap.PlyRacks != nil {
		s.manualPlyRacks = append([]string(nil), snap.PlyRacks...)
	} else {
		s.manualPlyRacks = nil
	}
	s.ManualRack = snap.ManualRack
	if snap.Bag != nil {
		bag := make(map[string]int, len(snap.Bag))
		for k, v := range snap.Bag {
			bag[k] = v
		}
		s.AnalysisBag = bag
	} else {
		s.AnalysisBag = nil
	}
}

// Expose helpers for handlers without exporting full fields
func (s *Session) Capture() AnalysisSnapshot     { return s.captureSnapshot() }
func (s *Session) Restore(snap AnalysisSnapshot) { s.restoreSnapshot(snap) }

// Undo/redo stack helpers
func (s *Session) ClearRedo()                     { s.manualRedo = nil }
func (s *Session) PushUndo(snap AnalysisSnapshot) { s.manualUndo = append(s.manualUndo, snap) }
func (s *Session) PopUndo() (AnalysisSnapshot, bool) {
	if len(s.manualUndo) == 0 {
		return AnalysisSnapshot{}, false
	}
	last := s.manualUndo[len(s.manualUndo)-1]
	s.manualUndo = s.manualUndo[:len(s.manualUndo)-1]
	return last, true
}
func (s *Session) PushRedo(snap AnalysisSnapshot) { s.manualRedo = append(s.manualRedo, snap) }
func (s *Session) PopRedo() (AnalysisSnapshot, bool) {
	if len(s.manualRedo) == 0 {
		return AnalysisSnapshot{}, false
	}
	last := s.manualRedo[len(s.manualRedo)-1]
	s.manualRedo = s.manualRedo[:len(s.manualRedo)-1]
	return last, true
}

// AppendPlyRack appends definitive rack (placed tokens) for latest ply.
func (s *Session) AppendPlyRack(r string) { s.manualPlyRacks = append(s.manualPlyRacks, r) }

// ManualPlyRacks exposes placed-rack list for handlers
func (s *Session) ManualPlyRacks() []string { return s.manualPlyRacks }

// RebuildToTurn reconstructs the Game state to the specified turn (0..len(events))
func (s *Session) RebuildToTurn(turn int) error {
	hist := s.Game.History()
	rules := s.Game.Rules()
	ng, err := game.NewFromHistory(hist, rules, 0)
	if err != nil {
		return err
	}
	if turn < 0 {
		turn = 0
	}
	if turn > len(hist.Events) {
		turn = len(hist.Events)
	}
	if err := ng.PlayToTurn(turn); err != nil {
		return err
	}
	s.Game = ng
	// Rebuild helpers
	csc, _ := equity.NewCombinedStaticCalculator(s.Lexicon, s.CFG, equity.LeavesFilename, equity.PEGAdjustmentFilename)
	s.csc = csc
	s.sp, _ = aitp.NewAIStaticTurnPlayerFromGame(s.Game, s.CFG, []equity.EquityCalculator{csc})
	// Mirror board rows and score for UI/snapshots
	alph := s.Game.Alphabet()
	for rr := 0; rr < 15; rr++ {
		var sb strings.Builder
		for cc := 0; cc < 15; cc++ {
			ml := s.Game.Board().GetLetter(rr, cc)
			if ml == 0 {
				sb.WriteByte(' ')
			} else {
				sb.WriteString(alph.Letter(ml))
			}
		}
		s.ManualBoardRows[rr] = sb.String()
	}
	s.ManualScore = [2]int{s.Game.PointsFor(0), s.Game.PointsFor(1)}
	s.AnalysisTurn = turn
	return nil
}

// HistRow is a lightweight move/event record for UI scoresheet.
type HistRow struct {
	Ply    int
	Player int
	Type   string // PLAY, PASS, EXCH
	Word   string // primary word or tiles/exchanged letters
	Row    int
	Col    int
	Dir    string // H or V
	Score  int
	Cum    int
}

// NewSession creates a new macondo game for Spanish OSPS rules
func NewSession(id, ruleset, kwgPath string) (*Session, error) {
	if strings.TrimSpace(ruleset) == "" {
		ruleset = "OSPS49"
	}
	cfg := mconfig.DefaultConfig()
	// Prefer MACONDO_DATA_PATH; otherwise try repo-relative defaults handled by macondo engine helpers
	// Assume env was set by start_go_server.sh; otherwise, game.NewBasicGameRules will fail early.

	// Stage KWG into WGL cache directory name detection
	lexName := baseLexiconName(kwgPath)
	if lexName == "" {
		lexName = "FISE2016_converted"
	}

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
	// Initialize history/state and deal racks
	g.StartGame()
	// Default to SINGLE challenge rule per Spanish preference
	g.SetChallengeRule(pb.ChallengeRule_SINGLE)
	// OSPS: end after two scoreless turns per player (4 consecutives total)
	g.SetMaxScorelessTurns(4)
	ld, err := tilemapping.GetDistribution(cfg.WGLConfig(), "Spanish")
	if err != nil {
		return nil, err
	}

	// Equity calc
	csc, _ := equity.NewCombinedStaticCalculator(lexName, cfg, equity.LeavesFilename, equity.PEGAdjustmentFilename)

	sp, _ := aitp.NewAIStaticTurnPlayerFromGame(g, cfg, []equity.EquityCalculator{csc})

	s := &Session{ID: id, Ruleset: ruleset, Lexicon: lexName, CFG: cfg, Game: g, LD: ld, KwgName: lexName, sp: sp, csc: csc}
	// Ensure player 0 starts
	s.Game.SetPlayerOnTurn(0)
	return s, nil
}

func baseLexiconName(kwgPath string) string {
	if kwgPath == "" {
		return ""
	}
	base := filepath.Base(kwgPath)
	if i := strings.LastIndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	return base
}

type Coords struct {
	Row, Col int
	Dir      string
}

// PlayHuman validates and applies a human move. Returns applied move and score.
func (s *Session) PlayHuman(word string, c Coords) (*move.Move, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
		max := len(plays)
		if max > 30 {
			max = 30
		}
		for i := 0; i < max; i++ {
			pm := plays[i]
			if pm.Action() != move.MoveTypePlay {
				continue
			}
			r, col, v := pm.CoordsAndVertical()
			dir := "H"
			if v {
				dir = "V"
			}
			tiles := strings.ReplaceAll(pm.TilesString(), ".", "")
			log.Printf("cand[%d] %s @ (%d,%d,%s) score=%d", i, tiles, r, col, dir, pm.Score())
		}
		log.Printf("total candidates: %d", len(plays))
	}
	targetDir := strings.ToUpper(c.Dir)
	// First, try exact match on word and coordinates
	for _, pm := range plays {
		if pm.Action() != move.MoveTypePlay {
			continue
		}
		r, col, v := pm.CoordsAndVertical()
		dir := "H"
		if v {
			dir = "V"
		}
		if r == c.Row && col == c.Col && dir == targetDir {
			// TilesString includes anchors; compare word ignoring '.'
			tiles := strings.ReplaceAll(pm.TilesString(), ".", "")
			if equalWord(tiles, word) {
				player := int(s.Game.PlayerOnTurn())
				if err := s.Game.PlayMove(pm, true, 0); err != nil {
					return nil, err
				}
				s.recordPlayEvent(player, pm)
				s.maybeAutoChallenge()
				return pm, nil
			}
		}
	}
	// Fallback: if exactly one candidate exists at those coordinates/direction, accept it
	var only *move.Move
	for _, pm := range plays {
		if pm.Action() != move.MoveTypePlay {
			continue
		}
		r, col, v := pm.CoordsAndVertical()
		dir := "H"
		if v {
			dir = "V"
		}
		if r == c.Row && col == c.Col && dir == targetDir {
			if only != nil {
				only = nil
				break
			}
			only = pm
		}
	}
	if only != nil {
		if os.Getenv("DEBUG_MATCH") == "1" {
			log.Printf("fallback accepting candidate at coords; want=%q", word)
		}
		player := int(s.Game.PlayerOnTurn())
		if err := s.Game.PlayMove(only, true, 0); err != nil {
			return nil, err
		}
		s.recordPlayEvent(player, only)
		s.maybeAutoChallenge()
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
	if s == "" {
		return s
	}
	// Remove dots (anchors) just in case caller forgot
	s = strings.ReplaceAll(s, ".", "")
	var out strings.Builder
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r == '[' { // bracket token
			j := i + 1
			for j < len(rs) && rs[j] != ']' {
				j++
			}
			if j < len(rs) { // have closing bracket
				inner := string(rs[i+1 : j])
				// map digraph tokens and blanks in brackets to their letters
				switch inner {
				case "CH", "ch":
					out.WriteString("CH")
				case "LL", "ll":
					out.WriteString("LL")
				case "RR", "rr":
					out.WriteString("RR")
				default:
					if len(inner) == 1 {
						out.WriteString(strings.ToUpper(inner))
					} else {
						out.WriteString(strings.ToUpper(inner))
					}
				}
				i = j
				continue
			}
		}
		// Map single-letter FISE digraph symbols if present
		switch r {
		case 'Ç', 'ç':
			out.WriteString("CH")
		case 'K', 'k':
			out.WriteString("LL")
		case 'W', 'w':
			out.WriteString("RR")
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
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '[' || c == ']' {
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// Exchange tiles from current rack
func (s *Session) Exchange(letters string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rack := s.Game.RackFor(s.Game.PlayerOnTurn())
	// Verify availability
	mls, err := tilemapping.ToMachineLetters(letters, s.LD.TileMapping())
	if err != nil {
		return err
	}
	for _, ml := range mls {
		if rack.CountOf(ml) == 0 {
			return errors.New("tile not in rack")
		}
	}
	// Build a specific exchange move
	pi := s.Game.PlayerOnTurn()
	p := int(pi)
	em, err := s.sp.BaseTurnPlayer.NewExchangeMove(pi, letters)
	if err != nil {
		return err
	}
	if err := s.Game.PlayMove(em, true, 0); err != nil {
		return err
	}
	// Record minimal exchange event (score 0)
	s.appendHist(HistRow{Ply: len(s.hist) + 1, Player: int(p), Type: "EXCH", Word: letters, Row: 0, Col: 0, Dir: "", Score: 0, Cum: s.cumulativeFor(int(p))})
	return nil
}

// Pass the turn
func (s *Session) Pass() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pi := s.Game.PlayerOnTurn()
	p := int(pi)
	pm, err := s.sp.BaseTurnPlayer.NewPassMove(pi)
	if err != nil {
		return err
	}
	if err := s.Game.PlayMove(pm, true, 0); err != nil {
		return err
	}
	s.appendHist(HistRow{Ply: len(s.hist) + 1, Player: int(p), Type: "PASS", Word: "", Row: 0, Col: 0, Dir: "", Score: 0, Cum: s.cumulativeFor(int(p))})
	return nil
}

// AIMove makes an AI move using either static or simulation mode.
// Automatically detects endgame/preendgame scenarios and uses perfect algorithms.
func (s *Session) AIMove(mode AIMode, simIters, simPlies, topK, simThreads int) (*move.Move, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 🎯 AUTOMATIC ENDGAME/PREENDGAME DETECTION
	tr := s.Game.Bag().TilesRemaining()
	opp := s.Game.RackFor(s.Game.NextPlayer()).NumTiles()
	unseen := int(opp) + tr

	// Use perfect algorithms when applicable (following Macondo's elite bot logic)
	if unseen <= 7 {
		// ENDGAME: ≤7 tiles unseen - use perfect endgame solver
		return s.perfectEndgame()
	} else if unseen == 8 {
		// PREENDGAME: exactly 8 tiles unseen - use preendgame solver
		return s.perfectPreendgame()
	}

	// Continue with existing simulation logic for mid-game
	pi := s.Game.PlayerOnTurn()
	p := int(pi)
	rack := s.Game.RackFor(pi)
	// Allow exchange only if there are tiles left in the bag
	exchAllowed := s.Game.Bag().TilesRemaining() >= 1
	s.sp.MoveGenerator().GenAll(rack, exchAllowed)
	plays := s.sp.MoveGenerator().(*movegen.GordonGenerator).Plays()
	if len(plays) == 0 {
		return nil, errors.New("no plays")
	}
	var best *move.Move
	if mode == AIStatic {
		// pick by best equity (score + leave value), not by raw score
		if s.csc == nil {
			c, _ := equity.NewCombinedStaticCalculator(s.KwgName, s.CFG, equity.LeavesFilename, equity.PEGAdjustmentFilename)
			s.csc = c
		}
		moveEquity := func(pm *move.Move) float64 {
			if pm == nil {
				return -1e18
			}
			if eq := pm.Equity(); eq != 0 {
				return eq
			}
			lv := 0.0
			if s.csc != nil {
				lv = s.csc.LeaveValue(pm.Leave())
			}
			return float64(pm.Score()) + lv
		}
		best = plays[0]
		bestEq := moveEquity(best)
		for _, pm := range plays[1:] {
			if eq := moveEquity(pm); eq > bestEq {
				best, bestEq = pm, eq
			}
		}
	} else {
		// Simmer (equity-first candidates)
		if topK <= 0 || topK > len(plays) {
			topK = min(len(plays), 50)
		}
		// Ensure csc exists for leave values
		if s.csc == nil {
			c, _ := equity.NewCombinedStaticCalculator(s.KwgName, s.CFG, equity.LeavesFilename, equity.PEGAdjustmentFilename)
			s.csc = c
		}
		// Sort all moves by equity desc before slicing
		moveEquity := func(pm *move.Move) float64 {
			if pm == nil {
				return -1e18
			}
			if eq := pm.Equity(); eq != 0 {
				return eq
			}
			lv := 0.0
			if s.csc != nil {
				lv = s.csc.LeaveValue(pm.Leave())
			}
			return float64(pm.Score()) + lv
		}
		sort.SliceStable(plays, func(i, j int) bool { return moveEquity(plays[i]) > moveEquity(plays[j]) })
		cand := append([]*move.Move{}, plays[:topK]...)
		// Ensure we consider exchange (and pass) even if they didn't make topK
		if exchAllowed {
			var bestEx *move.Move
			for _, pm := range plays {
				if pm == nil {
					continue
				}
				if pm.Action() == move.MoveTypeExchange {
					bestEx = pm
					break
				}
			}
			if bestEx != nil {
				// Append if not already present
				seen := false
				for _, pm := range cand {
					if pm == bestEx {
						seen = true
						break
					}
				}
				if !seen {
					cand = append(cand, bestEx)
				}
			}
		}
		// Always consider pass as a safety candidate in sim if generated
		var passMv *move.Move
		for _, pm := range plays {
			if pm != nil && pm.Action() == move.MoveTypePass {
				passMv = pm
				break
			}
		}
		if passMv != nil {
			seen := false
			for _, pm := range cand {
				if pm == passMv {
					seen = true
					break
				}
			}
			if !seen {
				cand = append(cand, passMv)
			}
		}
		// Ensure csc exists (already ensured above)
		simmer := &montecarlo.Simmer{}
		simmer.Init(s.Game, []equity.EquityCalculator{s.csc}, s.csc, s.CFG)
		if simThreads <= 0 {
			simThreads = max(1, min(8, runtime.NumCPU()-1))
		}
		simmer.SetThreads(simThreads)
		// Enhanced default lookahead for stronger play
		if simPlies <= 0 {
			simPlies = 5
		}
		// Use confidence-based stopping to avoid wasting work when converged
		simmer.SetStoppingCondition(montecarlo.Stop99)
		simmer.SetAutostopCheckInterval(16) // More frequent checks for responsiveness
		if err := simmer.PrepareSim(simPlies, cand); err != nil {
			return nil, err
		}
		if simIters <= 0 {
			simIters = 2000 // Stronger default
		}

		// Use multi-threaded simulation when beneficial
		if simThreads > 1 {
			ctx := context.Background()
			if s.logWriter != nil {
				// Create a logger that writes to our log buffer for simulation
				logger := zerolog.New(s.logWriter).With().
					Timestamp().
					Str("component", "simulation").
					Logger()
				ctx = logger.WithContext(ctx)
			}
			if err := simmer.Simulate(ctx); err != nil {
				return nil, fmt.Errorf("multi-threaded simulation failed: %w", err)
			}
		} else {
			// Single-threaded simulation doesn't take context, but we can log start/end
			if s.logWriter != nil {
				s.logWriter.Write([]byte(fmt.Sprintf(`{"time":"%s","level":"info","component":"simulation","message":"Starting single-threaded simulation: %d iterations, %d plies"}`+"\n",
					time.Now().Format(time.RFC3339), simIters, simPlies)))
			}
			simmer.SimSingleThread(simIters, simPlies)
			if s.logWriter != nil {
				s.logWriter.Write([]byte(fmt.Sprintf(`{"time":"%s","level":"info","component":"simulation","message":"Single-threaded simulation completed"}`+"\n",
					time.Now().Format(time.RFC3339))))
			}
		}
		// Equity-first; use win% to break ties within epsilon
		winners := simmer.PlaysByWinProb().PlaysNoLock()
		if len(winners) == 0 {
			return nil, errors.New("no sim winner")
		}
		eps := 1.0
		bestPM := winners[0].Move()
		bestEq := moveEquity(bestPM)
		bestWP := winners[0].WinProb()
		for _, sp := range winners[1:] {
			pm := sp.Move()
			if pm == nil {
				continue
			}
			eq := moveEquity(pm)
			if eq > bestEq+eps || (math.Abs(eq-bestEq) <= eps && sp.WinProb() > bestWP) {
				bestPM = pm
				bestEq = eq
				bestWP = sp.WinProb()
			}
		}
		best = bestPM
	}
	if err := s.Game.PlayMove(best, true, 0); err != nil {
		return nil, err
	}
	s.recordPlayEvent(p, best)
	return best, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// recordPlayEvent appends a PLAY event to fallback history.
func (s *Session) recordPlayEvent(player int, pm *move.Move) {
	r, c, v := pm.CoordsAndVertical()
	dir := "H"
	if v {
		dir = "V"
	}
	// TilesString returns anchors and lowercase blanks; strip anchors
	tiles := strings.ReplaceAll(pm.TilesString(), ".", "")
	// Derive main word formed from the board after applying the move
	word := s.mainWordAt(r, c, dir)
	sc := pm.Score()
	// Cum score after this move for the player
	// We compute cumulative from game directly to be accurate
	cum := s.Game.PointsFor(player)
	if word == "" {
		word = tiles
	}
	s.appendHist(HistRow{Ply: len(s.hist) + 1, Player: player, Type: "PLAY", Word: word, Row: r, Col: c, Dir: dir, Score: sc, Cum: cum})
}

func (s *Session) appendHist(h HistRow) { s.hist = append(s.hist, h) }

// perfectEndgame uses Macondo's perfect endgame solver when ≤7 tiles remain unseen
func (s *Session) perfectEndgame() (*move.Move, error) {
	// Create bot configuration using existing session config
	botConfig := &bot.BotConfig{
		Config: *s.CFG,  // Use session's Macondo config
	}

	// Create BotTurnPlayer with SIMMING_BOT (has endgame + preendgame + simulation)
	botPlayer, err := bot.NewBotTurnPlayerFromGame(s.Game, botConfig, pb.BotRequest_SIMMING_BOT)
	if err != nil {
		return nil, fmt.Errorf("failed to create endgame bot: %w", err)
	}

	// Create context with custom logger for capturing logs
	ctx := context.Background()
	if s.logWriter != nil {
		// Create a logger that writes to our log buffer
		logger := zerolog.New(s.logWriter).With().
			Timestamp().
			Str("component", "endgame").
			Logger()
		ctx = logger.WithContext(ctx)
	}

	// BestPlay automatically detects endgame scenario and uses perfect solver
	bestMove, err := botPlayer.BestPlay(ctx)
	if err != nil {
		return nil, fmt.Errorf("endgame solver failed: %w", err)
	}

	// Add detailed logging to inspect the move object
	if s.logWriter != nil {
		logger := zerolog.New(s.logWriter).With().
			Timestamp().
			Str("component", "endgame").
			Logger()

		if bestMove == nil {
			logger.Info().Msg("endgame solver returned nil move")
			return nil, fmt.Errorf("endgame solver returned nil move")
		}

		// Log detailed move information
		logger.Info().
			Int("action", int(bestMove.Action())).
			Str("action_name", bestMove.MoveTypeString()).
			Int("score", bestMove.Score()).
			Str("tiles", bestMove.TilesString()).
			Msg("endgame move details")

		// If it's a play move, log coordinates
		if bestMove.Action() == move.MoveTypePlay {
			row, col, vert := bestMove.CoordsAndVertical()
			dir := "H"
			if vert {
				dir = "V"
			}
			logger.Info().
				Int("row", row).
				Int("col", col).
				Str("direction", dir).
				Msg("endgame play coordinates")
		}

		// Check if tiles string is empty which might indicate the issue
		if bestMove.TilesString() == "" {
			logger.Warn().Msg("endgame move has empty tiles string")
		}
	}

	// Apply the move to the game (similar to regular simulation)
	player := int(s.Game.PlayerOnTurn())
	if err := s.Game.PlayMove(bestMove, true, 0); err != nil {
		return nil, fmt.Errorf("failed to apply endgame move: %w", err)
	}
	s.recordPlayEvent(player, bestMove)

	// Log successful application
	if s.logWriter != nil {
		logger := zerolog.New(s.logWriter).With().
			Timestamp().
			Str("component", "endgame").
			Logger()
		logger.Info().Msg("endgame move applied successfully")
	}

	return bestMove, nil
}

// perfectPreendgame uses Macondo's preendgame solver when exactly 8 tiles remain unseen
func (s *Session) perfectPreendgame() (*move.Move, error) {
	// Create bot configuration using existing session config
	botConfig := &bot.BotConfig{
		Config: *s.CFG,  // Use session's Macondo config
	}

	// Create BotTurnPlayer with SIMMING_BOT (has preendgame capabilities)
	botPlayer, err := bot.NewBotTurnPlayerFromGame(s.Game, botConfig, pb.BotRequest_SIMMING_BOT)
	if err != nil {
		return nil, fmt.Errorf("failed to create preendgame bot: %w", err)
	}

	// Create context with custom logger for capturing logs
	ctx := context.Background()
	if s.logWriter != nil {
		// Create a logger that writes to our log buffer
		logger := zerolog.New(s.logWriter).With().
			Timestamp().
			Str("component", "preendgame").
			Logger()
		ctx = logger.WithContext(ctx)

		// Log preendgame entry details
		tr := s.Game.Bag().TilesRemaining()
		opp := s.Game.RackFor(s.Game.NextPlayer()).NumTiles()
		ourRack := s.Game.RackFor(s.Game.PlayerOnTurn())
		oppRack := s.Game.RackFor(s.Game.NextPlayer())
		spread := s.Game.SpreadFor(s.Game.PlayerOnTurn())

		s.logWriter.Write([]byte(fmt.Sprintf(`{"time":"%s","level":"info","component":"preendgame","message":"Entering preendgame solver","bag_remaining":%d,"opp_tiles":%d,"our_rack":"%s","opp_rack":"%s","spread":%d}`+"\n",
			time.Now().Format(time.RFC3339), tr, opp, ourRack.String(), oppRack.String(), spread)))
	}

	// BestPlay automatically detects preendgame scenario and uses specialized solver
	if s.logWriter != nil {
		s.logWriter.Write([]byte(fmt.Sprintf(`{"time":"%s","level":"info","component":"preendgame","message":"Calling BestPlay() - this may take time in preendgame"}`+"\n",
			time.Now().Format(time.RFC3339))))
	}

	move, err := botPlayer.BestPlay(ctx)

	if s.logWriter != nil {
		if err != nil {
			s.logWriter.Write([]byte(fmt.Sprintf(`{"time":"%s","level":"error","component":"preendgame","message":"BestPlay() failed","error":"%s"}`+"\n",
				time.Now().Format(time.RFC3339), err.Error())))
		} else {
			s.logWriter.Write([]byte(fmt.Sprintf(`{"time":"%s","level":"info","component":"preendgame","message":"BestPlay() completed successfully","move":"%s"}`+"\n",
				time.Now().Format(time.RFC3339), move.String())))
		}
	}

	if err != nil {
		return nil, fmt.Errorf("preendgame solver failed: %w", err)
	}

	// Apply the move to the game (similar to regular simulation and endgame)
	player := int(s.Game.PlayerOnTurn())
	if err := s.Game.PlayMove(move, true, 0); err != nil {
		return nil, fmt.Errorf("failed to apply preendgame move: %w", err)
	}
	s.recordPlayEvent(player, move)

	// Log successful application
	if s.logWriter != nil {
		s.logWriter.Write([]byte(fmt.Sprintf(`{"time":"%s","level":"info","component":"preendgame","message":"preendgame move applied successfully"}`+"\n",
			time.Now().Format(time.RFC3339))))
	}

	return move, nil
}

func (s *Session) cumulativeFor(player int) int { return s.Game.PointsFor(player) }

// SetLogWriter sets the log writer for capturing bot logs
func (s *Session) SetLogWriter(lw LogWriter) {
	s.logWriter = lw
}

// Abort force-ends the match without applying rack penalties. It sets history
// play state to GAME_OVER and records final scores as-is.
func (s *Session) Abort() {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.Game.History()
	if h == nil {
		return
	}
	if s.Game.Playing() == pb.PlayState_GAME_OVER {
		return
	}
	h.PlayState = pb.PlayState_GAME_OVER
	// Ensure final scores are recorded for UI snapshot
	s.Game.AddFinalScoresToHistory()
}

// maybeAutoChallenge auto-issues a challenge from the opponent under SINGLE/DOUBLE/etc when
// the last words formed are invalid (phony). It does nothing under VOID, and it never
// challenges valid plays.
func (s *Session) maybeAutoChallenge() {
	h := s.Game.History()
	if h == nil {
		return
	}
	if h.ChallengeRule == pb.ChallengeRule_VOID {
		return
	}
	evs := h.GetEvents()
	if len(evs) == 0 {
		return
	}
	last := evs[len(evs)-1]
	words := last.GetWordsFormed()
	if len(words) == 0 {
		return
	}
	alph := s.Game.Alphabet()
	// Build machine words to validate lexically
	mws := make([]tilemapping.MachineWord, 0, len(words))
	for _, w := range words {
		mw, err := tilemapping.ToMachineWord(w, alph)
		if err != nil {
			return
		}
		mws = append(mws, mw)
	}
	if err := s.Game.ValidateWords(s.Game.Lexicon(), mws); err != nil {
		// It's a phony: current onturn is the opponent, so issue challenge
		_, _ = s.Game.ChallengeEvent(0, 0)
	}
}

// ScoreRows returns a minimal scoresheet either from engine history or from fallback history.
func (s *Session) ScoreRows() []HistRow {
	h := s.Game.History()
	evs := h.GetEvents()
	if len(evs) > 0 {
		rows := make([]HistRow, 0, len(evs))
		for i, e := range evs {
			t := e.GetType().String()
			word := ""
			if ws := e.GetWordsFormed(); len(ws) > 0 {
				word = ws[0]
			}
			dir := "H"
			if e.GetDirection() == pb.GameEvent_VERTICAL {
				dir = "V"
			}
			rows = append(rows, HistRow{
				Ply: i + 1, Player: int(e.GetPlayerIndex()), Type: t, Word: word,
				Row: int(e.GetRow()), Col: int(e.GetColumn()), Dir: dir,
				Score: int(e.GetScore()), Cum: int(e.GetCumulative()),
			})
		}
		return rows
	}
	// fallback
	return append([]HistRow(nil), s.hist...)
}

// HistAppend appends a fallback history row (analysis/manual mode).
func (s *Session) HistAppend(hr HistRow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hist = append(s.hist, hr)
}

// mainWordAt scans the board starting at (row,col) along dir to extract the full
// word formed (including pre-existing anchors). Returns bracket-aware tokens
// from the game's Alphabet, e.g., "PAN[CH]O".
func (s *Session) mainWordAt(row, col int, dir string) string {
	b := s.Game.Board()
	alph := s.Game.Alphabet()
	r, c := row, col
	// Move to true start by walking backwards until empty
	if strings.ToUpper(dir) == "H" {
		for cc := c - 1; cc >= 0; cc-- {
			if b.GetLetter(r, cc) == 0 {
				break
			}
			c = cc
		}
		// Build forward
		var out strings.Builder
		for cc := c; cc < 15; cc++ {
			ml := b.GetLetter(r, cc)
			if ml == 0 {
				break
			}
			out.WriteString(alph.Letter(ml))
		}
		return out.String()
	}
	// Vertical
	for rr := r - 1; rr >= 0; rr-- {
		if b.GetLetter(rr, c) == 0 {
			break
		}
		r = rr
	}
	var out strings.Builder
	for rr := r; rr < 15; rr++ {
		ml := b.GetLetter(rr, c)
		if ml == 0 {
			break
		}
		out.WriteString(alph.Letter(ml))
	}
	return out.String()
}
