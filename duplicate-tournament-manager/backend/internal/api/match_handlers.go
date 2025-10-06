package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
)

// LogBuffer captures zerolog output and broadcasts to SSE clients
type LogBuffer struct {
	mu       sync.RWMutex
	buffer   bytes.Buffer
	clients  map[chan string]bool
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
	eng Engine
}

func NewMatchHandlers(eng Engine) *MatchHandlers {
	return &MatchHandlers{
		byID: map[string]*match.Session{},
		logBuffers: map[string]*LogBuffer{},
		eng: eng,
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
	s, err := match.NewSession(id, in.Ruleset, in.KWG)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
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
		Word string `json:"word"`
		Row  int    `json:"row"`
		Col  int    `json:"col"`
		Dir  string `json:"dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	_, err := s.PlayHuman(in.Word, matchCoords(in.Row, in.Col, in.Dir))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
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
	if err := s.Exchange(in.Tiles); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
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
	if err := s.Pass(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
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
		out["winner"] = h.Winner
		out["play_state"] = h.PlayState.String()
		if len(h.FinalScores) == 2 {
			out["final_score"] = []int{int(h.FinalScores[0]), int(h.FinalScores[1])}
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
	// rack: en análisis incluimos ambos; en vs-bot solo el de jugador 0
	if s.Analysis {
		cur := s.Game.PlayerOnTurn()
		if cur < 0 || cur > 1 {
			cur = 0
		}
		out["rack"] = s.Game.RackFor(cur).String()
		out["rack_you"] = s.Game.RackFor(0).String()
		out["rack_bot"] = s.Game.RackFor(1).String()
	} else {
		out["rack"] = s.Game.RackFor(0).String()
	}
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
			if isLowerToken(tk) {
				b.WriteString("?")
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
	// Current and desired racks
	cur := s.Game.RackFor(p).TilesOn()
	desiredRack := tilemapping.RackFromString(desired, s.LD.TileMapping())
	newTiles := desiredRack.TilesOn()
	// Build multiset deltas
	toMap := func(arr []tilemapping.MachineLetter) map[tilemapping.MachineLetter]int {
		m := map[tilemapping.MachineLetter]int{}
		for _, ml := range arr {
			m[ml]++
		}
		return m
	}
	oldMap := toMap(cur)
	newMap := toMap(newTiles)
	var retList []tilemapping.MachineLetter
	for ml, ct := range oldMap {
		if d := ct - newMap[ml]; d > 0 {
			for i := 0; i < d; i++ {
				retList = append(retList, ml)
			}
		}
	}
	var addList []tilemapping.MachineLetter
	for ml, ct := range newMap {
		if d := ct - oldMap[ml]; d > 0 {
			for i := 0; i < d; i++ {
				addList = append(addList, ml)
			}
		}
	}
	// Reconcile bag
	bag := s.Game.Bag()
	if len(retList) > 0 {
		bag.PutBack(retList)
	}
	if len(addList) > 0 {
		_ = bag.RemoveTiles(addList)
	}
	// Set final rack
	s.Game.SetRackForOnly(p, desiredRack)
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
			on := s.Game.PlayerOnTurn()
			opp := 1 - on
			oppTiles := int(s.Game.RackFor(opp).NumTiles())
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
			oppTiles := int(ng.RackFor(1).NumTiles())
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
		opp := ng.RackFor(1)
		for _, ml := range opp.TilesOn() {
			letter := alph.Letter(ml)
			counts[letter] += 1
		}
		tiles := make([][2]any, 0, len(counts))
		for k, v := range counts {
			tiles = append(tiles, [2]any{k, v})
		}
		bagRem := ng.Bag().TilesRemaining()
		oppTiles := int(opp.NumTiles())
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
		opp := s.Game.RackFor(1)
		for _, ml := range opp.TilesOn() {
			letter := alph.Letter(ml)
			counts[letter] += 1
		}
		tiles := make([][2]any, 0, len(counts))
		for k, v := range counts {
			tiles = append(tiles, [2]any{k, v})
		}
		bagRem := s.Game.Bag().TilesRemaining()
		oppTiles := int(opp.NumTiles())
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
	}
	sr := s.ScoreRows()
	rows := make([]Row, 0, len(sr))
	// Prefer macondo events if present to include PlayedTiles
	if evs := s.Game.History().GetEvents(); len(evs) > 0 {
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
			played := e.GetPlayedTiles()
			// For exchanges, use the exchanged tiles instead of played tiles
			if t == "EXCHANGE" {
				played = e.GetExchanged()
			}
			rows = append(rows, Row{Ply: i + 1, Player: int(e.GetPlayerIndex()), Type: t, Word: word, Played: played, Row: int(e.GetRow()), Col: int(e.GetColumn()), Dir: dir, Score: int(e.GetScore()), Cum: int(e.GetCumulative())})
		}
	} else {
		for _, e := range sr {
			rows = append(rows, Row{Ply: e.Ply, Player: e.Player, Type: e.Type, Word: e.Word, Played: "", Row: e.Row, Col: e.Col, Dir: e.Dir, Score: e.Score, Cum: e.Cum})
		}
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
	// Put the placed rack as the current player's rack for immediate analysis
	if lp := s.ManualPlyRacks(); len(lp) > 0 && target < len(lp) {
		lastPlaced := lp[target]
		if strings.TrimSpace(lastPlaced) != "" {
			on := s.Game.PlayerOnTurn()
			s.Game.SetRackForOnly(on, tilemapping.RackFromString(lastPlaced, s.LD.TileMapping()))
		}
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
	if lp := s.ManualPlyRacks(); len(lp) > 0 {
		firstPlaced := lp[0]
		if strings.TrimSpace(firstPlaced) != "" {
			on := s.Game.PlayerOnTurn()
			s.Game.SetRackForOnly(on, tilemapping.RackFromString(firstPlaced, s.LD.TileMapping()))
		}
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
	_ = s.RebuildToTurn(len(hist.Events))
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
func displayWordWithAnchors(beforeRows, afterRows []string, mv Move) string {
	toPlain := func(tk string) string {
		if strings.HasPrefix(tk, "[") && strings.HasSuffix(tk, "]") {
			return tk[1 : len(tk)-1]
		}
		return tk
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
	// consume until break
	type seg struct {
		anchor bool
		tok    string
	}
	segs := []seg{}
	rr, cc := sr, sc
	for rr >= 0 && rr < 15 && cc >= 0 && cc < 15 {
		tk := b1[rr][cc]
		if strings.TrimSpace(tk) == "" {
			break
		}
		isAnchor := strings.TrimSpace(b0[rr][cc]) != ""
		segs = append(segs, seg{anchor: isAnchor, tok: toPlain(tk)})
		rr += dr
		cc += dc
	}
	// merge anchors into parenthesized blocks
	var out strings.Builder
	for i := 0; i < len(segs); {
		if !segs[i].anchor {
			out.WriteString(segs[i].tok)
			i++
			continue
		}
		j := i
		var buf strings.Builder
		for j < len(segs) && segs[j].anchor {
			buf.WriteString(segs[j].tok)
			j++
		}
		out.WriteString("(" + buf.String() + ")")
		i = j
	}
	return out.String()
}

// GCG exports the current match history using Macondo's native GCG format.
// This ensures full compatibility with Macondo CLI for analysis and comparison.
func (m *MatchHandlers) GCG(w http.ResponseWriter, r *http.Request) {
	id := m.pathID(r.URL.Path)
	m.mu.RLock()
	s := m.byID[id]
	m.mu.RUnlock()
	if s == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	// Use Macondo's native GCG export function for full compatibility
	gcgContent, err := gcgio.GameHistoryToGCG(s.Game.History(), true)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("Failed to generate GCG: %v", err)})
		return
	}

	// Set appropriate headers for file download
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+s.ID+".gcg\"")

	// Write the GCG content
	_, _ = w.Write([]byte(gcgContent))
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
	out := map[string]any{
		"id":         s.ID,
		"turn":       turn,
		"events":     len(hist.Events),
		"onturn":     ng.PlayerOnTurn(),
		"board_rows": rows,
		"rack":       ng.RackFor(ng.PlayerOnTurn()).String(),
		"rack_you":   ng.RackFor(0).String(),
		"rack_bot":   ng.RackFor(1).String(),
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
	ng, err := game.NewFromHistory(hist, rules, 0)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
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
	// Prepare static equity calculator and generator
	csc, _ := equity.NewCombinedStaticCalculator(s.Lexicon, s.CFG, equity.LeavesFilename, equity.PEGAdjustmentFilename)
	sp, _ := aitp.NewAIStaticTurnPlayerFromGame(ng, s.CFG, []equity.EquityCalculator{csc})
	mg := sp.MoveGenerator()
	rack := ng.RackFor(ng.PlayerOnTurn())
	// Allow exchanges as long as there is at least 1 tile in the bag
	exchAllowed := ng.Bag().TilesRemaining() >= 1
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
		simmer := &montecarlo.Simmer{}
		simmer.Init(ng, []equity.EquityCalculator{csc}, csc, s.CFG)
		if threads <= 0 {
			threads = max(1, min(8, runtime.NumCPU()-1))
		}
		simmer.SetThreads(threads)
		simmer.SetStoppingCondition(montecarlo.Stop99)
		simmer.SetAutostopCheckInterval(16)
		if err := simmer.PrepareSim(plies, cand); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		// Use multi-threaded simulation for analysis when beneficial
		if threads > 1 {
			ctx := context.Background()
			if err := simmer.Simulate(ctx); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("simulation failed: %v", err)})
				return
			}
		} else {
			simmer.SimSingleThread(iters, plies)
		}
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
