package api

import (
    "encoding/json"
    "net/http"
    "strconv"
    "strings"
    "sync"

    "dupman/backend/internal/match"
    "github.com/domino14/word-golib/tilemapping"
    pb "github.com/domino14/macondo/gen/api/proto/macondo"
    "github.com/domino14/macondo/game"
    aitp "github.com/domino14/macondo/ai/turnplayer"
    "github.com/domino14/macondo/equity"
    "github.com/domino14/macondo/move"
    "github.com/domino14/macondo/montecarlo"
)

type MatchHandlers struct{
    mu sync.RWMutex
    byID map[string]*match.Session
}

func NewMatchHandlers() *MatchHandlers { return &MatchHandlers{byID: map[string]*match.Session{}} }

// Abort ends the match immediately and marks it as GAME_OVER without rack penalties.
func (m *MatchHandlers) Abort(w http.ResponseWriter, r *http.Request){
    id := m.pathID(r.URL.Path)
    m.mu.RLock(); s := m.byID[id]; m.mu.RUnlock()
    if s==nil { writeJSON(w,http.StatusNotFound, map[string]string{"error":"not found"}); return }
    if r.Method != http.MethodPost { writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error":"POST only"}); return }
    s.Abort()
    writeJSON(w, http.StatusOK, m.serialize(s))
}

func (m *MatchHandlers) Create(w http.ResponseWriter, r *http.Request){
    var in struct{ Ruleset string `json:"ruleset"`; KWG string `json:"kwg"`; Challenge string `json:"challenge"` }
    _ = json.NewDecoder(r.Body).Decode(&in)
    if strings.TrimSpace(in.KWG)=="" {
        // Prefer FILE2017 if present; fallback to FISE2016
        if p := findRootFile("FILE2017.kwg"); p != "" { in.KWG = p } else { in.KWG = findRootFile("FISE2016_converted.kwg") }
    } else {
        // If a bare filename was passed, try to resolve it similarly
        if p := findRootFile(in.KWG); p != "" { in.KWG = p }
    }
    id := genID("m")
    s, err := match.NewSession(id, in.Ruleset, in.KWG)
    if err != nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()}); return }
    // Optional: override challenge rule ("single" default, or "void")
    switch strings.ToLower(strings.TrimSpace(in.Challenge)) {
    case "void":
        s.Game.SetChallengeRule(pb.ChallengeRule_VOID)
    case "single", "":
        s.Game.SetChallengeRule(pb.ChallengeRule_SINGLE)
    }
    m.mu.Lock(); m.byID[id]=s; m.mu.Unlock()
    writeJSON(w, http.StatusOK, m.serialize(s))
}

func (m *MatchHandlers) Get(w http.ResponseWriter, r *http.Request){
    id := m.pathID(r.URL.Path)
    m.mu.RLock(); s := m.byID[id]; m.mu.RUnlock()
    if s==nil { writeJSON(w,http.StatusNotFound, map[string]string{"error":"not found"}); return }
    writeJSON(w, http.StatusOK, m.serialize(s))
}

func (m *MatchHandlers) Play(w http.ResponseWriter, r *http.Request){
    id := m.pathID(r.URL.Path)
    m.mu.RLock(); s := m.byID[id]; m.mu.RUnlock()
    if s==nil { writeJSON(w,http.StatusNotFound, map[string]string{"error":"not found"}); return }
    // Note: Row/Col must have individual JSON tags; a combined declaration with
    // a single tag string prevents proper decoding (would default to 0,0).
    var in struct{
        Word string `json:"word"`
        Row  int    `json:"row"`
        Col  int    `json:"col"`
        Dir  string `json:"dir"`
    }
    if err := json.NewDecoder(r.Body).Decode(&in); err!=nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error":"bad json"}); return }
    _, err := s.PlayHuman(in.Word, matchCoords(in.Row,in.Col,in.Dir))
    if err != nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()}); return }
    writeJSON(w, http.StatusOK, m.serialize(s))
}

