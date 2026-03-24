package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"dupman/backend/internal/match"
	aitp "github.com/domino14/macondo/ai/turnplayer"
	mconfig "github.com/domino14/macondo/config"
	"github.com/domino14/macondo/equity"
	"github.com/domino14/macondo/game"
	"github.com/domino14/macondo/gcgio"
	pb "github.com/domino14/macondo/gen/api/proto/macondo"
	"github.com/domino14/macondo/montecarlo"
	"github.com/domino14/macondo/move"
	"github.com/domino14/word-golib/tilemapping"
	"google.golang.org/protobuf/proto"
)

// LogBuffer captures zerolog output and broadcasts to SSE clients
type LogBuffer struct {
	mu        sync.RWMutex
	buffer    bytes.Buffer
	clients   map[chan string]bool
	sessionID string
}

func NewLogBuffer(sessionID string) *LogBuffer {
	return &LogBuffer{
		clients:   make(map[chan string]bool),
		sessionID: sessionID,
	}
}

func (lb *LogBuffer) Write(p []byte) (n int, err error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	// Write to internal buffer
	n, err = lb.buffer.Write(p)
	if err != nil {
		return n, err
	}

	// Broadcast to all SSE clients
	logLine := string(p)
	if len(strings.TrimSpace(logLine)) > 0 {
		for client := range lb.clients {
			select {
			case client <- logLine:
			default:
				// Client channel is full or closed, remove it
				delete(lb.clients, client)
				close(client)
			}
		}
	}

	return n, nil
}

func (lb *LogBuffer) AddClient(client chan string) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	lb.clients[client] = true
}

func (lb *LogBuffer) RemoveClient(client chan string) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	if _, exists := lb.clients[client]; exists {
		delete(lb.clients, client)
		close(client)
	}
}

func (lb *LogBuffer) GetBuffer() string {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	return lb.buffer.String()
}

type MatchHandlers struct {
	mu   sync.RWMutex
	byID map[string]*match.Session
	// Log buffers for each session
	logBuffers map[string]*LogBuffer
	eng        Engine
}

func NewMatchHandlers(eng Engine) *MatchHandlers {
	return &MatchHandlers{
		byID:       map[string]*match.Session{},
		logBuffers: map[string]*LogBuffer{},
		eng:        eng,
	}
}

// Abort ends the match immediately and marks it as GAME_OVER without rack penalties.
func (m *MatchHandlers) Abort(w http.ResponseWriter, r *http.Request) {
	id := m.pathID(r.URL.Path)
	m.mu.RLock()
	s := m.byID[id]
	m.mu.RUnlock()
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	s.Abort()
	writeJSON(w, http.StatusOK, m.serialize(s))
}

func (m *MatchHandlers) Create(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Ruleset   string `json:"ruleset"`
		KWG       string `json:"kwg"`
		Challenge string `json:"challenge"`
		Mode      string `json:"mode"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if strings.TrimSpace(in.KWG) == "" {
		// Prefer FILE2017 if present; fallback to FISE2016
		if p := findRootFile("FILE2017.kwg"); p != "" {
			in.KWG = p
		} else {
			in.KWG = findRootFile("FISE2016_converted.kwg")
		}
	} else {
		// If a bare filename was passed, try to resolve it similarly
		if p := findRootFile(in.KWG); p != "" {
			in.KWG = p
		}
	}
	id := genID("m")
	log.Printf("[Create] Creating new session: id=%s, ruleset=%s, kwg=%s", id, in.Ruleset, in.KWG)
	s, err := match.NewSession(id, in.Ruleset, in.KWG)
	if err != nil {
		log.Printf("[Create] ERROR creating session: %v", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	log.Printf("[Create] Session created successfully: id=%s", id)
	if strings.EqualFold(strings.TrimSpace(in.Mode), "analysis") {
		s.Analysis = true
		// Initialize empty manual board (15 rows of 15 spaces)
		for i := 0; i < 15; i++ {
			s.ManualBoardRows[i] = strings.Repeat(" ", 15)
		}
		s.ManualScore = [2]int{0, 0}
		// Seed analysis bag from distribution and draw 7 tiles per player
		s.AnalysisBag = make(map[string]int)
		alph := s.LD.TileMapping()
		dist := s.LD.Distribution()
		for i, ct := range dist {
			if ct == 0 {
				continue
			}
			key := alph.Letter(tilemapping.MachineLetter(i))
			s.AnalysisBag[key] += int(ct)
		}
		rand.Seed(time.Now().UnixNano())
		s.ManualRack[0] = drawFromBag(s.AnalysisBag, 7)
		s.ManualRack[1] = drawFromBag(s.AnalysisBag, 7)
		// Prefer accepting phonies: disable auto-challenge in analysis
		s.Game.SetChallengeRule(pb.ChallengeRule_VOID)
		// Seed Game racks to match manual defaults for both players
		s.Game.SetRackForOnly(0, tilemapping.RackFromString(s.ManualRack[0], s.LD.TileMapping()))
		s.Game.SetRackForOnly(1, tilemapping.RackFromString(s.ManualRack[1], s.LD.TileMapping()))
		s.AnalysisTurn = len(s.Game.History().Events)
	}
	// Optional: override challenge rule ("single" default, or "void")
	switch strings.ToLower(strings.TrimSpace(in.Challenge)) {
	case "void":
		s.Game.SetChallengeRule(pb.ChallengeRule_VOID)
	case "single", "":
		s.Game.SetChallengeRule(pb.ChallengeRule_SINGLE)
	}
	m.mu.Lock()
	m.byID[id] = s
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, m.serialize(s))
}

func (m *MatchHandlers) Get(w http.ResponseWriter, r *http.Request) {
	id := m.pathID(r.URL.Path)
	m.mu.RLock()
	s := m.byID[id]
	m.mu.RUnlock()
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusOK, m.serialize(s))
}

func (m *MatchHandlers) Play(w http.ResponseWriter, r *http.Request) {
	id := m.pathID(r.URL.Path)
	m.mu.RLock()
	s := m.byID[id]
	m.mu.RUnlock()
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	// Note: Row/Col must have individual JSON tags; a combined declaration with
	// a single tag string prevents proper decoding (would default to 0,0).
	var in struct {
		Word      string   `json:"word"`
		Row       int      `json:"row"`
		Col       int      `json:"col"`
		Dir       string   `json:"dir"`
		Tokens    []string `json:"tokens"`
		FreeInput bool     `json:"free_input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	origPlayer := int(s.Game.PlayerOnTurn())
	// Always capture the full rack BEFORE any modifications, so we can
	// store it when the rack was manually defined via set_rack.
	prePlayRack := s.Game.RackFor(origPlayer).String()
	var oldRack string
	// Capture the turn index BEFORE the move adds an event to history
	turnIdx := len(s.Game.History().Events)
	mv := Move{Word: normalizeWordToBrackets(in.Word), Row: in.Row, Col: in.Col, Dir: strings.ToUpper(in.Dir)}
	if in.FreeInput {
		var errPrep error
		oldRack, errPrep = m.prepareFreeInputRack(s, mv, in.Tokens)
		if errPrep != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": errPrep.Error()})
			return
		}
	}
	_, err := s.PlayHuman(in.Word, matchCoords(in.Row, in.Col, in.Dir))
	if err != nil {
		if in.FreeInput {
			revertFreeInput(s, origPlayer, oldRack)
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// Store the full rack when it was explicitly defined by the user
	// (via set_rack). Works for both free_input and normal play paths:
	// the user may select a generated move or input directly on the board.
	if s.ManualRackFlag(origPlayer) {
		s.SetFullRack(turnIdx, prePlayRack)
	}
	s.SetManualRackFlag(origPlayer, false)
	if in.FreeInput {
		nextPlayer := s.Game.PlayerOnTurn()
		if _, err := s.Game.SetRandomRack(nextPlayer, nil); err != nil {
			log.Printf("[Play] Warning: could not set random rack for player %d: %v", nextPlayer, err)
		}
	}
	writeJSON(w, http.StatusOK, m.serialize(s))
}

// AcceptLivePlay force-applies a move even if it wasn't recognized by PlayHuman (e.g., phonies).
func (m *MatchHandlers) AcceptLivePlay(w http.ResponseWriter, r *http.Request) {
	id := m.pathID(r.URL.Path)
	m.mu.RLock()
	s := m.byID[id]
	m.mu.RUnlock()
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if s.Analysis {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "use analysis apply endpoint"})
		return
	}
	var in struct {
		Word      string   `json:"word"`
		Row       int      `json:"row"`
		Col       int      `json:"col"`
		Dir       string   `json:"dir"`
		Tokens    []string `json:"tokens"`
		FreeInput bool     `json:"free_input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	mv := Move{Word: normalizeWordToBrackets(in.Word), Row: in.Row, Col: in.Col, Dir: strings.ToUpper(in.Dir)}
	if mv.Dir != "H" && mv.Dir != "V" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid direction"})
		return
	}
	var oldRack string
	origPlayer := int(s.Game.PlayerOnTurn())
	// Always capture the full rack BEFORE any modifications.
	prePlayRack := s.Game.RackFor(origPlayer).String()
	turnIdx := len(s.Game.History().Events)
	if in.FreeInput {
		var errPrep error
		oldRack, errPrep = m.prepareFreeInputRack(s, mv, in.Tokens)
		if errPrep != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": errPrep.Error()})
			return
		}
	}
	tiles, err := buildTilesForMove(s, mv, in.Tokens)
	if err != nil {
		if in.FreeInput {
			revertFreeInput(s, origPlayer, oldRack)
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	coords := move.ToBoardGameCoords(mv.Row, mv.Col, mv.Dir == "V")
	rack := s.Game.RackFor(s.Game.PlayerOnTurn()).String()
	play, err := s.Game.CreateAndScorePlacementMove(coords, tiles, rack, false)
	if err != nil {
		if in.FreeInput {
			revertFreeInput(s, origPlayer, oldRack)
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.Game.PlayMove(play, true, 0); err != nil {
		if in.FreeInput {
			revertFreeInput(s, origPlayer, oldRack)
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.RecordPlayEvent(origPlayer, play)
	// Store the full rack when it was explicitly defined by the user
	// (via set_rack). Works for both free_input and normal play paths.
	if s.ManualRackFlag(origPlayer) {
		s.SetFullRack(turnIdx, prePlayRack)
	}
	s.SetManualRackFlag(origPlayer, false)
	if in.FreeInput {
		nextPlayer := s.Game.PlayerOnTurn()
		if _, err := s.Game.SetRandomRack(nextPlayer, nil); err != nil {
			log.Printf("[AcceptLivePlay] Warning: could not set random rack for player %d: %v", nextPlayer, err)
		}
	}
	writeJSON(w, http.StatusOK, m.serialize(s))
}

func (m *MatchHandlers) Exchange(w http.ResponseWriter, r *http.Request) {
	id := m.pathID(r.URL.Path)
	m.mu.RLock()
	s := m.byID[id]
	m.mu.RUnlock()
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	var in struct {
		Tiles string `json:"tiles"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	// Capture the player and turn index BEFORE the exchange modifies them
	player := int(s.Game.PlayerOnTurn())
	preRack := s.Game.RackFor(player).String()
	turnIdx := len(s.Game.History().Events)
	if err := s.Exchange(in.Tiles); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// Only store the full rack when it was explicitly defined by the user
	if s.ManualRackFlag(player) {
		s.SetFullRack(turnIdx, preRack)
	}
	s.SetManualRackFlag(player, false)
	writeJSON(w, http.StatusOK, m.serialize(s))
}

