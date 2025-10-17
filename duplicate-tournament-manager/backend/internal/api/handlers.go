package api

import (
    "crypto/rand"
    "encoding/hex"
    "encoding/json"
    "errors"
    "fmt"
    "context"
    "bytes"
    "os"
    "os/exec"
    "net/http"
    "strconv"
    "strings"
    "time"
    "path/filepath"

    // Macondo equity + tilemapping for hybrid evaluation
    mequity "github.com/domino14/macondo/equity"
    mconfig "github.com/domino14/macondo/config"
    wgtm "github.com/domino14/word-golib/tilemapping"
    "io"
)

type Engine interface {
    GenAll(board Board, rack string, kwgPath, ruleset string) (MovesResponse, error)
}

type Handlers struct {
    st  *memState
    eng Engine
}

func NewHandlers(eng Engine) *Handlers {
    return &Handlers{st: newMemState(), eng: eng}
}

// Utility
func writeJSON(w http.ResponseWriter, code int, v any) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(code)
    _ = json.NewEncoder(w).Encode(v)
}

func parseNumFromPath(r *http.Request) (int, error) {
    // Expect .../rounds/{num}/...
    parts := strings.Split(r.URL.Path, "/")
    for i := 0; i < len(parts); i++ {
        if parts[i] == "rounds" && i+1 < len(parts) {
            n, err := strconv.Atoi(parts[i+1])
            if err != nil || n < 1 { return 0, errors.New("invalid number") }
            return n, nil
        }
    }
    return 0, errors.New("round number not found")
}

func pathTid(r *http.Request) (string, error) {
    // Expect /tournaments/{tid}/...
    parts := strings.Split(r.URL.Path, "/")
    for i := 0; i < len(parts); i++ {
        if parts[i] == "tournaments" && i+1 < len(parts) {
            return parts[i+1], nil
        }
    }
    return "", errors.New("tournament id not found")
}

func genID(prefix string) string {
    var b [8]byte
    _, _ = rand.Read(b[:])
    return prefix + "_" + hex.EncodeToString(b[:])
}

// Handlers
func (h *Handlers) CreateTournament(w http.ResponseWriter, r *http.Request) {
    var in struct {
        Name        string `json:"name"`
        Ruleset     string `json:"ruleset"`
        LexiconPath string `json:"lexicon_path"`
    }
    if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
        writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
        return
    }
    // initialize empty 15x15 board with spaces
    boardRows := make([]string, 15)
    for i := range boardRows { boardRows[i] = "               " }

    // If no lexicon provided, auto-detect FILE2017.kwg in common locations
    if in.LexiconPath == "" {
        if p := findRootFile("FILE2017.kwg"); p != "" { in.LexiconPath = p }
    } else {
        // Normalize provided lexicon path to an absolute/accessible path
        if _, err := os.Stat(in.LexiconPath); err != nil {
            if p := findRootFile(in.LexiconPath); p != "" { in.LexiconPath = p }
        }
    }
    // Infer default ruleset if not provided
    rules := in.Ruleset
    if rules == "" {
        lp := in.LexiconPath
        if lp != "" && (containsCI(lp, "FILE2017") || containsCI(lp, "FISE") || containsCI(lp, "OSPS")) {
            rules = "OSPS49"
        } else {
            rules = "NWL23"
        }
    }

    t := &Tournament{
        ID:          genID("t"),
        Name:        in.Name,
        Ruleset:     rules,
        LexiconPath: in.LexiconPath,
        CreatedAt:   time.Now(),
        BoardRows:   boardRows,
    }
    h.st.mu.Lock()
    h.st.tournaments[t.ID] = t
    if _, ok := h.st.playersByT[t.ID]; !ok { h.st.playersByT[t.ID] = make(map[string]*Player) }
    h.st.mu.Unlock()
    writeJSON(w, http.StatusOK, t)
}

func (h *Handlers) AddPlayer(w http.ResponseWriter, r *http.Request) {
    tid, err := pathTid(r)
    if err != nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error":"bad path"}); return }
    h.st.mu.RLock()
    _, ok := h.st.tournaments[tid]
    h.st.mu.RUnlock()
    if !ok { writeJSON(w, http.StatusNotFound, map[string]string{"error":"tournament not found"}); return }

    var in struct{ Name string `json:"name"` }
    if err := json.NewDecoder(r.Body).Decode(&in); err != nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error":"bad json"}); return }

    p := &Player{ ID: genID("p"), Name: in.Name, TournamentID: tid }
    h.st.mu.Lock()
    h.st.players[p.ID] = p
    h.st.playersByT[tid][p.ID] = p
    h.st.mu.Unlock()
    writeJSON(w, http.StatusOK, p)
}

func (h *Handlers) GetTournament(w http.ResponseWriter, r *http.Request) {
    tid, err := pathTid(r)
    if err != nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error":"bad path"}); return }
    h.st.mu.RLock()
    defer h.st.mu.RUnlock()
    t, ok := h.st.tournaments[tid]
    if !ok { writeJSON(w, http.StatusNotFound, map[string]string{"error":"tournament not found"}); return }
    writeJSON(w, http.StatusOK, t)
}