func (m *MatchHandlers) Exchange(w http.ResponseWriter, r *http.Request){
    id := m.pathID(r.URL.Path)
    m.mu.RLock(); s := m.byID[id]; m.mu.RUnlock()
    if s==nil { writeJSON(w,http.StatusNotFound, map[string]string{"error":"not found"}); return }
    var in struct{ Tiles string `json:"tiles"` }
    if err := json.NewDecoder(r.Body).Decode(&in); err!=nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error":"bad json"}); return }
    if err := s.Exchange(in.Tiles); err!=nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()}); return }
    writeJSON(w, http.StatusOK, m.serialize(s))
}

func (m *MatchHandlers) Pass(w http.ResponseWriter, r *http.Request){
    id := m.pathID(r.URL.Path)
    m.mu.RLock(); s := m.byID[id]; m.mu.RUnlock()
    if s==nil { writeJSON(w,http.StatusNotFound, map[string]string{"error":"not found"}); return }
    if err := s.Pass(); err!=nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()}); return }
    writeJSON(w, http.StatusOK, m.serialize(s))
}

func (m *MatchHandlers) AIMove(w http.ResponseWriter, r *http.Request){
    id := m.pathID(r.URL.Path)
    m.mu.RLock(); s := m.byID[id]; m.mu.RUnlock()
    if s==nil { writeJSON(w,http.StatusNotFound, map[string]string{"error":"not found"}); return }
    var in struct{ Mode string `json:"mode"`; Sim *struct{ Iters,Plies,TopK,Threads int } `json:"sim"` }
    _ = json.NewDecoder(r.Body).Decode(&in)
    mode := match.AIStatic
    if strings.EqualFold(in.Mode, "sim") { mode = match.AISim }
    iters, plies, topk, threads := 0,0,0,0
    if in.Sim!=nil { iters=in.Sim.Iters; plies=in.Sim.Plies; topk=in.Sim.TopK; threads=in.Sim.Threads }
    _, err := s.AIMove(mode, iters, plies, topk, threads)
    if err != nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()}); return }
    writeJSON(w, http.StatusOK, m.serialize(s))
}

func (m *MatchHandlers) pathID(p string) string {
    parts := strings.Split(strings.TrimSuffix(p, "/"), "/")
    if len(parts)>=3 { return parts[2] }
    return ""
}

func matchCoords(r,c int, d string) match.Coords { return match.Coords{ Row:r, Col:c, Dir: strings.ToUpper(d) } }

func (m *MatchHandlers) serialize(s *match.Session) map[string]any {
    // minimal snapshot
    out := map[string]any{
        "id": s.ID,
        "ruleset": s.Ruleset,
        "lexicon": s.Lexicon,
        "turn": s.Game.PlayerOnTurn(),
        "bag": s.Game.Bag().TilesRemaining(),
        "score": []int{ s.Game.PointsFor(0), s.Game.PointsFor(1) },
        "ver": 1,
    }
    // expose basic game state/winner if available
    if h := s.Game.History(); h != nil {
        out["winner"] = h.Winner
        out["play_state"] = h.PlayState.String()
        if len(h.FinalScores) == 2 { out["final_score"] = []int{ int(h.FinalScores[0]), int(h.FinalScores[1]) } }
    }
    // board rows: 15 strings, spaces for empty
    rows := make([]string, 15)
    bonus := make([]string, 15)
    alph := s.Game.Alphabet()
    for r:=0; r<15; r++ {
        var sb strings.Builder
        var bb strings.Builder
        for c:=0; c<15; c++ {
            ml := s.Game.Board().GetLetter(r,c)
            if ml == 0 { sb.WriteByte(' ') } else { sb.WriteString(alph.Letter(ml)) }
            // write bonus square code as a single byte (per board.BonusSquare rune)
            b := s.Game.Board().GetBonus(r,c)
            bb.WriteByte(byte(b))
        }
        rows[r] = sb.String()
        bonus[r] = bb.String()
    }
    out["board_rows"] = rows
    out["bonus_rows"] = bonus
    // player rack visible only for player 0
    out["rack"] = s.Game.RackFor(0).String()
    return out
}