func (m *MatchHandlers) Pass(w http.ResponseWriter, r *http.Request) {
	id := m.pathID(r.URL.Path)
	m.mu.RLock()
	s := m.byID[id]
	m.mu.RUnlock()
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	// Capture the player and turn index BEFORE the pass adds an event
	player := int(s.Game.PlayerOnTurn())
	preRack := s.Game.RackFor(player).String()
	turnIdx := len(s.Game.History().Events)
	if err := s.Pass(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// Only store the full rack when it was explicitly defined by the user
	if s.ManualRackFlag(player) {
		s.SetFullRack(turnIdx, preRack)
	}
	s.SetManualRackFlag(player, false)
	writeJSON(w, http.StatusOK, m.serialize(s))
}

// ChallengePhony handles a phony challenge: force-plays the rejected word
// onto the board and immediately issues a ChallengeEvent to remove it.
// This produces the native Macondo TILE_PLACEMENT + PHONY_TILES_RETURNED
// sequence in the game history and GCG output.
func (m *MatchHandlers) ChallengePhony(w http.ResponseWriter, r *http.Request) {
	id := m.pathID(r.URL.Path)
	m.mu.RLock()
	s := m.byID[id]
	m.mu.RUnlock()
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if s.Analysis {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not available in analysis mode"})
		return
	}
	var in struct {
		Word        string   `json:"word"`
		Row         int      `json:"row"`
		Col         int      `json:"col"`
		Dir         string   `json:"dir"`
		Tokens      []string `json:"tokens"`
		FreeInput   bool     `json:"free_input"`
		DisplayWord string   `json:"display_word"` // human-readable form for annotation
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}

	// --- Step 1: Force-play the phony onto the board (same as AcceptLivePlay). ---
	// Always prepare the rack from the placed tiles so the challenge works
	// even when the tiles don't match the current rack (free input mode).
	mv := Move{Word: normalizeWordToBrackets(in.Word), Row: in.Row, Col: in.Col, Dir: strings.ToUpper(in.Dir)}
	if mv.Dir != "H" && mv.Dir != "V" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid direction"})
		return
	}
	origPlayer := int(s.Game.PlayerOnTurn())
	prePlayRack := s.Game.RackFor(origPlayer).String()
	turnIdx := len(s.Game.History().Events)
	oldRack, errPrep := m.prepareFreeInputRack(s, mv, in.Tokens)
	if errPrep != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": errPrep.Error()})
		return
	}
	tiles, err := buildTilesForMove(s, mv, in.Tokens)
	if err != nil {
		revertFreeInput(s, origPlayer, oldRack)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	coords := move.ToBoardGameCoords(mv.Row, mv.Col, mv.Dir == "V")
	rack := s.Game.RackFor(s.Game.PlayerOnTurn()).String()
	play, err := s.Game.CreateAndScorePlacementMove(coords, tiles, rack, false)
	if err != nil {
		revertFreeInput(s, origPlayer, oldRack)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.Game.PlayMove(play, true, 0); err != nil {
		revertFreeInput(s, origPlayer, oldRack)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.RecordPlayEvent(origPlayer, play)
	if s.ManualRackFlag(origPlayer) {
		s.SetFullRack(turnIdx, prePlayRack)
	}
	s.SetManualRackFlag(origPlayer, false)

	// --- Step 2: Immediately challenge the play we just placed. ---
	evtIdx, err := s.ChallengeLastPlay()
	if err != nil {
		// ChallengeEvent failed; the phony is still on the board.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// Record the challenged word for scoresheet / GCG annotation.
	displayWord := strings.TrimSpace(in.DisplayWord)
	if displayWord == "" {
		displayWord = strings.TrimSpace(in.Word)
	}
	if displayWord != "" {
		s.SetChallengedWord(evtIdx, displayWord)
	}

	writeJSON(w, http.StatusOK, m.serialize(s))
}

func (m *MatchHandlers) AIMove(w http.ResponseWriter, r *http.Request) {
	id := m.pathID(r.URL.Path)
	m.mu.RLock()
	s := m.byID[id]
	m.mu.RUnlock()
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	var in struct {
		Mode string                                     `json:"mode"`
		Sim  *struct{ Iters, Plies, TopK, Threads int } `json:"sim"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	mode := match.AIStatic
	if strings.EqualFold(in.Mode, "sim") {
		mode = match.AISim
	}
	iters, plies, topk, threads := 0, 0, 0, 0
	if in.Sim != nil {
		iters = in.Sim.Iters
		plies = in.Sim.Plies
		topk = in.Sim.TopK
		threads = in.Sim.Threads
	}

	// Set up log writer for this session
	logBuffer := m.GetLogBuffer(id)
	s.SetLogWriter(logBuffer)

	_, err := s.AIMove(mode, iters, plies, topk, threads)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, m.serialize(s))
}

func (m *MatchHandlers) pathID(p string) string {
	parts := strings.Split(strings.TrimSuffix(p, "/"), "/")
	if len(parts) >= 3 {
		return parts[2]
	}
	return ""
}

func matchCoords(r, c int, d string) match.Coords {
	return match.Coords{Row: r, Col: c, Dir: strings.ToUpper(d)}
}

// spanishEndgameAdj inspects the game history for an END_RACK_PTS event and
// returns the Spanish-style corrected scores.  Macondo adds 2× the opponent's
// rack value to the closer; Spanish rules add 1× to the closer and subtract
// 1× from the opponent.  If no adjustment applies, ok is false.
//
// NOTE: We do NOT gate on h.PlayState == GAME_OVER because Macondo's
// endgame solver can leave PlayState in an inconsistent state (e.g.
// PLAYING) even after the END_RACK_PTS event has been recorded.  The
// presence of the event itself is the authoritative signal.
func spanishEndgameAdj(h *pb.GameHistory) (scores [2]int, rackPts int, closer int, rackStr string, ok bool) {
	if h == nil {
		return
	}
	for _, ev := range h.GetEvents() {
		if ev.GetType() == pb.GameEvent_END_RACK_PTS {
			endPts := int(ev.GetEndRackPoints()) // 2× actual rack value
			rackPts = endPts / 2
			closer = int(ev.GetPlayerIndex())
			opponent := 1 - closer
			rackStr = ev.GetRack()

			// Macondo state: closer got +2×rackPts, opponent unchanged.
			// Spanish: closer should get +1×rackPts, opponent −1×rackPts.
			// So: corrected_closer = macondo_closer − rackPts
			//     corrected_opp    = macondo_opp    − rackPts
			if len(h.FinalScores) == 2 {
				scores[closer] = int(h.FinalScores[closer]) - rackPts
				scores[opponent] = int(h.FinalScores[opponent]) - rackPts
			} else {
				// Fallback: compute from the event's cumulative.
				// The closer's cumulative already includes +2×rackPts.
				scores[closer] = int(ev.GetCumulative()) - rackPts
				// For the opponent, scan backwards for their last cumulative.
				for i := len(h.GetEvents()) - 1; i >= 0; i-- {
					prev := h.GetEvents()[i]
					if int(prev.GetPlayerIndex()) == opponent && prev.GetType() != pb.GameEvent_END_RACK_PTS {
						scores[opponent] = int(prev.GetCumulative()) - rackPts
						break
					}
				}
			}
			ok = true
			return
		}
	}
	return
}

func (m *MatchHandlers) serialize(s *match.Session) map[string]any {
	// minimal snapshot
	out := map[string]any{
		"id":       s.ID,
		"ruleset":  s.Ruleset,
		"lexicon":  s.Lexicon,
		"turn":     s.Game.PlayerOnTurn(),
		"bag":      s.Game.Bag().TilesRemaining(),
		"score":    []int{s.Game.PointsFor(0), s.Game.PointsFor(1)},
		"ver":      1,
		"analysis": s.Analysis,
	}
	// expose basic game state/winner if available
	if h := s.Game.History(); h != nil {
		out["play_state"] = h.PlayState.String()

		// Apply Spanish-style endgame score correction when applicable.
		if scores, _, _, _, ok := spanishEndgameAdj(h); ok {
			out["score"] = []int{scores[0], scores[1]}
			out["final_score"] = []int{scores[0], scores[1]}
			// Ensure play_state reflects GAME_OVER when endgame calcs
			// have run, even if Macondo's PlayState is stale.
			out["play_state"] = pb.PlayState_GAME_OVER.String()
			if scores[0] > scores[1] {
				out["winner"] = int32(0)
			} else if scores[1] > scores[0] {
				out["winner"] = int32(1)
			} else {
				out["winner"] = int32(-1)
			}
		} else {
			out["winner"] = h.Winner
			if len(h.FinalScores) == 2 {
				out["final_score"] = []int{int(h.FinalScores[0]), int(h.FinalScores[1])}
			}
		}
	}
	// board rows: 15 strings, spaces for empty
	rows := make([]string, 15)
	bonus := make([]string, 15)
	if s.Analysis {
		// Use manual board rows when in analysis mode
		copy(rows, s.ManualBoardRows[:])
		// Bonus layout from current game rules for rendering
		alph := s.Game.Alphabet()
		_ = alph // suppress unused if not used below
		for r := 0; r < 15; r++ {
			var bb strings.Builder
			for c := 0; c < 15; c++ {
				b := s.Game.Board().GetBonus(r, c)
				bb.WriteByte(byte(b))
			}
			bonus[r] = bb.String()
		}
		// Override score with manual score if any non-zero
		out["score"] = []int{s.ManualScore[0], s.ManualScore[1]}
	} else {
		alph := s.Game.Alphabet()
		for r := 0; r < 15; r++ {
			var sb strings.Builder
			var bb strings.Builder
			for c := 0; c < 15; c++ {
				ml := s.Game.Board().GetLetter(r, c)
				if ml == 0 {
					sb.WriteByte(' ')
				} else {
					sb.WriteString(alph.Letter(ml))
				}
				// write bonus square code as a single byte (per board.BonusSquare rune)
				b := s.Game.Board().GetBonus(r, c)
				bb.WriteByte(byte(b))
			}
			rows[r] = sb.String()
			bonus[r] = bb.String()
		}
	}
	out["board_rows"] = rows
	out["bonus_rows"] = bonus
	// rack: always include both racks for sim mode visualization
	// rack = current player on turn, rack_you = player 0, rack_bot = player 1
	cur := s.Game.PlayerOnTurn()
	if cur < 0 || cur > 1 {
		cur = 0
	}
	out["rack"] = s.Game.RackFor(cur).String()
	out["rack_you"] = s.Game.RackFor(0).String()
	out["rack_bot"] = s.Game.RackFor(1).String()
	return out
}

// SetRack sets the rack for a given player (or current on-turn) in Game. Works in vs-bot and analysis.
func (m *MatchHandlers) SetRack(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid rack", "detail": rec})
		}
	}()
	id := m.pathID(r.URL.Path)
	m.mu.RLock()
	s := m.byID[id]
	m.mu.RUnlock()
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	var in struct {
		Player *int   `json:"player"`
		Rack   string `json:"rack"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	p := s.Game.PlayerOnTurn()
	if in.Player != nil && (*in.Player == 0 || *in.Player == 1) {
		p = *in.Player
	}
	desired := strings.TrimSpace(in.Rack)
	// Normalize desired rack to a format acceptable by RackFromString (uppercase letters, [CH]/[LL]/[RR], '?' for blanks)
	normalizeRack := func(s string) string {
		toks := tokenizeRow(normalizeWordToBrackets(s))
		var b strings.Builder
		for _, tk := range toks {
			if strings.TrimSpace(tk) == "" {
				continue
			}
			if strings.HasPrefix(tk, "[") && strings.HasSuffix(tk, "]") {
				inner := strings.ToUpper(tk[1 : len(tk)-1])
				switch inner {
				case "CH", "LL", "RR":
					b.WriteString("[" + inner + "]")
				default:
					b.WriteString(inner)
				}
				continue
			}
			b.WriteString(strings.ToUpper(tk))
		}
		return b.String()
	}
	desired = normalizeRack(desired)
	desiredRack := tilemapping.RackFromString(desired, s.LD.TileMapping())

	// Debug: log before setting rack
	log.Printf("[SetRack] Player %d, desired rack: %s, current rack_0: %s, rack_1: %s, bag tiles: %d",
		p, desired, s.Game.RackFor(0).String(), s.Game.RackFor(1).String(), s.Game.Bag().TilesRemaining())

	// Strategy: To allow free rack definition (especially in analysis mode), we need to:
	// 1. Return BOTH racks to the bag (to make all tiles available)
	// 2. Set the desired rack for player p
	// 3. Restore the opponent's rack from what it was before

	// Save opponent's rack before modifications
	oppIdx := 1 - p
	oppRackStr := s.Game.RackFor(oppIdx).String()

	// Return both racks to the bag to make all tiles available
	s.Game.ThrowRacksIn()

	// Set the desired rack for player p
	if err := s.Game.SetRackForOnly(p, desiredRack); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tiles not available in bag", "detail": err.Error()})
		return
	}

	// Restore opponent's rack (if it's valid and tiles are available)
	if strings.TrimSpace(oppRackStr) != "" {
		oppRack := tilemapping.RackFromString(oppRackStr, s.LD.TileMapping())
		if oppRack != nil {
			if err := s.Game.SetRackForOnly(oppIdx, oppRack); err != nil {
				// If opponent's rack can't be restored (tiles not available), log but continue
				log.Printf("[SetRack] Warning: could not restore opponent rack %s: %v", oppRackStr, err)
			}
		}
	}

	// Debug: log after setting rack
	log.Printf("[SetRack] After setting racks - rack_0: %s, rack_1: %s, bag tiles: %d",
		s.Game.RackFor(0).String(), s.Game.RackFor(1).String(), s.Game.Bag().TilesRemaining())

	// Mark that this player's rack was explicitly defined by the user,
	// so Play/Exchange/Pass know to store it as the full rack for that turn.
	s.SetManualRackFlag(p, true)

	writeJSON(w, http.StatusOK, m.serialize(s))
}

// Bag returns a detailed breakdown letter->count for the bag
func (m *MatchHandlers) Bag(w http.ResponseWriter, r *http.Request) {
	id := m.pathID(r.URL.Path)
	m.mu.RLock()
	s := m.byID[id]
	m.mu.RUnlock()
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	tm := s.Game.Bag().PeekMap() // index: MachineLetter, value: count
	alph := s.Game.Alphabet()
	tiles := make([][2]any, 0, len(tm))
	for i, ct := range tm {
		if ct == 0 {
			continue
		}
		letter := alph.Letter(tilemapping.MachineLetter(i))
		tiles = append(tiles, [2]any{letter, ct})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":        s.ID,
		"remaining": s.Game.Bag().TilesRemaining(),
		"tiles":     tiles,
	})
}

// Unseen returns counts of tiles not visible to the human player:
// bag contents + opponent rack. Also includes opponent rack size and totals.
func (m *MatchHandlers) Unseen(w http.ResponseWriter, r *http.Request) {
	id := m.pathID(r.URL.Path)
	m.mu.RLock()
	s := m.byID[id]
	m.mu.RUnlock()
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	// Analysis mode: if no historical turn requested, use analysis bag + opponent rack
	if s.Analysis {
		playerParam := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("player")))
		useOnTurn := playerParam == "onturn"
		viewIdx := 0
		switch playerParam {
		case "bot", "1":
			viewIdx = 1
		}
		if useOnTurn {
			viewIdx = int(s.Game.PlayerOnTurn())
			if viewIdx < 0 || viewIdx > 1 {
				viewIdx = 0
			}
		}
		tparam := strings.TrimSpace(r.URL.Query().Get("turn"))
		if tparam == "" {
			// Use Game bag and opponent rack like vs-bot
			alph := s.Game.Alphabet()
			bagMap := s.Game.Bag().PeekMap()
			counts := map[string]int{}
			for i, ct := range bagMap {
				if ct > 0 {
					counts[alph.Letter(tilemapping.MachineLetter(i))] += int(ct)
				}
			}
			if viewIdx < 0 || viewIdx > 1 {
				viewIdx = 0
			}
			oppIdx := 1 - viewIdx
			oppRack := s.Game.RackFor(oppIdx)
			for _, ml := range oppRack.TilesOn() {
				counts[alph.Letter(ml)]++
			}
			oppTiles := int(oppRack.NumTiles())
			tiles := make([][2]any, 0, len(counts))
			bagRem := 0
			for k, v := range counts {
				tiles = append(tiles, [2]any{k, v})
				bagRem += v
			}
			writeJSON(w, http.StatusOK, map[string]any{"id": s.ID, "bag_remaining": bagRem, "opp_rack": oppTiles, "total_unseen": bagRem + oppTiles, "tiles": tiles})
			return
		} else {
			// Historical unseen via Game history
			turn := 0
			if n, err := strconv.Atoi(tparam); err == nil && n >= 0 {
				turn = n
			}
			hist := s.Game.History()
			rules := s.Game.Rules()
			ng, err := game.NewFromHistory(hist, rules, 0)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			if turn > len(hist.Events) {
				turn = len(hist.Events)
			}
			if err := ng.PlayToTurn(turn); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			playerParam := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("player")))
			useOnTurn := playerParam == "onturn"
			viewIdx := 0
			switch playerParam {
			case "bot", "1":
				viewIdx = 1
			}
			if useOnTurn {
				viewIdx = int(ng.PlayerOnTurn())
				if viewIdx < 0 || viewIdx > 1 {
					viewIdx = 0
				}
			}
			alph := ng.Alphabet()
			bagMap := ng.Bag().PeekMap()
			counts := map[string]int{}
			for i, ct := range bagMap {
				if ct > 0 {
					counts[alph.Letter(tilemapping.MachineLetter(i))] += int(ct)
				}
			}
			tiles := make([][2]any, 0, len(counts))
			bagRem := 0
			for k, v := range counts {
				tiles = append(tiles, [2]any{k, v})
				bagRem += v
			}
			if viewIdx < 0 || viewIdx > 1 {
				viewIdx = 0
			}
			oppIdx := 1 - viewIdx
			oppTiles := int(ng.RackFor(oppIdx).NumTiles())
			writeJSON(w, http.StatusOK, map[string]any{"id": s.ID, "bag_remaining": bagRem, "opp_rack": oppTiles, "total_unseen": bagRem + oppTiles, "tiles": tiles})
			return
		}
	}
	// Optional historical turn for analysis mode
	turn := -1
	if t := r.URL.Query().Get("turn"); t != "" {
		if n, err := strconv.Atoi(t); err == nil && n >= 0 {
			turn = n
		}
	}
	if turn >= 0 {
		// Historical unseen at a given turn
		hist := s.Game.History()
		rules := s.Game.Rules()
		ng, err := game.NewFromHistory(hist, rules, 0)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if turn > len(hist.Events) {
			turn = len(hist.Events)
		}
		if err := ng.PlayToTurn(turn); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		playerParam := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("player")))
		useOnTurn := playerParam == "onturn"
		viewIdx := 0
		switch playerParam {
		case "bot", "1":
			viewIdx = 1
		}
		if useOnTurn {
			viewIdx = int(ng.PlayerOnTurn())
			if viewIdx < 0 || viewIdx > 1 {
				viewIdx = 0
			}
		}
		alph := ng.Alphabet()
		bagMap := ng.Bag().PeekMap()
		counts := map[string]int{}
		for i, ct := range bagMap {
			if ct == 0 {
				continue
			}
			letter := alph.Letter(tilemapping.MachineLetter(i))
			counts[letter] += int(ct)
		}
		if viewIdx < 0 || viewIdx > 1 {
			viewIdx = 0
		}
		oppIdx := 1 - viewIdx
		oppRack := ng.RackFor(oppIdx)
		for _, ml := range oppRack.TilesOn() {
			letter := alph.Letter(ml)
			counts[letter] += 1
		}
		tiles := make([][2]any, 0, len(counts))
		for k, v := range counts {
			tiles = append(tiles, [2]any{k, v})
		}
		bagRem := ng.Bag().TilesRemaining()
		oppTiles := int(oppRack.NumTiles())
		writeJSON(w, http.StatusOK, map[string]any{
			"id":            s.ID,
			"bag_remaining": bagRem,
			"opp_rack":      oppTiles,
			"total_unseen":  bagRem + oppTiles,
			"tiles":         tiles,
		})
		return
	}
	// Live unseen (current state)
	{
		playerParam := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("player")))
		useOnTurn := playerParam == "onturn"
		viewIdx := 0
		switch playerParam {
		case "bot", "1":
			viewIdx = 1
		}
		if useOnTurn {
			viewIdx = int(s.Game.PlayerOnTurn())
			if viewIdx < 0 || viewIdx > 1 {
				viewIdx = 0
			}
		}
		alph := s.Game.Alphabet()
		bagMap := s.Game.Bag().PeekMap()
		counts := map[string]int{}
		for i, ct := range bagMap {
			if ct == 0 {
				continue
			}
			letter := alph.Letter(tilemapping.MachineLetter(i))
			counts[letter] += int(ct)
		}
		if viewIdx < 0 || viewIdx > 1 {
			viewIdx = 0
		}
		oppIdx := 1 - viewIdx
		oppRack := s.Game.RackFor(oppIdx)
		for _, ml := range oppRack.TilesOn() {
			letter := alph.Letter(ml)
			counts[letter] += 1
		}
		tiles := make([][2]any, 0, len(counts))
		for k, v := range counts {
			tiles = append(tiles, [2]any{k, v})
		}
		bagRem := s.Game.Bag().TilesRemaining()
		oppTiles := int(oppRack.NumTiles())
		writeJSON(w, http.StatusOK, map[string]any{
			"id":            s.ID,
			"bag_remaining": bagRem,
			"opp_rack":      oppTiles,
			"total_unseen":  bagRem + oppTiles,
			"tiles":         tiles,
		})
	}
}