func (h *Handlers) StartRound(w http.ResponseWriter, r *http.Request) {
    tid, err := pathTid(r)
    if err != nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error":"bad path"}); return }
    h.st.mu.Lock()
    defer h.st.mu.Unlock()
    t, ok := h.st.tournaments[tid]
    if !ok { writeJSON(w, http.StatusNotFound, map[string]string{"error":"tournament not found"}); return }

    var in struct {
        Rack       string     `json:"rack"`
        DeadlineAt *time.Time `json:"deadline_at"`
    }
    if err := json.NewDecoder(r.Body).Decode(&in); err != nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error":"bad json"}); return }

    num := len(h.st.roundsByT[tid]) + 1
    start := append([]string{}, t.BoardRows...)
    rd := &Round{ ID: genID("r"), TournamentID: t.ID, Number: num, Rack: in.Rack, DeadlineAt: in.DeadlineAt, StartBoard: start }
    h.st.roundsByT[tid] = append(h.st.roundsByT[tid], rd)
    writeJSON(w, http.StatusOK, rd)
}

func (h *Handlers) SubmitMove(w http.ResponseWriter, r *http.Request) {
    tid, err := pathTid(r); if err != nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error":"bad path"}); return }
    num, err := parseNumFromPath(r); if err != nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error":"bad round number"}); return }
    var in struct{ PlayerID string `json:"player_id"`; Move Move `json:"move"` }
    if err := json.NewDecoder(r.Body).Decode(&in); err != nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error":"bad json"}); return }


    // Acquire lock to read current state
    h.st.mu.Lock()
    rounds := h.st.roundsByT[tid]
    if num < 1 || num > len(rounds) {
        h.st.mu.Unlock()
        writeJSON(w, http.StatusNotFound, map[string]string{"error":"round not found"}); return
    }
    rd := rounds[num-1]
    if rd.Closed {
        h.st.mu.Unlock()
        writeJSON(w, http.StatusConflict, map[string]string{"error":"round closed"}); return
    }
    if _, ok := h.st.players[in.PlayerID]; !ok {
        h.st.mu.Unlock()
        writeJSON(w, http.StatusNotFound, map[string]string{"error":"player not found"}); return
    }

    // Capture context for engine call then release lock while computing
    t := h.st.tournaments[tid]
    board := Board{ Rows: append([]string{}, t.BoardRows...) }
    rack := rd.Rack
    kwg := t.LexiconPath
    rules := t.Ruleset
    h.st.mu.Unlock()

    // Decide validation mode based on env ENGINE
    strict := os.Getenv("ENGINE") == "macondo"

    mv := in.Move
    // Normalize dir and word
    if mv.Dir == "v" { mv.Dir = "V" } else if mv.Dir == "h" { mv.Dir = "H" }
    if strict {
        // Call engine to validate and score
        res, err := h.eng.GenAll(board, rack, kwg, rules)
        if err != nil {
            writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()}); return
        }
        found := false
        // match on word/row/col/dir (case-insensitive for word)
        wordUp := strings.ToUpper(mv.Word)
        dirUp := strings.ToUpper(mv.Dir)
        for _, cand := range res.All {
            if strings.ToUpper(cand.Word) == wordUp && cand.Row == mv.Row && cand.Col == mv.Col && strings.ToUpper(cand.Dir) == dirUp {
                mv.Score = cand.Score
                found = true
                break
            }
        }
        if !found {
            writeJSON(w, http.StatusBadRequest, map[string]string{"error":"illegal move"}); return
        }
    } else {
        // Stub scoring: word length
        mv.Score = len(mv.Word)
    }

    // Append submission
    sub := &Submission{ ID: genID("s"), RoundID: rd.ID, PlayerID: in.PlayerID, Move: mv, Score: mv.Score, Created: time.Now() }
    h.st.mu.Lock()
    h.st.subsByRound[rd.ID] = append(h.st.subsByRound[rd.ID], sub)
    h.st.mu.Unlock()
    writeJSON(w, http.StatusOK, sub)
}

func (h *Handlers) CloseRound(w http.ResponseWriter, r *http.Request) {
    tid, err := pathTid(r); if err != nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error":"bad path"}); return }
    num, err := parseNumFromPath(r); if err != nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error":"bad round number"}); return }

    h.st.mu.Lock()
    defer h.st.mu.Unlock()
    rounds := h.st.roundsByT[tid]
    if num < 1 || num > len(rounds) { writeJSON(w, http.StatusNotFound, map[string]string{"error":"round not found"}); return }
    rd := rounds[num-1]
    if rd.Closed { writeJSON(w, http.StatusConflict, map[string]string{"error":"round already closed"}); return }

    // Compute master move using engine based on the round's starting board snapshot and the round rack
    t := h.st.tournaments[tid]
    // Keep a copy of current (possibly preview-applied) board for optional tie preference
    currentBoard := append([]string{}, t.BoardRows...)
    base := rd.StartBoard
    if len(base) != 15 { base = t.BoardRows }
    board := Board{Rows: append([]string{}, base...)}
    res, err := h.eng.GenAll(board, rd.Rack, t.LexiconPath, t.Ruleset)
    if err != nil {
        writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
        return
    }
    // Compute best strictly by max score from engine results (robust against wrapper mislabel)
    best := res.Best
    if len(res.All) > 0 {
        best = res.All[0]
        for _, m := range res.All[1:] {
            if m.Score > best.Score { best = m }
        }
    }
    // Prefer a submitted player's move if it ties in score with engine best
    // Gather submissions for this round
    subs := h.st.subsByRound[rd.ID]
    var tie *Move
    for _, s := range subs {
        if s.Score == best.Score {
            m := s.Move
            tie = &m
            break
        }
    }
    // If no explicit submissions tie, but a preview-applied move exists on the board
    // and ties engine best by score, prefer that preview move.
    if tie == nil {
        if mv, ok := diffAppliedMove(base, currentBoard); ok {
            // try to find its score from engine results
            for _, cand := range res.All {
                if strings.EqualFold(cand.Word, mv.Word) && cand.Row == mv.Row && cand.Col == mv.Col && strings.EqualFold(cand.Dir, mv.Dir) {
                    if cand.Score == best.Score { tmp := cand; tie = &tmp }
                    break
                }
            }
        }
    }
    if tie != nil { rd.MasterMove = tie } else { rd.MasterMove = &best }
    rd.Closed = true
    if rd.MasterMove != nil && rd.MasterMove.Word != "" {
        // Reset tournament board to round start to avoid preview/apply side-effects
        t.BoardRows = append([]string{}, base...)
        applyMoveToBoard(&t.BoardRows, *rd.MasterMove)
    }
    writeJSON(w, http.StatusOK, rd)
}