// Bag returns a detailed breakdown letter->count for the bag
func (m *MatchHandlers) Bag(w http.ResponseWriter, r *http.Request){
    id := m.pathID(r.URL.Path)
    m.mu.RLock(); s := m.byID[id]; m.mu.RUnlock()
    if s==nil { writeJSON(w,http.StatusNotFound, map[string]string{"error":"not found"}); return }
    tm := s.Game.Bag().PeekMap() // index: MachineLetter, value: count
    alph := s.Game.Alphabet()
    tiles := make([][2]any, 0, len(tm))
    for i, ct := range tm {
        if ct == 0 { continue }
        letter := alph.Letter(tilemapping.MachineLetter(i))
        tiles = append(tiles, [2]any{letter, ct})
    }
    writeJSON(w, http.StatusOK, map[string]any{
        "id": s.ID,
        "remaining": s.Game.Bag().TilesRemaining(),
        "tiles": tiles,
    })
}

// Unseen returns counts of tiles not visible to the human player:
// bag contents + opponent rack. Also includes opponent rack size and totals.
func (m *MatchHandlers) Unseen(w http.ResponseWriter, r *http.Request){
    id := m.pathID(r.URL.Path)
    m.mu.RLock(); s := m.byID[id]; m.mu.RUnlock()
    if s==nil { writeJSON(w,http.StatusNotFound, map[string]string{"error":"not found"}); return }
    alph := s.Game.Alphabet()
    // Bag breakdown by machine letter
    bagMap := s.Game.Bag().PeekMap()
    // Opponent rack (human is player 0)
    opp := s.Game.RackFor(1)
    // Aggregate per-letter unseen counts: bag + opponent rack
    counts := map[string]int{}
    for i, ct := range bagMap {
        if ct == 0 { continue }
        letter := alph.Letter(tilemapping.MachineLetter(i))
        counts[letter] += int(ct)
    }
    for _, ml := range opp.TilesOn() {
        letter := alph.Letter(ml)
        counts[letter] += 1
    }
    // Serialize counts map as an array to keep stable ordering on client
    tiles := make([][2]any, 0, len(counts))
    for k, v := range counts { tiles = append(tiles, [2]any{k, v}) }
    bagRem := s.Game.Bag().TilesRemaining()
    oppTiles := int(opp.NumTiles())
    writeJSON(w, http.StatusOK, map[string]any{
        "id": s.ID,
        "bag_remaining": bagRem,
        "opp_rack": oppTiles,
        "total_unseen": bagRem + oppTiles,
        "tiles": tiles,
    })
}

// ScoreSheet returns a minimal move history
func (m *MatchHandlers) ScoreSheet(w http.ResponseWriter, r *http.Request){
    id := m.pathID(r.URL.Path)
    m.mu.RLock(); s := m.byID[id]; m.mu.RUnlock()
    if s==nil { writeJSON(w,http.StatusNotFound, map[string]string{"error":"not found"}); return }
    type Row struct { Ply int `json:"ply"`; Player int `json:"player"`; Type string `json:"type"`; Word string `json:"word"`; Played string `json:"played"`; Row int `json:"row"`; Col int `json:"col"`; Dir string `json:"dir"`; Score int `json:"score"`; Cum int `json:"cum"` }
    sr := s.ScoreRows()
    rows := make([]Row, 0, len(sr))
    // Prefer macondo events if present to include PlayedTiles
    if evs := s.Game.History().GetEvents(); len(evs) > 0 {
        for i, e := range evs {
            t := e.GetType().String()
            word := ""; if ws := e.GetWordsFormed(); len(ws) > 0 { word = ws[0] }
            dir := "H"; if e.GetDirection() == pb.GameEvent_VERTICAL { dir = "V" }
            rows = append(rows, Row{ Ply: i+1, Player: int(e.GetPlayerIndex()), Type: t, Word: word, Played: e.GetPlayedTiles(), Row: int(e.GetRow()), Col: int(e.GetColumn()), Dir: dir, Score: int(e.GetScore()), Cum: int(e.GetCumulative()) })
        }
    } else {
        for _, e := range sr {
            rows = append(rows, Row{ Ply:e.Ply, Player:e.Player, Type:e.Type, Word:e.Word, Played:"", Row:e.Row, Col:e.Col, Dir:e.Dir, Score:e.Score, Cum:e.Cum })
        }
    }
    writeJSON(w, http.StatusOK, map[string]any{ "id": s.ID, "rows": rows })
}