// ScoreSheet returns a minimal move history
func (m *MatchHandlers) ScoreSheet(w http.ResponseWriter, r *http.Request) {
	id := m.pathID(r.URL.Path)
	m.mu.RLock()
	s := m.byID[id]
	m.mu.RUnlock()
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	type Row struct {
		Ply    int    `json:"ply"`
		Player int    `json:"player"`
		Type   string `json:"type"`
		Word   string `json:"word"`
		Played string `json:"played"`
		Row    int    `json:"row"`
		Col    int    `json:"col"`
		Dir    string `json:"dir"`
		Score  int    `json:"score"`
		Cum    int    `json:"cum"`
		Note   string `json:"note,omitempty"`
	}
	sr := s.ScoreRows()
	rows := make([]Row, 0, len(sr)+3)
	// Use sr for Word (has anchor/blank notation), but get PlayedTiles from macondo events
	hist := s.Game.History()
	evs := hist.GetEvents()

	// Track each player's last cumulative (before endgame adjustments).
	lastCum := [2]int{0, 0}

	for i, e := range sr {
		// Skip Macondo's native END_RACK_PTS event — we'll replace it with
		// Spanish-style adjustment rows below.
		if e.Type == "END_RACK_PTS" {
			continue
		}

		// Merge PHONY_TILES_RETURNED with its preceding TILE_PLACEMENT_MOVE:
		// the user never saw the phony score, so show net zero with a note.
		if e.Type == "PHONY_TILES_RETURNED" {
			cw := s.ChallengedWordAt(i)
			if cw == "" {
				cw = e.Word
			}
			note := "Phony impugnado: " + cw
			// Replace the preceding TILE_PLACEMENT row (the phony play)
			// with a single merged row showing score=0.
			if len(rows) > 0 && rows[len(rows)-1].Type == "TILE_PLACEMENT_MOVE" &&
				rows[len(rows)-1].Player == e.Player {
				prev := &rows[len(rows)-1]
				prev.Type = "PHONY_TILES_RETURNED"
				prev.Score = 0
				prev.Cum = e.Cum // restored cumulative (equals pre-play cum)
				prev.Note = note
				lastCum[e.Player] = e.Cum
			} else {
				// Fallback: standalone row (shouldn't normally happen)
				rows = append(rows, Row{Ply: e.Ply, Player: e.Player, Type: e.Type,
					Word: cw, Score: 0, Cum: e.Cum, Note: note})
				lastCum[e.Player] = e.Cum
			}
			continue
		}

		played := ""
		if i < len(evs) {
			ev := evs[i]
			played = ev.GetPlayedTiles()
			if e.Type == "EXCHANGE" {
				played = ev.GetExchanged()
			}
		}
		note := ""
		if e.Type == "PASS" {
			// Legacy: in case old sessions still have PASS-based challenges.
			if cw := s.ChallengedWordAt(i); cw != "" {
				note = "Phony impugnado: " + cw
			}
		}
		rows = append(rows, Row{Ply: e.Ply, Player: e.Player, Type: e.Type, Word: e.Word, Played: played, Row: e.Row, Col: e.Col, Dir: e.Dir, Score: e.Score, Cum: e.Cum, Note: note})

		// Track cumulative per player (only for regular events, not endgame).
		lastCum[e.Player] = e.Cum
	}

	// Renumber plys sequentially so the frontend round grouping
	// (idx = floor((ply-1)/2)) stays correct after PHONY merges.
	for i := range rows {
		rows[i].Ply = i + 1
	}

	// Append Spanish-style endgame adjustment rows if the game is over.
	if scores, rackPts, closer, rackStr, ok := spanishEndgameAdj(hist); ok {
		opponent := 1 - closer
		ply := len(rows) + 1
		// Opponent row: loses rackPts
		rows = append(rows, Row{
			Ply: ply, Player: opponent, Type: "END_RACK_PTS",
			Word: "-" + rackStr, Score: -rackPts, Cum: lastCum[opponent] - rackPts,
		})
		ply++
		// Closer row: gains rackPts
		rows = append(rows, Row{
			Ply: ply, Player: closer, Type: "END_RACK_PTS",
			Word: "(+" + rackStr + ")", Score: rackPts, Cum: lastCum[closer] + rackPts,
		})
		ply++
		// Final score row
		rows = append(rows, Row{
			Ply: ply, Type: "FINAL_SCORE",
			Note: fmt.Sprintf("Puntaje final: %d – %d", scores[0], scores[1]),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"id": s.ID, "rows": rows})
}

// Events returns the list of engine events (no synthetic rows), with compact fields.
func (m *MatchHandlers) Events(w http.ResponseWriter, r *http.Request) {
	id := m.pathID(r.URL.Path)
	m.mu.RLock()
	s := m.byID[id]
	m.mu.RUnlock()
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	evs := s.Game.History().GetEvents()
	type Ev struct {
		Ply    int    `json:"ply"`
		Player int    `json:"player"`
		Type   string `json:"type"`
		Row    int    `json:"row"`
		Col    int    `json:"col"`
		Dir    string `json:"dir"`
		Word   string `json:"word"`
	}
	out := make([]Ev, 0, len(evs))
	for i, e := range evs {
		dir := "H"
		if e.GetDirection() == pb.GameEvent_VERTICAL {
			dir = "V"
		}
		word := ""
		if ws := e.GetWordsFormed(); len(ws) > 0 {
			word = ws[0]
		}
		// For exchanges, put the exchanged tiles in the word field
		t := e.GetType().String()
		if t == "EXCHANGE" {
			word = e.GetExchanged()
		}
		out = append(out, Ev{Ply: i + 1, Player: int(e.GetPlayerIndex()), Type: t, Row: int(e.GetRow()), Col: int(e.GetColumn()), Dir: dir, Word: word})
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": s.ID, "count": len(out), "events": out})
}

// SetRackAnalysis sets a manual rack for a player in analysis mode.
func (m *MatchHandlers) SetRackAnalysis(w http.ResponseWriter, r *http.Request) {
	id := m.pathID(r.URL.Path)
	m.mu.RLock()
	s := m.byID[id]
	m.mu.RUnlock()
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if !s.Analysis {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "not analysis match"})
		return
	}
	var in struct {
		Player int    `json:"player"`
		Rack   string `json:"rack"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	if in.Player < 0 || in.Player > 1 {
		in.Player = 0
	}
	s.ManualRack[in.Player] = strings.TrimSpace(in.Rack)
	writeJSON(w, http.StatusOK, m.serialize(s))
}

// TruncateAnalysis trims the match history to the provided turn, allowing
// users to overwrite future moves while exploring alternate branches.
func (m *MatchHandlers) TruncateAnalysis(w http.ResponseWriter, r *http.Request) {
	id := m.pathID(r.URL.Path)
	m.mu.RLock()
	s := m.byID[id]
	m.mu.RUnlock()
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	var in struct {
		Turn int `json:"turn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	if err := s.TruncateToTurn(in.Turn); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, m.serialize(s))
}

func (m *MatchHandlers) prepareFreeInputRack(s *match.Session, mv Move, tokens []string) (string, error) {
	g := s.Game
	if len(tokens) == 0 {
		tokens = tokenizeRow(mv.Word)
	}
	on := int(g.PlayerOnTurn())
	oldRack := g.RackFor(on).String()
	// Build rack string from tokens.  Through-tiles (anchors) are marked
	// with "." in the word (e.g. "V.R" means V placed, through-tile, R placed).
	// tokenizeRow converts "." to " ", so TrimSpace naturally excludes them.
	var sb strings.Builder
	for _, tk := range tokens {
		tk = strings.TrimSpace(tk)
		if tk == "" {
			continue
		}
		if isBlankToken(tk) {
			sb.WriteString("?")
			continue
		}
		if strings.HasPrefix(tk, "[") && strings.HasSuffix(tk, "]") {
			inner := tk[1 : len(tk)-1]
			if inner == strings.ToLower(inner) {
				sb.WriteString("?")
			} else {
				sb.WriteString("[" + strings.ToUpper(inner) + "]")
			}
			continue
		}
		up := strings.ToUpper(tk)
		// Normalize naked digraph tokens into bracket form for rack parsing
		if up == "CH" || up == "LL" || up == "RR" {
			sb.WriteString("[" + up + "]")
			continue
		}
		sb.WriteString(up)
	}
	rackStr := strings.TrimSpace(sb.String())
	if rackStr == "" {
		return "", errors.New("no tiles placed for free input")
	}
	rack := tilemapping.RackFromString(rackStr, s.LD.TileMapping())
	if rack == nil {
		return "", fmt.Errorf("invalid tiles for free input: %s", rackStr)
	}
	// Throw ALL racks back into the bag (not just the current player's).
	// The opponent may hold a randomly-assigned rack that contains tiles the
	// user wants to place.  Returning everything to the bag avoids false
	// "tiles not available" errors.
	opp := 1 - on
	oppRackStr := g.RackFor(opp).String()
	g.ThrowRacksIn()
	if err := g.SetRackForOnly(on, rack); err != nil {
		restoreRackFromString(s, on, oldRack)
		return "", err
	}
	// Restore the opponent's rack from the remaining bag.
	if strings.TrimSpace(oppRackStr) != "" {
		oppRack := tilemapping.RackFromString(oppRackStr, s.LD.TileMapping())
		if oppRack != nil {
			if err := g.SetRackForOnly(opp, oppRack); err != nil {
				// Could not restore exact opponent rack (tiles taken by user).
				// Assign a fresh random rack instead.
				g.SetRandomRack(opp, nil)
			}
		}
	}
	return oldRack, nil
}

func buildTilesForMove(s *match.Session, mv Move, tokens []string) (string, error) {
	g := s.Game
	board := g.Board()
	alph := g.Alphabet()
	row, col := mv.Row, mv.Col
	dr, dc := 0, 1
	if strings.ToUpper(mv.Dir) == "V" {
		dr, dc = 1, 0
	}
	ti := 0
	var sb strings.Builder
	for row >= 0 && row < 15 && col >= 0 && col < 15 {
		ml := board.GetLetter(row, col)
		if ml != 0 {
			sb.WriteString(alph.Letter(ml))
		} else {
			if ti >= len(tokens) {
				break
			}
			tile := normalizePlacementToken(tokens[ti])
			if tile == "" {
				return "", fmt.Errorf("invalid token at index %d", ti)
			}
			sb.WriteString(tile)
			ti++
		}
		row += dr
		col += dc
		// Note: Don't break early after placing last token - we need to continue
		// including any board tiles that follow (e.g., placing "EN" before "CASA"
		// should build "ENCASA", not just "EN"). The break at lines 1056-1058
		// handles the case when we hit an empty cell after all tokens are placed.
	}
	if ti != len(tokens) {
		return "", fmt.Errorf("unplaced tokens: expected %d, placed %d", len(tokens), ti)
	}
	return sb.String(), nil
}

func normalizePlacementToken(token string) string {
	t := strings.TrimSpace(token)
	if t == "" {
		return ""
	}
	if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
		inner := t[1 : len(t)-1]
		up := strings.ToUpper(inner)
		// For digraphs (CH, LL, RR), preserve bracket notation for Macondo parsing
		if up == "CH" || up == "LL" || up == "RR" {
			if inner == strings.ToLower(inner) {
				// Blank digraph: lowercase inside brackets
				return "[" + strings.ToLower(up) + "]"
			}
			return "[" + up + "]"
		}
		// Single letter in brackets: remove brackets, preserve case for blanks
		if inner == strings.ToLower(inner) {
			return strings.ToLower(inner)
		}
		return strings.ToUpper(inner)
	}
	// Naked digraph without brackets (e.g., "CH" from user input)
	up := strings.ToUpper(t)
	if up == "CH" || up == "LL" || up == "RR" {
		if t == strings.ToLower(t) {
			return "[" + strings.ToLower(up) + "]"
		}
		return "[" + up + "]"
	}
	if len(t) == 1 && strings.ToLower(t) == t && strings.ToUpper(t) != t {
		return strings.ToLower(t)
	}
	return strings.ToUpper(t)
}