// diffAppliedMove compares two boards (tokenized as bracket-aware rows) and returns
// a single placed move if the difference is exactly one contiguous line of new tiles
// on empty cells. It does not allow overwriting existing tiles.
func diffAppliedMove(base, curr []string) (Move, bool) {
    if len(base) != 15 || len(curr) != 15 { return Move{}, false }
    type coord struct{ r, c int }
    diffs := []coord{}
    for r := 0; r < 15; r++ {
        tb := tokenizeRow(base[r])
        tc := tokenizeRow(curr[r])
        if len(tb) < 15 { pad := make([]string, 15); copy(pad, tb); tb = pad }
        if len(tc) < 15 { pad := make([]string, 15); copy(pad, tc); tc = pad }
        for c := 0; c < 15; c++ {
            if tb[c] == tc[c] { continue }
            // allow only placing on empties
            if tb[c] != " " && tc[c] != tb[c] { return Move{}, false }
            if tb[c] == " " && tc[c] != " " { diffs = append(diffs, coord{r, c}) }
        }
    }
    if len(diffs) == 0 { return Move{}, false }
    // Check alignment
    sameRow := true; sameCol := true
    r0, c0 := diffs[0].r, diffs[0].c
    for _, d := range diffs[1:] {
        if d.r != r0 { sameRow = false }
        if d.c != c0 { sameCol = false }
    }
    if !(sameRow || sameCol) { return Move{}, false }
    // Sort diffs by position
    if sameRow {
        // horizontal
        // find min/max col
        minc, maxc := c0, c0
        for _, d := range diffs { if d.c < minc { minc = d.c }; if d.c > maxc { maxc = d.c } }
        // build word from curr tokens along the line from minc to maxc
        toks := tokenizeRow(curr[r0])
        if len(toks) < 15 { pad := make([]string, 15); copy(pad, toks); toks = pad }
        word := ""
        for c := minc; c <= maxc; c++ { word += toks[c] }
        return Move{ Word: word, Row: r0, Col: minc, Dir: "H" }, true
    }
    // vertical
    minr, maxr := r0, r0
    for _, d := range diffs { if d.r < minr { minr = d.r }; if d.r > maxr { maxr = d.r } }
    word := ""
    for r := minr; r <= maxr; r++ {
        toks := tokenizeRow(curr[r])
        if len(toks) < 15 { pad := make([]string, 15); copy(pad, toks); toks = pad }
        word += toks[c0]
    }
    return Move{ Word: word, Row: minr, Col: c0, Dir: "V" }, true
}

func (h *Handlers) GetStandings(w http.ResponseWriter, r *http.Request) {
    tid, err := pathTid(r)
    if err != nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error":"bad path"}); return }
    h.st.mu.RLock()
    defer h.st.mu.RUnlock()

    // gather players
    pmap := h.st.playersByT[tid]
    if pmap == nil { writeJSON(w, http.StatusNotFound, map[string]string{"error":"tournament not found"}); return }

    // init rows
    rows := map[string]*StandingsRow{}
    for _, p := range pmap {
        rows[p.ID] = &StandingsRow{ PlayerID: p.ID, PlayerName: p.Name }
    }

    // accumulate per round
    for _, rd := range h.st.roundsByT[tid] {
        var master int
        if rd.MasterMove != nil { master = rd.MasterMove.Score }
        for _, s := range h.st.subsByRound[rd.ID] {
            r := rows[s.PlayerID]
            r.TotalScore += s.Score
            r.Submissions++
            if master > 0 {
                r.PctVsMaster += float64(s.Score) / float64(master)
            }
        }
    }
    // average pct per round
    out := make([]*StandingsRow, 0, len(rows))
    rounds := len(h.st.roundsByT[tid])
    for _, r := range rows {
        if rounds > 0 { r.PctVsMaster = r.PctVsMaster / float64(rounds) }
        out = append(out, r)
    }
    writeJSON(w, http.StatusOK, out)
}