// Events returns the list of engine events (no synthetic rows), with compact fields.
func (m *MatchHandlers) Events(w http.ResponseWriter, r *http.Request){
    id := m.pathID(r.URL.Path)
    m.mu.RLock(); s := m.byID[id]; m.mu.RUnlock()
    if s==nil { writeJSON(w,http.StatusNotFound, map[string]string{"error":"not found"}); return }
    evs := s.Game.History().GetEvents()
    type Ev struct { Ply int `json:"ply"`; Player int `json:"player"`; Type string `json:"type"`; Row int `json:"row"`; Col int `json:"col"`; Dir string `json:"dir"`; Word string `json:"word"` }
    out := make([]Ev, 0, len(evs))
    for i, e := range evs {
        dir := "H"; if e.GetDirection() == pb.GameEvent_VERTICAL { dir = "V" }
        word := ""; if ws := e.GetWordsFormed(); len(ws) > 0 { word = ws[0] }
        out = append(out, Ev{ Ply: i+1, Player: int(e.GetPlayerIndex()), Type: e.GetType().String(), Row: int(e.GetRow()), Col: int(e.GetColumn()), Dir: dir, Word: word })
    }
    writeJSON(w, http.StatusOK, map[string]any{ "id": s.ID, "count": len(out), "events": out })
}

// Position returns a snapshot at a given turn number (0..len(events)).
// Query: ?turn=<n>. Provides board_rows, racks, bag, score, onturn, events total, and turn index.
func (m *MatchHandlers) Position(w http.ResponseWriter, r *http.Request){
    id := m.pathID(r.URL.Path)
    m.mu.RLock(); s := m.byID[id]; m.mu.RUnlock()
    if s==nil { writeJSON(w,http.StatusNotFound, map[string]string{"error":"not found"}); return }
    turn := 0
    if t := r.URL.Query().Get("turn"); t != "" {
        if n, err := strconv.Atoi(t); err == nil && n >= 0 { turn = n }
    }
    hist := s.Game.History()
    rules := s.Game.Rules()
    ng, err := game.NewFromHistory(hist, rules, 0)
    if err != nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()}); return }
    // Clamp turn to available events
    if turn > len(hist.Events) { turn = len(hist.Events) }
    if turn < 0 { turn = 0 }
    if err := ng.PlayToTurn(turn); err != nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()}); return }
    rows := make([]string, 15)
    alph := ng.Alphabet()
    for rr:=0; rr<15; rr++ {
        var sb strings.Builder
        for cc:=0; cc<15; cc++ {
            ml := ng.Board().GetLetter(rr,cc)
            if ml == 0 { sb.WriteByte(' ') } else { sb.WriteString(alph.Letter(ml)) }
        }
        rows[rr] = sb.String()
    }
    out := map[string]any{
        "id": s.ID,
        "turn": turn,
        "events": len(hist.Events),
        "onturn": ng.PlayerOnTurn(),
        "board_rows": rows,
        "rack": ng.RackFor(ng.PlayerOnTurn()).String(),
        "rack_you": ng.RackFor(0).String(),
        "rack_bot": ng.RackFor(1).String(),
        "bag": ng.Bag().TilesRemaining(),
        "ruleset": s.Ruleset,
        "lexicon": s.Lexicon,
        "play_state": ng.Playing().String(),
        "score": []int{ ng.PointsFor(0), ng.PointsFor(1) },
    }
    writeJSON(w, http.StatusOK, out)
}