// applyPlacementDirect attempts to build and apply a placement move using explicit tokens.
// This is a fallback when PlayHuman doesn't find the move (e.g., orientation pruned).
func (m *MatchHandlers) applyPlacementDirect(s *match.Session, mv Move, tokens []string) error {
	g := s.Game
	coords := move.ToBoardGameCoords(mv.Row, mv.Col, strings.ToUpper(mv.Dir) == "V")
	rackStr := g.RackFor(g.PlayerOnTurn()).String()
	tiles, err := buildTilesForMove(s, mv, tokens)
	if err != nil {
		return err
	}
	play, err := g.CreateAndScorePlacementMove(coords, tiles, rackStr, false)
	if err != nil {
		return err
	}
	return g.PlayMove(play, true, 0)
}

func normalizeRackInput(rack string) string {
	if strings.TrimSpace(rack) == "" {
		return ""
	}
	toks := tokenizeRow(normalizeWordToBrackets(rack))
	var b strings.Builder
	for _, tk := range toks {
		if strings.TrimSpace(tk) == "" {
			continue
		}
		if strings.HasPrefix(tk, "[") && strings.HasSuffix(tk, "]") {
			innerRaw := tk[1 : len(tk)-1]
			if innerRaw == strings.ToLower(innerRaw) {
				b.WriteString("?")
				continue
			}
			inner := strings.ToUpper(innerRaw)
			switch inner {
			case "CH", "LL", "RR":
				b.WriteString("[" + inner + "]")
			default:
				b.WriteString(inner)
			}
			continue
		}
		if tk == "?" {
			b.WriteString("?")
			continue
		}
		if tk == strings.ToLower(tk) && strings.ToUpper(tk) != tk {
			// lowercase letters represent blanks
			b.WriteString("?")
			continue
		}
		b.WriteString(strings.ToUpper(tk))
	}
	return b.String()
}