// SetBoard updates a tournament's board rows (15 strings of length 15).
// Body: { rows: ["...............", ..., 15] } '.' is normalized to space.
func (h *Handlers) SetBoard(w http.ResponseWriter, r *http.Request) {
    tid, err := pathTid(r)
    if err != nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error":"bad path"}); return }
    var in struct{ Rows []string `json:"rows"` }
    if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
        writeJSON(w, http.StatusBadRequest, map[string]string{"error":"bad json"}); return
    }
    if len(in.Rows) != 15 { writeJSON(w, http.StatusBadRequest, map[string]string{"error":"rows must be 15 strings"}); return }
    norm := make([]string, 15)
    for i := 0; i < 15; i++ {
        s := in.Rows[i]
        s = replaceDotsWithSpaces(s)
        // Accept bracket-coded tokens; ensure there are 15 tiles (not chars). If less, pad with spaces.
        tks := tokenizeRow(s)
        if len(tks) < 15 {
            pad := make([]string, 15)
            copy(pad, tks)
            for j := len(tks); j < 15; j++ { pad[j] = " " }
            s = ""
            for _, tk := range pad { s += tk }
        }
        norm[i] = s
    }
    h.st.mu.Lock()
    defer h.st.mu.Unlock()
    t, ok := h.st.tournaments[tid]
    if !ok { writeJSON(w, http.StatusNotFound, map[string]string{"error":"tournament not found"}); return }
    t.BoardRows = norm
    writeJSON(w, http.StatusOK, t)
}

func (h *Handlers) Moves(w http.ResponseWriter, r *http.Request) {
    var req MovesRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error":"bad json"}); return }
    // Auto-inject FILE2017 if no KWG provided
    if strings.TrimSpace(req.KWGPath) == "" {
        // Prefer FISE2016_converted.kwg for Spanish rulesets by default; fallback to FILE2017.kwg
        if req.Ruleset == "" { req.Ruleset = "OSPS49" }
        // Try explicit FISE first
        if p := findRootFile("FISE2016_converted.kwg"); p != "" {
            req.KWGPath = p
        } else if p := findRootFile("FILE2017.kwg"); p != "" {
            req.KWGPath = p
        }
    } else {
        // Resolve relative or non-existing kwg path using common roots
        if _, err := os.Stat(req.KWGPath); err != nil {
            cand := findRootFile(req.KWGPath)
            if cand == "" {
                // try base filename in common roots
                cand = findRootFile(baseName(req.KWGPath))
            }
            if cand != "" { req.KWGPath = cand }
        }
    }
    // Normalize KWG path to absolute if possible
    if p := strings.TrimSpace(req.KWGPath); p != "" {
        if abs, err := filepath.Abs(p); err == nil { req.KWGPath = abs }
    }
    // If sim mode is requested and engine is macondo, shell out to MACONDO_BIN directly to preserve sim params
    // Optional override: choose engine by query param
    engineName := r.URL.Query().Get("engine")
    var res MovesResponse
    var err error
    if (engineName == "macondo" || engineName == "") && (strings.EqualFold(req.Mode, "sim") || req.Sim != nil) {
        bin := os.Getenv("MACONDO_BIN")
        if strings.TrimSpace(bin) == "" {
            writeJSON(w, http.StatusBadRequest, map[string]string{"error": "MACONDO_BIN not configured"}); return
        }
        // Ensure KLV2 if available
        if strings.TrimSpace(req.KLV2Path) == "" {
            lex := baseName(req.KWGPath)
            tryNames := []string{lex + ".klv2", "FILE2017.klv2", "FISE2017.klv2"}
            if dir := os.Getenv("KLV2_DIR"); dir != "" {
                for _, nm := range tryNames {
                    cand := dir + "/" + nm
                    if st, e := os.Stat(cand); e == nil && st.Size() > 0 { req.KLV2Path = cand; break }
                }
            }
            if strings.TrimSpace(req.KLV2Path) == "" {
                for _, nm := range tryNames {
                    if p := findRootFile(nm); p != "" { req.KLV2Path = p; break }
                }
            }
        }
        in, _ := json.Marshal(req)
        // Allow longer time for simulation; configurable via MACONDO_SIM_TIMEOUT_MS (default 45s)
        simTO := 45 * time.Second
        if s := strings.TrimSpace(os.Getenv("MACONDO_SIM_TIMEOUT_MS")); s != "" {
            if ms, err := time.ParseDuration(s + "ms"); err == nil && ms > 0 { simTO = ms }
        }
        ctx, cancel := context.WithTimeout(r.Context(), simTO)
        defer cancel()
        cmd := exec.CommandContext(ctx, bin)
        cmd.Stdin = bytes.NewReader(in)
        var outBuf, errBuf bytes.Buffer
        cmd.Stdout = &outBuf
        cmd.Stderr = &errBuf
        if e := cmd.Run(); e != nil {
            if ctx.Err() == context.DeadlineExceeded {
                err = errors.New("macondo timeout")
            } else {
                err = fmt.Errorf("macondo exec error: %v (stderr: %s)", e, errBuf.String())
            }
        } else if e := json.Unmarshal(outBuf.Bytes(), &res); e != nil {
            err = fmt.Errorf("invalid engine json: %v", e)
        }
    } else if engineName == "wolges" || engineName == "hybrid" {
        // Detect FISE-style KWG (digraph tiles as single chars)
        hasDig := kwgHasDigraphs(req.KWGPath)
        // Prepare request possibly remapped for Wolges
        sendReq := req
        if hasDig {
            sendReq.Board.Rows = mapBoardRowsToFISE(req.Board.Rows)
            sendReq.Rack = mapRackToFISE(req.Rack)
        }
        if bin := os.Getenv("WOLGES_BIN"); bin != "" {
            in, _ := json.Marshal(sendReq)
            ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
            defer cancel()
            cmd := exec.CommandContext(ctx, bin)
            cmd.Stdin = bytes.NewReader(in)
            var outBuf, errBuf bytes.Buffer
            cmd.Stdout = &outBuf
            cmd.Stderr = &errBuf
            if e := cmd.Run(); e != nil {
                if ctx.Err() == context.DeadlineExceeded {
                    err = errors.New("wolges timeout")
                } else {
                    err = fmt.Errorf("wolges exec error: %v (stderr: %s)", e, errBuf.String())
                }
            } else if e := json.Unmarshal(outBuf.Bytes(), &res); e != nil {
                err = fmt.Errorf("invalid engine json: %v", e)
            }
        } else {
            err = errors.New("WOLGES_BIN not configured")
        }
        // Normalize Wolges words only when using a lexicon with digraph tiles (e.g., FISE2016)
        if err == nil && hasDig {
            for i := range res.All { res.All[i].Word = normalizeWordToBrackets(res.All[i].Word) }
            res.Best.Word = normalizeWordToBrackets(res.Best.Word)
            for i := range res.Ties { res.Ties[i].Word = normalizeWordToBrackets(res.Ties[i].Word) }
        }
        if err == nil && engineName == "hybrid" {
            if e := h.enrichWithMacondoEquity(&res, req, hasDig); e != nil {
                // best effort; keep wolges list
                _ = e
            }
        }
    } else {
        res, err = h.eng.GenAll(req.Board, req.Rack, req.KWGPath, req.Ruleset)
    }
    if err != nil { writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()}); return }
    writeJSON(w, http.StatusOK, res)
}

