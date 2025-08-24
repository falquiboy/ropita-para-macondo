package api

import (
    "encoding/json"
    "net/http"
    "strings"
    "sync"

    "dupman/backend/internal/match"
    pb "github.com/domino14/macondo/gen/api/proto/macondo"
    "github.com/domino14/word-golib/tilemapping"
)

type MatchHandlers struct{
    mu sync.RWMutex
    byID map[string]*match.Session
}

func NewMatchHandlers() *MatchHandlers { return &MatchHandlers{byID: map[string]*match.Session{}} }

func (m *MatchHandlers) Create(w http.ResponseWriter, r *http.Request){
    var in struct{ Ruleset string `json:"ruleset"`; KWG string `json:"kwg"` }
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
    var in struct{ Mode string `json:"mode"`; Sim *struct{ Iters,Plies,TopK int } `json:"sim"` }
    _ = json.NewDecoder(r.Body).Decode(&in)
    mode := match.AIStatic
    if strings.EqualFold(in.Mode, "sim") { mode = match.AISim }
    iters, plies, topk := 0,0,0
    if in.Sim!=nil { iters=in.Sim.Iters; plies=in.Sim.Plies; topk=in.Sim.TopK }
    _, err := s.AIMove(mode, iters, plies, topk)
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

// ScoreSheet returns a minimal move history
func (m *MatchHandlers) ScoreSheet(w http.ResponseWriter, r *http.Request){
    id := m.pathID(r.URL.Path)
    m.mu.RLock(); s := m.byID[id]; m.mu.RUnlock()
    if s==nil { writeJSON(w,http.StatusNotFound, map[string]string{"error":"not found"}); return }
    h := s.Game.History()
    type Row struct { Ply int `json:"ply"`; Player int `json:"player"`; Type string `json:"type"`; Word string `json:"word"`; Row int `json:"row"`; Col int `json:"col"`; Dir string `json:"dir"`; Score int `json:"score"`; Cum int `json:"cum"` }
    rows := make([]Row, 0, len(h.GetEvents()))
    for i, e := range h.GetEvents(){
        t := e.GetType().String()
        word := ""
        if len(e.GetWordsFormed())>0 { word = e.GetWordsFormed()[0] }
        dir := "H"; if e.GetDirection() == pb.GameEvent_VERTICAL { dir = "V" }
        rows = append(rows, Row{ Ply:i+1, Player:int(e.GetPlayerIndex()), Type:t, Word:word, Row:int(e.GetRow()), Col:int(e.GetColumn()), Dir:dir, Score:int(e.GetScore()), Cum:int(e.GetCumulative()) })
    }
    writeJSON(w, http.StatusOK, map[string]any{ "id": s.ID, "rows": rows })
}