// MovesAt returns generated moves (static equity) for a given match position at turn N.
// Query: ?turn=<n>
func (m *MatchHandlers) MovesAt(w http.ResponseWriter, r *http.Request){
    id := m.pathID(r.URL.Path)
    m.mu.RLock(); s := m.byID[id]; m.mu.RUnlock()
    if s==nil { writeJSON(w,http.StatusNotFound, map[string]string{"error":"not found"}); return }
    turn := 0
    if t := r.URL.Query().Get("turn"); t != "" {
        if n, err := strconv.Atoi(t); err == nil && n >= 0 { turn = n }
    }
    mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
    iters, plies, topk, threads := 300, 2, 20, 1
    if v := r.URL.Query().Get("iters"); v != "" { if n,err:=strconv.Atoi(v); err==nil && n>0 { iters=n } }
    if v := r.URL.Query().Get("plies"); v != "" { if n,err:=strconv.Atoi(v); err==nil && n>0 { plies=n } }
    if v := r.URL.Query().Get("topK"); v != "" { if n,err:=strconv.Atoi(v); err==nil && n>0 { topk=n } }
    if v := r.URL.Query().Get("threads"); v != "" { if n,err:=strconv.Atoi(v); err==nil && n>0 { threads=n } }
    hist := s.Game.History()
    rules := s.Game.Rules()
    ng, err := game.NewFromHistory(hist, rules, 0)
    if err != nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()}); return }
    if turn > len(hist.Events) { turn = len(hist.Events) }
    if turn < 0 { turn = 0 }
    if err := ng.PlayToTurn(turn); err != nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()}); return }
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
    if hp, ok := mg.(hasPlays); ok { plays = hp.Plays() }
    res := MovesResponse{}
    toMove := func(pm *move.Move) Move {
        if pm == nil { return Move{} }
        r, c, v := pm.CoordsAndVertical(); dir := "H"; if v { dir = "V" }
        lv := 0.0; if csc != nil { lv = csc.LeaveValue(pm.Leave()) } else { lv = pm.Equity() - float64(pm.Score()) }
        eq := pm.Equity(); if eq == 0 { eq = float64(pm.Score()) + lv }
        raw := strings.ReplaceAll(pm.TilesString(), ".", ""); word := normalizeWordToBrackets(raw)
        typ := "PLAY"
        switch pm.Action() {
        case move.MoveTypeExchange:
            typ = "EXCH"
        case move.MoveTypePass:
            typ = "PASS"
        }
        return Move{ Type: typ, Word: word, Row: r, Col: c, Dir: dir, Score: pm.Score(), Leave: pm.LeaveString(), LeaveVal: lv, Equity: eq }
    }
    if mode == "sim" {
        if topk > len(plays) { topk = len(plays) }
        cand := plays[:topk]
        // Ensure exchange/pass are considered even if not in topK
        // Allow exchange only if bag has at least 1 tile
        exchAllowed := ng.Bag().TilesRemaining() >= 1
        if exchAllowed {
            var bestEx *move.Move
            for _, pm := range plays {
                if pm != nil && pm.Action() == move.MoveTypeExchange { bestEx = pm; break }
            }
            if bestEx != nil {
                seen := false
                for _, pm := range cand { if pm == bestEx { seen = true; break } }
                if !seen { cand = append(cand, bestEx) }
            }
        }
        var passMv *move.Move
        for _, pm := range plays {
            if pm != nil && pm.Action() == move.MoveTypePass { passMv = pm; break }
        }
        if passMv != nil {
            seen := false
            for _, pm := range cand { if pm == passMv { seen = true; break } }
            if !seen { cand = append(cand, passMv) }
        }
        // Ensure csc
        if csc == nil { csc, _ = equity.NewCombinedStaticCalculator(s.Lexicon, s.CFG, "", equity.PEGAdjustmentFilename) }
        simmer := &montecarlo.Simmer{}
        simmer.Init(ng, []equity.EquityCalculator{csc}, csc, s.CFG)
        if threads <= 0 { threads = 1 }
        simmer.SetThreads(threads)
        if err := simmer.PrepareSim(plies, cand); err != nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()}); return }
        simmer.SimSingleThread(iters, plies)
        sp := simmer.PlaysByWinProb().PlaysNoLock()
        for _, simPlay := range sp {
            pm := simPlay.Move(); if pm == nil { continue }
            mv := toMove(pm)
            mv.WinPct = 100.0 * simPlay.WinProb()
            res.All = append(res.All, mv)
        }
        if len(res.All) > 0 { res.Best = res.All[0] }
        writeJSON(w, http.StatusOK, res); return
    }
    // Static equity path
    for _, pm := range plays { res.All = append(res.All, toMove(pm)) }
    writeJSON(w, http.StatusOK, res)
}