// enrichWithMacondoEquity annotates Wolges moves with leave/leaveVal/equity from Macondo.
func (h *Handlers) enrichWithMacondoEquity(res *MovesResponse, req MovesRequest, hasDig bool) error {
    if res == nil { return nil }
    // Build Macondo config
    cfg := mconfig.DefaultConfig()
    if dp := os.Getenv("MACONDO_DATA_PATH"); strings.TrimSpace(dp) != "" {
        cfg.Set(mconfig.ConfigDataPath, dp)
    } else {
        for _, p := range []string{"../../macondo/data", "../macondo/data", "macondo/data"} {
            if st, err := os.Stat(p); err == nil && st.IsDir() { cfg.Set(mconfig.ConfigDataPath, p); break }
        }
    }
    // Distribution (Spanish for OSPS ruleset)
    dist := "English"
    if containsCI(req.Ruleset, "osps") || containsCI(req.Ruleset, "spanish") { dist = "Spanish" }
    ld, err := wgtm.GetDistribution(cfg.WGLConfig(), dist)
    if err != nil { return err }
    // Ensure leaves file is staged for this lexicon if available
    lex := baseName(req.KWGPath)
    _ = stageLeavesIfAvailable(cfg, lex)
    // If no KLV2 for this lexicon, fall back to FILE2017 leaves (proxy)
    useLex := lex
    if !hasLeavesAvailable(cfg, lex) {
        useLex = "FILE2017"
        _ = stageLeavesIfAvailable(cfg, useLex)
    }
    // Combined static calculator
    csc, _ := mequity.NewCombinedStaticCalculator(useLex, cfg, mequity.LeavesFilename, mequity.PEGAdjustmentFilename)

    for i := range res.All {
        mv := &res.All[i]
        leaveStr := computeLeaveString(req.Board.Rows, *mv, req.Rack, hasDig)
        mv.Leave = leaveStr
        if csc != nil {
            if mls, e := wgtm.ToMachineLetters(leaveStr, ld.TileMapping()); e == nil {
                lv := csc.LeaveValue(wgtm.MachineWord(mls))
                mv.LeaveVal = lv
                mv.Equity = float64(mv.Score) + lv
            } else {
                mv.Equity = float64(mv.Score)
            }
        } else {
            mv.Equity = float64(mv.Score)
        }
    }
    return nil
}

func baseName(p string) string {
    if p == "" { return "" }
    if i := strings.LastIndexAny(p, "/\\"); i >= 0 { p = p[i+1:] }
    if j := strings.LastIndex(p, "."); j >= 0 { p = p[:j] }
    return p
}

// stageLeavesIfAvailable tries to copy <lexicon>.klv2 into MACONDO_DATA_PATH/strategy/<lexicon>/leaves.klv2
func stageLeavesIfAvailable(cfg *mconfig.Config, lexicon string) error {
    if strings.TrimSpace(lexicon) == "" { return nil }
    // Find klv2 near repo root
    src := findRootFile(lexicon + ".klv2")
    if src == "" { return nil }
    dataDir := cfg.GetString(mconfig.ConfigDataPath)
    if strings.TrimSpace(dataDir) == "" { return nil }
    dstDir := dataDir + "/strategy/" + lexicon
    _ = os.MkdirAll(dstDir, 0o755)
    dst := dstDir + "/" + mequity.LeavesFilename
    // Copy if missing or size differs
    si, _ := os.Stat(src)
    di, derr := os.Stat(dst)
    if derr == nil && si != nil && di.Size() == si.Size() { return nil }
    in, err := os.Open(src)
    if err != nil { return err }
    defer in.Close()
    out, err := os.Create(dst)
    if err != nil { return err }
    defer func() { _ = out.Close() }()
    if _, err := io.Copy(out, in); err != nil { return err }
    return out.Sync()
}