func restoreRackFromString(s *match.Session, player int, rackStr string) {
	if strings.TrimSpace(rackStr) == "" {
		return
	}
	norm := normalizeRackInput(rackStr)
	if norm == "" {
		return
	}
	rack := tilemapping.RackFromString(norm, s.LD.TileMapping())
	if rack == nil {
		return
	}
	if err := s.Game.SetRackForOnly(player, rack); err != nil {
		log.Printf("restore rack failed: %v", err)
	}
}

func revertFreeInput(s *match.Session, player int, oldRack string) {
	s.Game.ThrowRacksInFor(player)
	restoreRackFromString(s, player, oldRack)
}

func isBlankToken(tk string) bool {
	if tk == "" {
		return false
	}
	if strings.HasPrefix(tk, "[") && strings.HasSuffix(tk, "]") {
		inner := tk[1 : len(tk)-1]
		return inner == strings.ToLower(inner)
	}
	return tk == strings.ToLower(tk) && tk != strings.ToUpper(tk)
}

// ApplyManual applies a manual move to the analysis board, updating score and history.
func (m *MatchHandlers) ApplyManual(w http.ResponseWriter, r *http.Request) {
	id := m.pathID(r.URL.Path)
	m.mu.RLock()
	s := m.byID[id]
	m.mu.RUnlock()
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if !s.Analysis {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "not analysis match"})
		return
	}
	var in struct {
		Player int    `json:"player"`
		Word   string `json:"word"`
		Row    int    `json:"row"`
		Col    int    `json:"col"`
		Dir    string `json:"dir"`
		Score  int    `json:"score"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	if in.Player < 0 || in.Player > 1 {
		in.Player = 0
	}
	// Snapshot handling: push PRE-state for proper board rollback on undo
	s.ClearRedo()
	s.PushUndo(s.Capture())
	// Build rows from Game for placed token derivation
	before := make([]string, 15)
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
		before[rr] = sb.String()
	}
	// Derive placed tokens for this ply (rack definitivo del turno)
	mv := Move{Word: normalizeWordToBrackets(in.Word), Row: in.Row, Col: in.Col, Dir: strings.ToUpper(in.Dir)}
	placedRack := extractPlacedTokens(before, mv)
	// Capture turn index before the play adds an event
	turnIdx := len(s.Game.History().Events)
	// Set Game rack to placed tokens so generator encuentre la jugada exacta
	pi := s.Game.PlayerOnTurn()
	s.Game.SetRackForOnly(pi, tilemapping.RackFromString(placedRack, s.LD.TileMapping()))
	// Try to play via Game (accepts phonies under VOID)
	if _, err := s.PlayHuman(in.Word, matchCoords(in.Row, in.Col, in.Dir)); err != nil {
		// Fallback: manual apply/scoring if Game route falla
		autoScore := computeManualScore(s, before, mv)
		rows := make([]string, 15)
		copy(rows, before)
		applyMoveToBoard(&rows, mv)
		copy(s.ManualBoardRows[:], rows)
		s.ManualScore[in.Player] += autoScore
		// switch turn
		s.Game.SetPlayerOnTurn(1 - s.Game.PlayerOnTurn())
		display := displayWordWithAnchors(before, rows, mv)
		cum := s.ManualScore[in.Player]
		s.HistAppend(match.HistRow{Ply: len(s.ScoreRows()) + 1, Player: in.Player, Type: "PLAY", Word: display, Row: in.Row, Col: in.Col, Dir: strings.ToUpper(in.Dir), Score: autoScore, Cum: cum})
	} else {
		// Sync manual mirrors from Game for snapshots/UI as needed
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
	}
	// Track rack definitivo para el turno (para unseen histórico)
	s.AppendPlyRack(placedRack)
	// Track full rack: if the user manually defined a rack and all placed tiles
	// are consistent with it (subset), preserve the full manual rack.
	// Otherwise fall back to placed-only tiles.
	fullRack := placedRack
	if manualR := strings.TrimSpace(s.ManualRack[in.Player]); manualR != "" {
		if placedTokensSubsetOf(placedRack, manualR) {
			fullRack = manualR
		}
	}
	s.SetFullRack(turnIdx, fullRack)
	// Clear manual rack for this player after applying (consumed by this move)
	s.ManualRack[in.Player] = ""
	// Always assign a fresh full rack to the next player from the bag
	// This simplifies input libre mode where the leave is unknown
	nextPlayer := s.Game.PlayerOnTurn()
	if _, err := s.Game.SetRandomRack(nextPlayer, nil); err != nil {
		log.Printf("[ApplyManual] Warning: could not set random rack for player %d: %v", nextPlayer, err)
	}
	writeJSON(w, http.StatusOK, m.serialize(s))
}

// extractPlacedTokens returns the concatenated tokens that were actually placed for mv
func extractPlacedTokens(beforeRows []string, mv Move) string {
	tokens := func(rows []string) [][]string {
		out := make([][]string, 15)
		for r := 0; r < 15; r++ {
			out[r] = tokenizeRow(replaceDotsWithSpaces(rows[r]))
		}
		return out
	}
	b0 := tokens(beforeRows)
	toks := tokenizeRow(mv.Word)
	r, c := mv.Row, mv.Col
	dr, dc := 0, 1
	if strings.ToUpper(mv.Dir) == "V" {
		dr, dc = 1, 0
	}
	ti := 0
	var b strings.Builder
	for r >= 0 && r < 15 && c >= 0 && c < 15 {
		if ti >= len(toks) {
			break
		}
		if strings.TrimSpace(pick(b0, r, c)) == "" {
			b.WriteString(toks[ti])
			ti++
		}
		r += dr
		c += dc
	}
	return b.String()
}

func pick(grid [][]string, r, c int) string {
	if r < 0 || r >= len(grid) {
		return ""
	}
	row := grid[r]
	if c < 0 || c >= len(row) {
		return ""
	}
	return row[c]
}

// placedTokensSubsetOf returns true if every token in placedRack can be found
// (with multiplicity) in manualRack.  Both strings use bracket notation for
// digraphs, e.g. "[CH]A[LL]E" → tokens ["[CH]","A","[LL]","E"].
func placedTokensSubsetOf(placedRack, manualRack string) bool {
	tokenize := func(s string) []string {
		var out []string
		rs := []rune(s)
		for i := 0; i < len(rs); {
			if rs[i] == '[' {
				j := i + 1
				for j < len(rs) && rs[j] != ']' {
					j++
				}
				if j < len(rs) && rs[j] == ']' {
					out = append(out, string(rs[i:j+1]))
					i = j + 1
					continue
				}
			}
			out = append(out, string(rs[i]))
			i++
		}
		return out
	}
	// Build frequency map from manual rack tokens
	avail := make(map[string]int)
	for _, tk := range tokenize(manualRack) {
		avail[strings.ToUpper(tk)]++
	}
	// Check every placed token is available
	for _, tk := range tokenize(placedRack) {
		key := strings.ToUpper(tk)
		if avail[key] > 0 {
			avail[key]--
		} else if avail["?"] > 0 {
			// blank can represent any tile
			avail["?"]--
		} else {
			return false
		}
	}
	return true
}

// Undo last manual action in analysis mode.
func (m *MatchHandlers) Undo(w http.ResponseWriter, r *http.Request) {
	id := m.pathID(r.URL.Path)
	m.mu.RLock()
	s := m.byID[id]
	m.mu.RUnlock()
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if !s.Analysis {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "not analysis match"})
		return
	}
	// Rebuild one turn back with Game
	curTurn := s.AnalysisTurn
	target := curTurn - 1
	if target < 0 {
		target = 0
	}
	_ = s.RebuildToTurn(target)
	// Restore the full rack (manual or placed-only) for this ply so the user
	// can generate/simulate with the complete rack they originally had.
	if rk := s.FullRackAt(target); rk != "" {
		on := s.Game.PlayerOnTurn()
		s.Game.SetRackForOnly(on, tilemapping.RackFromString(rk, s.LD.TileMapping()))
	}
	writeJSON(w, http.StatusOK, m.serialize(s))
}

// Redo next manual action in analysis mode.
func (m *MatchHandlers) Redo(w http.ResponseWriter, r *http.Request) {
	id := m.pathID(r.URL.Path)
	m.mu.RLock()
	s := m.byID[id]
	m.mu.RUnlock()
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if !s.Analysis {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "not analysis match"})
		return
	}
	curTurn := s.AnalysisTurn
	hist := s.Game.History()
	target := curTurn + 1
	if target > len(hist.Events) {
		target = len(hist.Events)
	}
	_ = s.RebuildToTurn(target)
	// Restore the full rack for this ply (same logic as Undo)
	if rk := s.FullRackAt(target); rk != "" {
		on := s.Game.PlayerOnTurn()
		s.Game.SetRackForOnly(on, tilemapping.RackFromString(rk, s.LD.TileMapping()))
	}
	writeJSON(w, http.StatusOK, m.serialize(s))
}

// UndoAll walks the undo stack to the earliest snapshot in analysis mode.
func (m *MatchHandlers) UndoAll(w http.ResponseWriter, r *http.Request) {
	id := m.pathID(r.URL.Path)
	m.mu.RLock()
	s := m.byID[id]
	m.mu.RUnlock()
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if !s.Analysis {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "not analysis match"})
		return
	}
	_ = s.RebuildToTurn(0)
	// Restore the full rack for turn 0
	if rk := s.FullRackAt(0); rk != "" {
		on := s.Game.PlayerOnTurn()
		s.Game.SetRackForOnly(on, tilemapping.RackFromString(rk, s.LD.TileMapping()))
	}
	writeJSON(w, http.StatusOK, m.serialize(s))
}

// RedoAll walks the redo stack to the latest snapshot in analysis mode.
func (m *MatchHandlers) RedoAll(w http.ResponseWriter, r *http.Request) {
	id := m.pathID(r.URL.Path)
	m.mu.RLock()
	s := m.byID[id]
	m.mu.RUnlock()
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if !s.Analysis {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "not analysis match"})
		return
	}
	hist := s.Game.History()
	lastTurn := len(hist.Events)
	_ = s.RebuildToTurn(lastTurn)
	// Restore the full rack for the last turn
	if rk := s.FullRackAt(lastTurn); rk != "" {
		on := s.Game.PlayerOnTurn()
		s.Game.SetRackForOnly(on, tilemapping.RackFromString(rk, s.LD.TileMapping()))
	}
	writeJSON(w, http.StatusOK, m.serialize(s))
}

// computeManualScore calculates score of a manual move in analysis mode using
// board bonuses and cross words. It accepts bracket-coded tokens and blanks.
func computeManualScore(s *match.Session, beforeRows []string, mv Move) int {
	// Build token grid from rows
	board := make([][]string, 15)
	for r := 0; r < 15; r++ {
		row := ""
		if r < len(beforeRows) {
			row = replaceDotsWithSpaces(beforeRows[r])
		}
		tks := tokenizeRow(row)
		line := make([]string, 15)
		copy(line, tks)
		for c := 0; c < 15; c++ {
			if line[c] == "" {
				line[c] = " "
			}
		}
		board[r] = line
	}
	toks := tokenizeRow(mv.Word)
	r, c := mv.Row, mv.Col
	dr, dc := 0, 1
	if strings.ToUpper(mv.Dir) == "V" {
		dr, dc = 1, 0
	}
	// token score via LetterDistribution
	scoreTok := func(token string) int {
		if strings.TrimSpace(token) == "" {
			return 0
		}
		mls, err := tilemapping.ToMachineLetters(token, s.LD.TileMapping())
		if err != nil || len(mls) == 0 {
			return 0
		}
		return s.LD.Score(mls[0])
	}
	// multipliers
	letterMult := func(br rune) int {
		switch br {
		case '\'':
			return 2
		case '^':
			return 4
		case '"':
			return 3
		default:
			return 1
		}
	}
	wordMult := func(br rune) int {
		switch br {
		case '-':
			return 2
		case '=':
			return 3
		case '~':
			return 4
		default:
			return 1
		}
	}
	// main word
	ti := 0
	main := 0
	wmul := 1
	placed := map[[2]int]string{}
	rr, cc := r, c
	for rr >= 0 && rr < 15 && cc >= 0 && cc < 15 {
		btk := board[rr][cc]
		hasAnchor := strings.TrimSpace(btk) != ""
		if !hasAnchor && ti >= len(toks) {
			break
		}
		var use string
		if hasAnchor {
			use = btk
		} else {
			use = toks[ti]
			ti++
		}
		val := scoreTok(use)
		if hasAnchor {
			main += val
		} else {
			br := s.Game.Board().GetBonus(rr, cc)
			lm := letterMult(rune(br))
			wm := wordMult(rune(br))
			main += val * lm
			if wm > 1 {
				wmul *= wm
			}
			placed[[2]int{rr, cc}] = use
		}
		rr += dr
		cc += dc
	}
	main *= wmul
	total := main
	// cross words for each placed tile
	for pos, tk := range placed {
		pr, pc := pos[0], pos[1]
		cdr, cdc := dc, dr // perpendicular
		// find start
		sr, sc := pr, pc
		for nr, nc := sr-cdr, sc-cdc; nr >= 0 && nr < 15 && nc >= 0 && nc < 15; nr, nc = nr-cdr, nc-cdc {
			if strings.TrimSpace(board[nr][nc]) == "" {
				break
			}
			sr, sc = nr, nc
		}
		// build and score
		length := 0
		wsum := 0
		br := s.Game.Board().GetBonus(pr, pc)
		lm := letterMult(rune(br))
		wm := wordMult(rune(br))
		for nr, nc := sr, sc; nr >= 0 && nr < 15 && nc >= 0 && nc < 15; nr, nc = nr+cdr, nc+cdc {
			var use string
			if nr == pr && nc == pc {
				use = tk
			} else {
				use = board[nr][nc]
			}
			if strings.TrimSpace(use) == "" {
				break
			}
			val := scoreTok(use)
			if nr == pr && nc == pc {
				wsum += val * lm
			} else {
				wsum += val
			}
			length++
		}
		if length > 1 {
			if wm < 1 {
				wm = 1
			}
			total += wsum * wm
		}
	}
	// Bingo: 7 fichas colocadas en un turno
	if len(placed) == 7 {
		total += 50
	}
	return total
}

// drawFromBag draws up to n tokens from a bag count map randomly and decreases the bag.
func drawFromBag(bag map[string]int, n int) string {
	if n <= 0 || bag == nil {
		return ""
	}
	// expand bag into pool
	pool := make([]string, 0)
	for k, ct := range bag {
		for i := 0; i < ct; i++ {
			pool = append(pool, k)
		}
	}
	if len(pool) == 0 {
		return ""
	}
	if n > len(pool) {
		n = len(pool)
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		if len(pool) == 0 {
			break
		}
		j := rand.Intn(len(pool))
		pick := pool[j]
		b.WriteString(pick)
		// remove from pool and bag
		pool[j] = pool[len(pool)-1]
		pool = pool[:len(pool)-1]
		if bag[pick] > 0 {
			bag[pick]--
		}
	}
	return b.String()
}

func bagKeyForToken(tk string) string {
	if strings.TrimSpace(tk) == "" {
		return ""
	}
	if isLowerToken(tk) {
		return "?"
	}
	return tk
}

// displayWordWithAnchors builds a string for history showing placed letters with anchor segments in paréntesis.
// Blanks (comodines) are shown as lowercase letters.
// Example output: CO(RA)ZOn(E)S where RA and E are anchors, n is a blank.
func displayWordWithAnchors(beforeRows, afterRows []string, mv Move) string {
	toPlain := func(tk string) string {
		if strings.HasPrefix(tk, "[") && strings.HasSuffix(tk, "]") {
			return tk[1 : len(tk)-1]
		}
		return tk
	}
	// Check if a token represents a blank (lowercase letter or lowercase digraph)
	isBlankToken := func(tk string) bool {
		tk = strings.TrimSpace(tk)
		if tk == "" {
			return false
		}
		// Check for bracketed digraph like [ch], [ll], [rr]
		if strings.HasPrefix(tk, "[") && strings.HasSuffix(tk, "]") {
			inner := tk[1 : len(tk)-1]
			if len(inner) > 0 {
				r := []rune(inner)
				return r[0] >= 'a' && r[0] <= 'z'
			}
			return false
		}
		// Single letter - check if lowercase
		r := []rune(tk)
		if len(r) == 1 {
			return (r[0] >= 'a' && r[0] <= 'z') || r[0] == 'ñ' || r[0] == 'á' || r[0] == 'é' || r[0] == 'í' || r[0] == 'ó' || r[0] == 'ú' || r[0] == 'ü'
		}
		return false
	}
	board := func(rows []string) [][]string {
		out := make([][]string, 15)
		for r := 0; r < 15; r++ {
			line := tokenizeRow(replaceDotsWithSpaces(rows[r]))
			if len(line) < 15 {
				tmp := make([]string, 15)
				copy(tmp, line)
				for i := 0; i < 15; i++ {
					if tmp[i] == "" {
						tmp[i] = " "
					}
				}
				line = tmp
			}
			out[r] = line
		}
		return out
	}
	b0 := board(beforeRows)
	b1 := board(afterRows)
	r, c := mv.Row, mv.Col
	dr, dc := 0, 1
	if strings.ToUpper(mv.Dir) == "V" {
		dr, dc = 1, 0
	}
	// find actual start by walking backward until empty on after board
	sr, sc := r, c
	for nr, nc := sr-dr, sc-dc; nr >= 0 && nr < 15 && nc >= 0 && nc < 15; nr, nc = nr-dr, nc-dc {
		if strings.TrimSpace(b1[nr][nc]) == "" {
			break
		}
		sr, sc = nr, nc
	}
	// Tokenize mv.Word to identify which placed tiles are blanks
	placedTokens := tokenizeRow(mv.Word)
	placedBlanks := make([]bool, len(placedTokens))
	for i, pt := range placedTokens {
		placedBlanks[i] = isBlankToken(pt)
	}
	// consume until break, tracking blanks
	type seg struct {
		anchor bool
		blank  bool
		tok    string
	}
	segs := []seg{}
	rr, cc := sr, sc
	placedIdx := 0 // index into placedTokens
	for rr >= 0 && rr < 15 && cc >= 0 && cc < 15 {
		tk := b1[rr][cc]
		if strings.TrimSpace(tk) == "" {
			break
		}
		isAnchor := strings.TrimSpace(b0[rr][cc]) != ""
		isBlank := false
		if !isAnchor && placedIdx < len(placedBlanks) {
			isBlank = placedBlanks[placedIdx]
			placedIdx++
		}
		segs = append(segs, seg{anchor: isAnchor, blank: isBlank, tok: toPlain(tk)})
		rr += dr
		cc += dc
	}
	// merge anchors into parenthesized blocks, preserve blank lowercase
	var out strings.Builder
	for i := 0; i < len(segs); {
		if !segs[i].anchor {
			tok := segs[i].tok
			if segs[i].blank {
				tok = strings.ToLower(tok)
			} else {
				tok = strings.ToUpper(tok)
			}
			out.WriteString(tok)
			i++
			continue
		}
		j := i
		var buf strings.Builder
		for j < len(segs) && segs[j].anchor {
			buf.WriteString(strings.ToUpper(segs[j].tok))
			j++
		}
		out.WriteString("(" + buf.String() + ")")
		i = j
	}
	return out.String()
}

// GCG exports the current match history using Macondo's native GCG format.
// This ensures full compatibility with Macondo CLI for analysis and comparison.
// When full manual racks are stored (from free_input / analysis mode), the
// exported GCG includes the complete rack for each turn, not just placed tiles.
func (m *MatchHandlers) GCG(w http.ResponseWriter, r *http.Request) {
	id := m.pathID(r.URL.Path)
	m.mu.RLock()
	s := m.byID[id]
	m.mu.RUnlock()
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	// Clone the history so we can override rack fields with the full manual
	// racks without mutating the live game state.
	hist := proto.Clone(s.Game.History()).(*pb.GameHistory)
	for i, evt := range hist.Events {
		if rk := s.FullRackAt(i); rk != "" {
			// The full rack is only stored when the user explicitly
			// defined it via set_rack (ManualRackFlag).  Trust it
			// unconditionally — token-representation mismatches
			// (e.g. individual "C" vs digraph "[CH]") must not
			// block the override.
			evt.Rack = rk
		}
	}

	// Use Macondo's native GCG export function for full compatibility
	gcgContent, err := gcgio.GameHistoryToGCG(hist, true)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to generate GCG: %v", err)})
		return
	}

	// Annotate challenge-penalty passes and Spanish endgame adjustments.
	gcgContent = m.annotateGCG(gcgContent, s)

	// Set appropriate headers for file download
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+s.ID+".gcg\"")

	// Write the GCG content
	_, _ = w.Write([]byte(gcgContent))
}

// annotateGCG inserts GCG comment lines for challenge-penalty passes and
// Spanish-style endgame adjustments.  Move lines start with '>'.
//
// Challenge annotations use an event-index counter that tracks 1:1 with
// '>' lines.  Endgame annotations are handled separately because Macondo's
// GCG exporter may omit the final pass event, shifting indices.  Instead
// we detect the END_RACK_PTS GCG line by its distinctive "(TILES)" format
// and insert the annotation right before it.
func (m *MatchHandlers) annotateGCG(gcg string, s *match.Session) string {
	lines := strings.Split(gcg, "\n")
	hist := s.Game.History()
	evs := hist.GetEvents()

	// Pre-compute Spanish endgame info (if applicable).
	_, rackPts, closer, rackStr, hasEndgame := spanishEndgameAdj(hist)
	var endNames [2]string
	if hasEndgame && len(hist.Players) == 2 {
		endNames[0] = hist.Players[0].GetNickname()
		endNames[1] = hist.Players[1].GetNickname()
	} else {
		endNames = [2]string{"Jugador0", "Jugador1"}
	}

	var out []string
	evtIdx := 0
	for _, line := range lines {
		if strings.HasPrefix(line, ">") {
			// Challenge annotation (uses sequential event-index counter).
			if cw := s.ChallengedWordAt(evtIdx); cw != "" {
				out = append(out, "#note Phony impugnado: "+cw)
			}

			// Spanish endgame annotation: detect the END_RACK_PTS GCG line
			// by its "(TILES) +pts" pattern rather than by event index,
			// because Macondo's GCG export omits the final pass and the
			// index would be off by one.
			if hasEndgame && isEndRackGCGLine(line) {
				opponent := 1 - closer
				out = append(out, fmt.Sprintf("#note Cierre español: %s tiene atril [%s] (valor %d)", endNames[opponent], rackStr, rackPts))
				out = append(out, fmt.Sprintf("#note %s: -%d (fichas restantes)", endNames[opponent], rackPts))
				out = append(out, fmt.Sprintf("#note %s: +%d (cierra partida)", endNames[closer], rackPts))
			}

			// Only advance evtIdx for non-END_RACK_PTS lines, since
			// challenge annotations need 1:1 with played events.
			if !isEndRackGCGLine(line) {
				evtIdx++
			}
		}
		out = append(out, line)
	}
	_ = evs // used indirectly via spanishEndgameAdj
	return strings.Join(out, "\n")
}

// isEndRackGCGLine returns true if the GCG line looks like an END_RACK_PTS
// event, e.g. ">Player: (TILES) +pts total".  The key marker is that the
// "word" field is enclosed in parentheses and there is no board coordinate.
func isEndRackGCGLine(line string) bool {
	// Format: ">Nick: (TILES) +pts cum"
	// After ">Nick: " the next non-space token starts with '('
	idx := strings.Index(line, ": ")
	if idx < 0 {
		return false
	}
	rest := strings.TrimSpace(line[idx+2:])
	// Skip optional rack field (uppercase letters before the coordinate)
	// The END_RACK_PTS line has format "(TILES) +N M" — starts with '('
	return len(rest) > 0 && rest[0] == '('
}

// LoadGCG creates a new analysis match from a GCG file.
// This enables analyzing games loaded from Macondo CLI or other GCG sources.
func (m *MatchHandlers) LoadGCG(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}

	// Parse multipart form for file upload
	err := r.ParseMultipartForm(10 << 20) // 10MB max
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to parse form"})
		return
	}

	file, _, err := r.FormFile("gcg")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no gcg file provided"})
		return
	}
	defer file.Close()

	// Create Macondo configuration (similar to handlers.go pattern)
	cfg := mconfig.DefaultConfig()
	if dp := os.Getenv("MACONDO_DATA_PATH"); strings.TrimSpace(dp) != "" {
		cfg.Set(mconfig.ConfigDataPath, dp)
	} else {
		for _, p := range []string{"../../macondo/data", "../macondo/data", "macondo/data"} {
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				cfg.Set(mconfig.ConfigDataPath, p)
				break
			}
		}
	}

	// The configuration should pick up KWG files from the standard paths

	// Parse GCG using Macondo's native parser
	history, err := gcgio.ParseGCGFromReader(cfg, file)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid GCG: %v", err)})
		return
	}

	// Ensure challenge rule is set to VOID to allow invalid words for analysis
	history.ChallengeRule = pb.ChallengeRule_VOID

	// Get turn parameter (optional - defaults to end of game)
	turnParam := r.FormValue("turn")
	targetTurn := len(history.Events) // Default to end
	if turnParam != "" {
		if t, err := strconv.Atoi(turnParam); err == nil && t >= 0 {
			targetTurn = t
		}
	}

	// Create new session from GCG history
	sessionID := genID("a")

	// Determine KWG file to use (prefer FILE2017, fallback to FISE2016)
	kwgFile := ""
	if p := findRootFile("FILE2017.kwg"); p != "" {
		kwgFile = p
	} else {
		kwgFile = findRootFile("FISE2016_converted.kwg")
	}

	// Create session using existing infrastructure (this properly handles KWG loading)
	session, err := match.NewSession(sessionID, "OSPS49", kwgFile)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to create session: %v", err)})
		return
	}

	// Now replace the game with one created from GCG history
	// Use the session's properly configured game rules
	g, err := game.NewFromHistory(history, session.Game.Rules(), targetTurn)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to create game from history: %v", err)})
		return
	}

	// Set challenge rule to VOID to allow any words in analysis mode
	g.SetChallengeRule(pb.ChallengeRule_VOID)

	// Replace the game in the session
	session.Game = g
	session.Analysis = true // Enable analysis mode
	session.AnalysisTurn = targetTurn

	// Initialize analysis state from loaded game
	session.InitializeAnalysisFromGame()

	// Store session
	m.mu.Lock()
	m.byID[sessionID] = session
	m.mu.Unlock()

	writeJSON(w, http.StatusOK, m.serialize(session))
}

func fmtPlus(n int) string {
	if n >= 0 {
		return "+" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// Position returns a snapshot at a given turn number (0..len(events)).
// Query: ?turn=<n>. Provides board_rows, racks, bag, score, onturn, events total, and turn index.
func (m *MatchHandlers) Position(w http.ResponseWriter, r *http.Request) {
	id := m.pathID(r.URL.Path)
	m.mu.RLock()
	s := m.byID[id]
	m.mu.RUnlock()
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	turn := 0
	if t := r.URL.Query().Get("turn"); t != "" {
		if n, err := strconv.Atoi(t); err == nil && n >= 0 {
			turn = n
		}
	}
	hist := s.Game.History()
	rules := s.Game.Rules()
	ng, err := game.NewFromHistory(hist, rules, 0)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// Clamp turn to available events
	if turn > len(hist.Events) {
		turn = len(hist.Events)
	}
	if turn < 0 {
		turn = 0
	}
	if err := ng.PlayToTurn(turn); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	rows := make([]string, 15)
	alph := ng.Alphabet()
	for rr := 0; rr < 15; rr++ {
		var sb strings.Builder
		for cc := 0; cc < 15; cc++ {
			ml := ng.Board().GetLetter(rr, cc)
			if ml == 0 {
				sb.WriteByte(' ')
			} else {
				sb.WriteString(alph.Letter(ml))
			}
		}
		rows[rr] = sb.String()
	}
	// Override the rack with the full rack (manual or placed-only) when available,
	// so that navigating to a past turn shows the complete rack the player had.
	// This applies to both Analysis mode and Sim + Input libre mode.
	// However, only override when the event rack (placed tiles) is consistent
	// with the full rack.  If the player used free input with tiles that don't
	// match the assigned rack, keep the event rack (what was actually played).
	rackCur := ng.RackFor(ng.PlayerOnTurn()).String()
	rackYou := ng.RackFor(0).String()
	rackBot := ng.RackFor(1).String()
	if rk := s.FullRackAt(turn); rk != "" {
		// Trust the user-defined rack unconditionally.
		rackCur = rk
		onTurn := int(ng.PlayerOnTurn())
		if onTurn == 0 {
			rackYou = rk
		} else {
			rackBot = rk
		}
	}
	out := map[string]any{
		"id":         s.ID,
		"turn":       turn,
		"events":     len(hist.Events),
		"onturn":     ng.PlayerOnTurn(),
		"board_rows": rows,
		"rack":       rackCur,
		"rack_you":   rackYou,
		"rack_bot":   rackBot,
		"bag":        ng.Bag().TilesRemaining(),
		"ruleset":    s.Ruleset,
		"lexicon":    s.Lexicon,
		"play_state": ng.Playing().String(),
		"score":      []int{ng.PointsFor(0), ng.PointsFor(1)},
	}
	writeJSON(w, http.StatusOK, out)
}

// MovesAt returns generated moves (static equity) for a given match position at turn N.
// Query: ?turn=<n>
func (m *MatchHandlers) MovesAt(w http.ResponseWriter, r *http.Request) {
	id := m.pathID(r.URL.Path)
	m.mu.RLock()
	s := m.byID[id]
	m.mu.RUnlock()
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	turn := 0
	if t := r.URL.Query().Get("turn"); t != "" {
		if n, err := strconv.Atoi(t); err == nil && n >= 0 {
			turn = n
		}
	}
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
	log.Printf("[MovesAt] REQUEST turn=%d mode=%s deep=%s iters=%s plies=%s topK=%s threads=%s player=%s",
		turn, mode, r.URL.Query().Get("deep"), r.URL.Query().Get("iters"), r.URL.Query().Get("plies"),
		r.URL.Query().Get("topK"), r.URL.Query().Get("threads"), r.URL.Query().Get("player"))
	// Optimized defaults for stronger play
	optimalThreads := max(1, min(8, runtime.NumCPU()-1))
	iters, plies, topk, threads := 1500, 4, 50, optimalThreads
	if v := r.URL.Query().Get("iters"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			iters = n
		}
	}
	if v := r.URL.Query().Get("plies"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			plies = n
		}
	}
	if v := r.URL.Query().Get("topK"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			topk = n
		}
	}
	if v := r.URL.Query().Get("threads"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			threads = n
		}
	}
	hist := s.Game.History()
	rules := s.Game.Rules()
	maxTurn := len(hist.Events)
	if turn > maxTurn {
		turn = maxTurn
	}
	if turn < 0 {
		turn = 0
	}

	// Determine player index - for historical turns, default to the player from the event
	rawPlayer := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("player")))
	playerIdx := 0
	historicalPlayerIdx := -1 // Will be set for historical turns

	// For current turn, use Copy() to preserve the actual bag state without modifying s.Game
	// (important for input libre mode where SetRandomRack modifies the bag)
	var ng *game.Game
	if turn == maxTurn {
		// Copy current game to preserve bag state but not modify the original
		ng = s.Game.Copy()
		log.Printf("[MovesAt] At current turn %d, using s.Game.Copy() - rack_0: %s, rack_1: %s, bag: %d",
			turn, ng.RackFor(0).String(), ng.RackFor(1).String(), ng.Bag().TilesRemaining())
	} else {
		// Historical turn - reconstruct from history
		var err error
		ng, err = game.NewFromHistory(hist, rules, 0)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := ng.PlayToTurn(turn); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		// For historical turns, use the full rack if we stored one (e.g., from
		// a manually-defined rack in free_input / analysis mode); otherwise
		// fall back to the rack recorded in the Macondo event.
		// Only override when the event rack (placed tiles) is consistent with
		// the full rack.  If the player used free input with tiles that don't
		// match the assigned rack, keep the event rack (what was actually played).
		if turn < len(hist.Events) {
			evt := hist.Events[turn]
			historicalRack := evt.GetRack()
			historicalPlayerIdx = int(evt.GetPlayerIndex())
			if rk := s.FullRackAt(turn); rk != "" {
				if historicalRack == "" || placedTokensSubsetOf(historicalRack, rk) {
					historicalRack = rk
				}
			}
			log.Printf("[MovesAt] Historical turn %d: using rack: %s (player %d)",
				turn, historicalRack, historicalPlayerIdx)
			rack := tilemapping.RackFromString(historicalRack, s.LD.TileMapping())
			if rack != nil {
				ng.SetPlayerOnTurn(historicalPlayerIdx)
				// Throw all racks back into the bag first so tiles are available.
				// After PlayToTurn, the rack may only contain the placed tiles
				// (what Macondo recorded), but the full manual rack may include
				// additional tiles that are still distributed in the bag.
				// ThrowRacksIn returns both players' racks to the bag, making
				// all tiles available for SetRackForOnly.
				ng.ThrowRacksIn()
				if err := ng.SetRackForOnly(historicalPlayerIdx, rack); err != nil {
					log.Printf("[MovesAt] Unable to set historical rack: %v", err)
				}
			}
		}
	}

	// Determine which player to generate moves for
	switch rawPlayer {
	case "1", "bot":
		playerIdx = 1
	case "onturn":
		playerIdx = int(ng.PlayerOnTurn())
		if playerIdx < 0 || playerIdx > 1 {
			playerIdx = 0
		}
	default:
		// For historical turns with no explicit player, use the player from the event
		if historicalPlayerIdx >= 0 {
			playerIdx = historicalPlayerIdx
		}
	}

	if playerIdx != int(ng.PlayerOnTurn()) {
		ng.SetPlayerOnTurn(playerIdx)
	}

	csc, _ := equity.NewCombinedStaticCalculator(s.Lexicon, s.CFG, equity.LeavesFilename, equity.PEGAdjustmentFilename)
	sp, _ := aitp.NewAIStaticTurnPlayerFromGame(ng, s.CFG, []equity.EquityCalculator{csc})
	mg := sp.MoveGenerator()
	rack := ng.RackFor(playerIdx)
	log.Printf("[MovesAt] Generating moves for rack: %s (player %d on turn %d, bag: %d tiles)",
		rack.String(), playerIdx, turn, ng.Bag().TilesRemaining())
	log.Printf("[MovesAt] Current game state - rack_0: %s, rack_1: %s",
		ng.RackFor(0).String(), ng.RackFor(1).String())
	bagTiles := ng.Bag().TilesRemaining()
	exchAllowed := bagTiles >= ng.ExchangeLimit()
	mg.SetMaxCanExchange(game.MaxCanExchange(bagTiles, ng.ExchangeLimit()))
	mg.GenAll(rack, exchAllowed)
	// Collect plays
	type hasPlays interface{ Plays() []*move.Move }
	plays := []*move.Move{}
	if hp, ok := mg.(hasPlays); ok {
		plays = hp.Plays()
	}
	res := MovesResponse{}
	toMove := func(pm *move.Move) Move {
		if pm == nil {
			return Move{}
		}
		r, c, v := pm.CoordsAndVertical()
		dir := "H"
		if v {
			dir = "V"
		}
		lv := 0.0
		if csc != nil {
			lv = csc.LeaveValue(pm.Leave())
		} else {
			lv = pm.Equity() - float64(pm.Score())
		}
		eq := pm.Equity()
		if eq == 0 {
			eq = float64(pm.Score()) + lv
		}
		raw := strings.ReplaceAll(pm.TilesString(), ".", "")
		word := normalizeWordToBrackets(raw)
		typ := "PLAY"
		switch pm.Action() {
		case move.MoveTypeExchange:
			typ = "EXCH"
		case move.MoveTypePass:
			typ = "PASS"
		}
		return Move{Type: typ, Word: word, Row: r, Col: c, Dir: dir, Score: pm.Score(), Leave: pm.LeaveString(), LeaveVal: lv, Equity: eq}
	}
	if mode == "sim" {
		// Sort plays by equity desc before selecting topK candidates
		// This ensures exchanges with high equity are included in simulation
		moveEquity := func(pm *move.Move) float64 {
			if pm == nil {
				return -1e18
			}
			if eq := pm.Equity(); eq != 0 {
				return eq
			}
			lv := 0.0
			if csc != nil {
				lv = csc.LeaveValue(pm.Leave())
			}
			return float64(pm.Score()) + lv
		}
		sort.SliceStable(plays, func(i, j int) bool { return moveEquity(plays[i]) > moveEquity(plays[j]) })
		if topk > len(plays) {
			topk = len(plays)
		}
		cand := plays[:topk]
		// Ensure exchange/pass are considered even if not in topK
		// Allow exchange only if bag has at least 1 tile
		exchAllowed := ng.Bag().TilesRemaining() >= 1
		if exchAllowed {
			var bestEx *move.Move
			for _, pm := range plays {
				if pm != nil && pm.Action() == move.MoveTypeExchange {
					bestEx = pm
					break
				}
			}
			if bestEx != nil {
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
		// Ensure csc
		if csc == nil {
			csc, _ = equity.NewCombinedStaticCalculator(s.Lexicon, s.CFG, "", equity.PEGAdjustmentFilename)
		}
		log.Printf("[MovesAt] Starting simulation with %d candidates for player %d", len(cand), playerIdx)
		log.Printf("[MovesAt] Simulation racks - rack_0: %s, rack_1: %s (player on turn: %d)",
			ng.RackFor(0).String(), ng.RackFor(1).String(), ng.PlayerOnTurn())
		simmer := &montecarlo.Simmer{}
		simmer.Init(ng, []equity.EquityCalculator{csc}, csc, s.CFG)
		if threads <= 0 {
			threads = max(1, min(8, runtime.NumCPU()-1))
		}
		simmer.SetThreads(threads)
		deep := r.URL.Query().Get("deep") == "1"
		log.Printf("[MovesAt] SIM CONFIG deep=%v threads=%d iters=%d plies=%d topK=%d candidates=%d", deep, threads, iters, plies, topk, len(cand))
		simmer.SetStoppingCondition(montecarlo.Stop99)
		simmer.SetAutostopCheckInterval(16)
		log.Printf("[MovesAt] Stop99 ENABLED (check/16), deep=%v", deep)
		if err := simmer.PrepareSim(plies, cand); err != nil {
			log.Printf("[MovesAt] PrepareSim FAILED: %v", err)
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		log.Printf("[MovesAt] PrepareSim OK, starting simulation...")
		simStart := time.Now()

		// Simulation timeout from env (default 45s, deep gets 2x)
		simTO := 45 * time.Second
		if s := strings.TrimSpace(os.Getenv("MACONDO_SIM_TIMEOUT_MS")); s != "" {
			if ms, err := time.ParseDuration(s + "ms"); err == nil && ms > 0 {
				simTO = ms
			}
		}
		if deep {
			simTO = simTO * 2
		}
		log.Printf("[MovesAt] Simulation timeout: %v", simTO)

		// Use multi-threaded simulation for analysis when beneficial
		if threads > 1 {
			ctx, cancel := context.WithTimeout(context.Background(), simTO)
			defer cancel()
			log.Printf("[MovesAt] Running MULTI-THREAD simulation (threads=%d)", threads)
			if err := simmer.Simulate(ctx); err != nil {
				log.Printf("[MovesAt] Simulate FAILED after %v: %v", time.Since(simStart), err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("simulation failed: %v", err)})
				return
			}
		} else {
			log.Printf("[MovesAt] Running SINGLE-THREAD simulation (iters=%d plies=%d)", iters, plies)
			simmer.SimSingleThread(iters, plies)
		}
		log.Printf("[MovesAt] Simulation COMPLETE in %v", time.Since(simStart))
		sp := simmer.PlaysByWinProb().PlaysNoLock()
		for _, simPlay := range sp {
			pm := simPlay.Move()
			if pm == nil {
				continue
			}
			mv := toMove(pm)
			mv.WinPct = 100.0 * simPlay.WinProb()
			res.All = append(res.All, mv)
		}
		if len(res.All) > 0 {
			res.Best = res.All[0]
		}
		writeJSON(w, http.StatusOK, res)
		return
	}
	// Static equity path
	for _, pm := range plays {
		res.All = append(res.All, toMove(pm))
	}
	// Ordenar por Equity desc para priorizar estático por equity (no score)
	sort.SliceStable(res.All, func(i, j int) bool { return res.All[i].Equity > res.All[j].Equity })

	// Ensure exchange and pass moves are prioritized in the response even if they have low equity
	// This ensures they appear in the UI's top-N display
	if len(res.All) > topk {
		// Find best exchange
		var bestExIdx int = -1
		for i, mv := range res.All {
			if mv.Type == "EXCH" {
				bestExIdx = i
				break
			}
		}
		// Find pass move
		var passIdx int = -1
		for i, mv := range res.All {
			if mv.Type == "PASS" {
				passIdx = i
				break
			}
		}
		// If exchange or pass are beyond topk, move them to the end of top results
		if bestExIdx >= topk {
			exMove := res.All[bestExIdx]
			res.All = append(res.All[:bestExIdx], res.All[bestExIdx+1:]...)
			res.All = append(res.All[:topk], append([]Move{exMove}, res.All[topk:]...)...)
		}
		if passIdx >= topk {
			// Re-find pass index in case exchange move affected it
			passIdx = -1
			for i, mv := range res.All {
				if mv.Type == "PASS" {
					passIdx = i
					break
				}
			}
			if passIdx >= topk {
				passMove := res.All[passIdx]
				res.All = append(res.All[:passIdx], res.All[passIdx+1:]...)
				res.All = append(res.All[:topk], append([]Move{passMove}, res.All[topk:]...)...)
			}
		}
	}

	if len(res.All) > 0 {
		res.Best = res.All[0]
	}
	writeJSON(w, http.StatusOK, res)
}

// LogStream provides Server-Sent Events for real-time Macondo bot logs
func (m *MatchHandlers) LogStream(w http.ResponseWriter, r *http.Request) {
	id := m.pathID(r.URL.Path)

	if r.Method != http.MethodGet {
		// Don't use writeJSON for SSE endpoints to avoid MIME type conflicts
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Write([]byte("GET only"))
		return
	}

	// Set SSE headers FIRST before any writes
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Cache-Control")

	// Write initial connection message to establish SSE stream
	fmt.Fprintf(w, "data: Log stream connected for session %s\n\n", id)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	// Create a channel for this client
	clientChan := make(chan string, 100) // Buffer to prevent blocking

	// Get or create log buffer for this session
	m.mu.Lock()
	logBuffer, exists := m.logBuffers[id]
	if !exists {
		logBuffer = NewLogBuffer(id)
		m.logBuffers[id] = logBuffer
	}
	m.mu.Unlock()

	// Add this client to the log buffer
	logBuffer.AddClient(clientChan)
	defer logBuffer.RemoveClient(clientChan)

	// Send any existing buffer content first
	if existingLogs := logBuffer.GetBuffer(); len(existingLogs) > 0 {
		fmt.Fprintf(w, "data: %s\n\n", strings.ReplaceAll(existingLogs, "\n", "\\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}

	// Send keepalive and listen for new logs
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case logLine, ok := <-clientChan:
			if !ok {
				return // Channel closed
			}
			// Send log line as SSE
			fmt.Fprintf(w, "data: %s\n\n", strings.ReplaceAll(logLine, "\n", "\\n"))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		case <-ticker.C:
			// Send keepalive
			fmt.Fprintf(w, ": keepalive\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		case <-r.Context().Done():
			// Client disconnected
			return
		}
	}
}

// GetLogBuffer returns the log buffer for a session (for use by session)
func (m *MatchHandlers) GetLogBuffer(sessionID string) *LogBuffer {
	m.mu.Lock()
	defer m.mu.Unlock()

	logBuffer, exists := m.logBuffers[sessionID]
	if !exists {
		logBuffer = NewLogBuffer(sessionID)
		m.logBuffers[sessionID] = logBuffer
	}
	return logBuffer
}
