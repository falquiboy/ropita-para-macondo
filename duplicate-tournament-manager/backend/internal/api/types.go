package api

import "time"

type Tournament struct {
    ID          string    `json:"id"`
    Name        string    `json:"name"`
    Ruleset     string    `json:"ruleset"`
    LexiconPath string    `json:"lexicon_path"`
    CreatedAt   time.Time `json:"created_at"`
    BoardRows   []string  `json:"board_rows"`
}

type Player struct {
    ID           string `json:"id"`
    Name         string `json:"name"`
    TournamentID string `json:"tournament_id"`
}

type Move struct {
    Type string `json:"type,omitempty"` // PLAY | EXCH | PASS
    Word string `json:"word"`
    Row  int    `json:"row"`
    Col  int    `json:"col"`
    Dir  string `json:"dir"` // H or V
    Score int   `json:"score,omitempty"`
    Leave string `json:"leave"`
    LeaveVal float64 `json:"leaveVal"`
    Equity   float64 `json:"equity"`
    // Simulation metrics (optional)
    WinPct   float64 `json:"winPct,omitempty"`   // percentage [0..100]
    Mean     float64 `json:"mean,omitempty"`     // mean score for this ply if available
    Stdev    float64 `json:"stdev,omitempty"`    // std dev of score for this ply
}

type Round struct {
    ID           string     `json:"id"`
    TournamentID string     `json:"tournament_id"`
    Number       int        `json:"number"`
    Rack         string     `json:"rack"`
    DeadlineAt   *time.Time `json:"deadline_at,omitempty"`
    StartBoard   []string   `json:"start_board_rows,omitempty"`
    MasterMove   *Move      `json:"master_move,omitempty"`
    Closed       bool       `json:"closed"`
}

type Submission struct {
    ID       string    `json:"id"`
    RoundID  string    `json:"round_id"`
    PlayerID string    `json:"player_id"`
    Move     Move      `json:"move"`
    Score    int       `json:"score"`
    Created  time.Time `json:"created_at"`
}

type StandingsRow struct {
    PlayerID      string  `json:"player_id"`
    PlayerName    string  `json:"player_name"`
    TotalScore    int     `json:"total_score"`
    PctVsMaster   float64 `json:"pct_vs_master"`
    Submissions   int     `json:"submissions"`
}

// Moves API contract (stub, replace via Macondo)
type Board struct {
    // Rows: 15 strings of length 15. Use space ' ' for empty.
    Rows []string `json:"rows,omitempty"`
}

type MovesRequest struct {
    Board      Board  `json:"board"`
    Rack       string `json:"rack"`
    KWGPath    string `json:"kwg"`
    Ruleset    string `json:"ruleset"`
    KLV2Path   string `json:"klv2,omitempty"`
    // Optional mode and simulation config
    Mode       string     `json:"mode,omitempty"` // "static" | "sim" | "peg" | "auto"
    Sim        *SimConfig `json:"sim,omitempty"`
}

type MovesResponse struct {
    Best Move   `json:"best"`
    Ties []Move `json:"ties"`
    All  []Move `json:"all"`
}

// SimConfig controls Monte Carlo simulation parameters when supported by the engine.
type SimConfig struct {
    Iters   int `json:"iters,omitempty"`   // iterations (default 100)
    Plies   int `json:"plies,omitempty"`   // lookahead plies (default 2)
    Threads int `json:"threads,omitempty"` // threads (default 1)
    TopK    int `json:"topK,omitempty"`    // number of candidate plays to sim (default 20)
}