// hasLeavesAvailable checks if a leaves.klv2 is available for the given lexicon
func hasLeavesAvailable(cfg *mconfig.Config, lexicon string) bool {
    if strings.TrimSpace(lexicon) == "" { return false }
    // Check staged path
    dataDir := cfg.GetString(mconfig.ConfigDataPath)
    if strings.TrimSpace(dataDir) != "" {
        dst := dataDir + "/strategy/" + lexicon + "/" + mequity.LeavesFilename
        if st, err := os.Stat(dst); err == nil && st.Size() > 0 { return true }
    }
    // Check root files
    if p := findRootFile(lexicon + ".klv2"); p != "" { return true }
    return false
}

// computeLeaveString builds the remaining rack after placing only new tiles.
func computeLeaveString(rows []string, mv Move, rack string, hasDig bool) string {
    bag := rackTokens(rack, hasDig)
    // traverse move path
    toks := tokenizeRow(strings.ReplaceAll(mv.Word, ".", " "))
    r, c := mv.Row, mv.Col
    dr, dc := 0, 1
    if strings.ToUpper(mv.Dir) == "V" { dr, dc = 1, 0 }
    for _, tk := range toks {
        if r < 0 || r >= 15 || c < 0 || c >= 15 { break }
        bt := ""
        if r >= 0 && r < 15 { br := tokenizeRow(replaceDotsWithSpaces(rows[r])); if c >= 0 && c < len(br) { bt = br[c] } }
        if strings.TrimSpace(bt) == "" {
            // need to consume from rack
            rem := tk
            if isLowerToken(tk) { rem = "?" }
            bag, _ = multisetRemove(bag, rem)
        }
        r += dr; c += dc
    }
    // join remaining
    var b strings.Builder
    for _, t := range bag { b.WriteString(t) }
    return b.String()
}

func rackTokens(rack string, hasDig bool) []string {
    s := strings.TrimSpace(rack)
    rs := []rune(s)
    out := []string{}
    for i := 0; i < len(rs); i++ {
        ch := rs[i]
        if ch == '?' { out = append(out, "?"); continue }
        if hasDig && i+1 < len(rs) {
            a, b := rs[i], rs[i+1]
            au, bu := a, b
            if au >= 'a' && au <= 'z' { au -= 32 }
            if bu >= 'a' && bu <= 'z' { bu -= 32 }
            if au == 'C' && bu == 'H' { out = append(out, "[CH]"); i++; continue }
            if au == 'L' && bu == 'L' { out = append(out, "[LL]"); i++; continue }
            if au == 'R' && bu == 'R' { out = append(out, "[RR]"); i++; continue }
        }
        out = append(out, strings.ToUpper(string(ch)))
    }
    return out
}

func isLowerToken(tk string) bool {
    if tk == "" { return false }
    if strings.HasPrefix(tk, "[") && strings.HasSuffix(tk, "]") {
        inner := tk[1:len(tk)-1]
        return inner == strings.ToLower(inner)
    }
    return tk == strings.ToLower(tk) && tk != strings.ToUpper(tk)
}

// multisetRemove removes one occurrence of token from bag; returns updated slice and whether removed.
func multisetRemove(bag []string, token string) ([]string, bool) {
    for i, t := range bag {
        if t == token {
            bag[i] = bag[len(bag)-1]
            return bag[:len(bag)-1], true
        }
    }
    return bag, false
}

// Reset clears all in-memory state (hard reset)
func (h *Handlers) Reset(w http.ResponseWriter, r *http.Request) {
    // Replace with a fresh state to avoid dangling references
    h.st.mu.Lock()
    h.st.tournaments = make(map[string]*Tournament)
    h.st.players = make(map[string]*Player)
    h.st.playersByT = make(map[string]map[string]*Player)
    h.st.roundsByT = make(map[string][]*Round)
    h.st.subsByRound = make(map[string][]*Submission)
    h.st.mu.Unlock()
    writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "state reset"})
}

// applyMoveToBoard mutates boardRows (15 lines of 15 chars) by placing the move's word
// onto empty cells along the given direction starting at row,col. Existing letters are preserved.
func applyMoveToBoard(boardRows *[]string, mv Move) {
    if boardRows == nil || len(*boardRows) != 15 || mv.Word == "" { return }
    // Tokenize existing rows into 15 tiles per row
    tokens := make([][]string, 15)
    for i := 0; i < 15; i++ {
        row := ""
        if i < len((*boardRows)) { row = (*boardRows)[i] }
        row = replaceDotsWithSpaces(row)
        tks := tokenizeRow(row)
        // ensure exactly 15 cells
        line := make([]string, 15)
        copy(line, tks)
        for j := 0; j < 15; j++ { if line[j] == "" { line[j] = " " } }
        tokens[i] = line
    }
    // Tokenize move word, honoring [digraph] and lowercase blanks
    placeToks := tokenizeRow(mv.Word)
    r, c := mv.Row, mv.Col
    dr, dc := 0, 1
    if mv.Dir == "V" || mv.Dir == "v" { dr, dc = 1, 0 }
    // Walk along the path; only consume a token when the board cell is empty (anchor-aware)
    ti := 0
    for r >= 0 && r < 15 && c >= 0 && c < 15 {
        if ti >= len(placeToks) { break }
        if tokens[r][c] == " " {
            tokens[r][c] = placeToks[ti]
            ti++
        }
        r += dr; c += dc
    }
    // Join back to strings (concatenate tokens; tile count stays 15 even if string length varies)
    out := make([]string, 15)
    for i := 0; i < 15; i++ {
        s := ""
        for j := 0; j < 15; j++ { s += tokens[i][j] }
        out[i] = s
    }
    *boardRows = out
}

func replaceDotsWithSpaces(s string) string {
    runeArr := []rune(s)
    for i, ch := range runeArr {
        if ch == '.' { runeArr[i] = ' ' }
    }
    return string(runeArr)
}

// tokenizeRow splits a row or move string into tile tokens:
//  - "[ll]" style digraphs are single tokens
//  - lowercase letters indicate blanks and are preserved
//  - spaces are preserved as " " tokens
func tokenizeRow(s string) []string {
    out := []string{}
    rs := []rune(s)
    for i := 0; i < len(rs); {
        ch := rs[i]
        if ch == '[' {
            // collect until closing ']'
            j := i + 1
            for j < len(rs) && rs[j] != ']' { j++ }
            if j < len(rs) && rs[j] == ']' {
                out = append(out, string(rs[i:j+1]))
                i = j + 1
                continue
            }
            // malformed; treat '[' as literal
            out = append(out, string(ch))
            i++
            continue
        }
        if ch == ' ' || ch == '.' {
            out = append(out, " ")
            i++
            continue
        }
        // single rune token (upper/lower preserved)
        out = append(out, string(ch))
        i++
    }
    return out
}

// containsCI: case-insensitive substring check
func containsCI(s, sub string) bool {
    if s == "" || sub == "" { return false }
    S := s
    U := sub
    // lower both without importing strings to keep patch minimal here
    // naive: iterate and normalize ASCII only
    toLower := func(t string) string {
        b := []byte(t)
        for i, c := range b { if c >= 'A' && c <= 'Z' { b[i] = c + 32 } }
        return string(b)
    }
    S = toLower(S)
    U = toLower(U)
    // substring search
    return (len(U) <= len(S)) && (S == U || (len(U) > 0 && (len(S) > 0 && (indexOf(S, U) >= 0))) )
}

func indexOf(hay, needle string) int {
    // simple O(n*m)
    n := len(hay); m := len(needle)
    if m == 0 { return 0 }
    for i := 0; i+m <= n; i++ {
        if hay[i:i+m] == needle { return i }
    }
    return -1
}

// findRootFile tries to locate a file at repo root or CWD (when server runs in backend/)
func findRootFile(name string) string {
    if name == "" { return "" }
    // Try current working dir
    if _, err := os.Stat(name); err == nil { return name }
    // Try ../../name (repo root when cwd is backend)
    if _, err := os.Stat("../../" + name); err == nil { return "../../" + name }
    // Try ../name (when cwd is duplicate-tournament-manager)
    if _, err := os.Stat("../" + name); err == nil { return "../" + name }
    // Try lexica/ subfolder variants
    if _, err := os.Stat("lexica/" + name); err == nil { return "lexica/" + name }
    if _, err := os.Stat("../lexica/" + name); err == nil { return "../lexica/" + name }
    if _, err := os.Stat("../../lexica/" + name); err == nil { return "../../lexica/" + name }
    // Try lexica/gaddag/ subfolder variants
    if _, err := os.Stat("lexica/gaddag/" + name); err == nil { return "lexica/gaddag/" + name }
    if _, err := os.Stat("../lexica/gaddag/" + name); err == nil { return "../lexica/gaddag/" + name }
    if _, err := os.Stat("../../lexica/gaddag/" + name); err == nil { return "../../lexica/gaddag/" + name }
    // Try cwd/../.. resolved to absolute
    if wd, err := os.Getwd(); err == nil {
        base := wd
        if strings.HasSuffix(base, "/duplicate-tournament-manager/backend") {
            cand := base + "/../../" + name
            if _, err := os.Stat(cand); err == nil { return cand }
        }
        if strings.HasSuffix(base, "/duplicate-tournament-manager") {
            cand := base + "/../" + name
            if _, err := os.Stat(cand); err == nil { return cand }
            cand2 := base + "/../lexica/" + name
            if _, err := os.Stat(cand2); err == nil { return cand2 }
        }
    }
    return ""
}

// removeTokensFromRack removes tiles from a rack string (used tiles from manual rack)
func removeTokensFromRack(rack, toRemove string) string {
	rackTokens := tokenizeRow(rack)
	removeTokens := tokenizeRow(toRemove)

	// Create a copy of rack tokens
	remaining := make([]string, len(rackTokens))
	copy(remaining, rackTokens)

	// Remove each tile in toRemove from remaining
	for _, tile := range removeTokens {
		if tile == " " {
			continue
		}
		// Find and remove first occurrence
		for i, rt := range remaining {
			if strings.EqualFold(rt, tile) {
				// Remove this token
				remaining = append(remaining[:i], remaining[i+1:]...)
				break
			}
		}
	}

	return strings.Join(remaining, "")
}

// normalizeWordToBrackets collapses Spanish digraphs and internal letters to bracket-coded tokens.
func normalizeWordToBrackets(s string) string {
    if s == "" { return s }
    rs := []rune(s)
    var out strings.Builder
    for i := 0; i < len(rs); {
        r := rs[i]
        if r == '[' {
            j := i+1
            for j < len(rs) && rs[j] != ']' { j++ }
            if j < len(rs) && rs[j] == ']' {
                inner := string(rs[i+1:j])
                // Map single-letter FISE forms inside brackets to bracket-coded digraphs
                switch inner {
                case "Ç": out.WriteString("[CH]"); i = j+1; continue
                case "ç": out.WriteString("[ch]"); i = j+1; continue
                case "K": out.WriteString("[LL]"); i = j+1; continue
                case "k": out.WriteString("[ll]"); i = j+1; continue
                case "W": out.WriteString("[RR]"); i = j+1; continue
                case "w": out.WriteString("[rr]"); i = j+1; continue
                default:
                    out.WriteString("[" + inner + "]")
                    i = j+1
                    continue
                }
            }
        }
        // FISE2016 mapping (confirmed): CH=Ç, LL=K, RR=W
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
            if a == 'C' && b == 'H' { out.WriteString("[CH]"); i+=2; continue }
            if a == 'L' && b == 'L' { out.WriteString("[LL]"); i+=2; continue }
            if a == 'R' && b == 'R' { out.WriteString("[RR]"); i+=2; continue }
            if a == 'c' && b == 'h' { out.WriteString("[ch]"); i+=2; continue }
            if a == 'l' && b == 'l' { out.WriteString("[ll]"); i+=2; continue }
            if a == 'r' && b == 'r' { out.WriteString("[rr]"); i+=2; continue }
            if (a == 'c' && b == 'H') || (a == 'C' && b == 'h') { out.WriteString("[CH]"); i+=2; continue }
            if (a == 'l' && b == 'L') || (a == 'L' && b == 'l') { out.WriteString("[LL]"); i+=2; continue }
            if (a == 'r' && b == 'R') || (a == 'R' && b == 'r') { out.WriteString("[RR]"); i+=2; continue }
        }
        out.WriteRune(r)
        i++
    }
    return out.String()
}

// kwgHasDigraphs heuristically determines if the KWG uses digraph tiles (FISE-style).
func kwgHasDigraphs(kwg string) bool {
    if kwg == "" { return false }
    k := strings.ToLower(kwg)
    if strings.Contains(k, "fise") || strings.Contains(k, "2016") { return true }
    if strings.Contains(k, "2017") { return false }
    return false
}

// mapBoardRowsToFISE maps bracket-coded tokens on the board to single-char letters used in FISE (Ç/K/W and lowercase for blanks).
func mapBoardRowsToFISE(rows []string) []string {
    out := make([]string, 15)
    for r := 0; r < 15 && r < len(rows); r++ {
        toks := tokenizeRow(replaceDotsWithSpaces(rows[r]))
        line := make([]string, 0, 15)
        for _, tk := range toks {
            if tk == "" { tk = " " }
            if strings.HasPrefix(tk, "[") && strings.HasSuffix(tk, "]") {
                inner := tk[1:len(tk)-1]
                switch inner {
                case "CH": line = append(line, "Ç")
                case "ch": line = append(line, "ç")
                case "LL": line = append(line, "K")
                case "ll": line = append(line, "k")
                case "RR": line = append(line, "W")
                case "rr": line = append(line, "w")
                default: line = append(line, tk)
                }
            } else {
                line = append(line, tk)
            }
        }
        // pad to 15
        for len(line) < 15 { line = append(line, " ") }
        // join
        sb := strings.Builder{}
        for i := 0; i < 15; i++ { sb.WriteString(line[i]) }
        out[r] = sb.String()
    }
    // pad remaining rows with spaces
    for r := len(rows); r < 15; r++ { out[r] = "               " }
    return out
}

// mapRackToFISE maps a natural or bracket-coded rack to single-char FISE letters (Ç/K/W), preserving '?'.
func mapRackToFISE(rack string) string {
    s := strings.TrimSpace(rack)
    rs := []rune(s)
    var out strings.Builder
    for i := 0; i < len(rs); i++ {
        ch := rs[i]
        if ch == '?' { out.WriteRune(ch); continue }
        if ch == '[' {
            // bracket coded
            j := i+1
            for j < len(rs) && rs[j] != ']' { j++ }
            if j < len(rs) && rs[j] == ']' {
                inner := string(rs[i+1:j])
                switch inner {
                case "CH": out.WriteString("Ç")
                case "ch": out.WriteString("ç")
                case "LL": out.WriteString("K")
                case "ll": out.WriteString("k")
                case "RR": out.WriteString("W")
                case "rr": out.WriteString("w")
                default: out.WriteString("["+inner+"]")
                }
                i = j
                continue
            }
        }
        // natural pairs
        if i+1 < len(rs) {
            a, b := rs[i], rs[i+1]
            au, bu := a, b
            if au >= 'a' && au <= 'z' { au -= 32 }
            if bu >= 'a' && bu <= 'z' { bu -= 32 }
            if au == 'C' && bu == 'H' { out.WriteString("Ç"); i++; continue }
            if au == 'L' && bu == 'L' { out.WriteString("K"); i++; continue }
            if au == 'R' && bu == 'R' { out.WriteString("W"); i++; continue }
        }
        // single letter: uppercase
        out.WriteString(strings.ToUpper(string(ch)))
    }
    return out.String()
}
