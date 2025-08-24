package engine

import (
    "errors"
    "strings"

    "dupman/backend/internal/api"
)

type StubEngine struct{}

func NewStubEngine() *StubEngine { return &StubEngine{} }

// GenAll: stub implementation. Chooses the lexicographically first word-like token from the rack
// (nonsense). Replace with Macondo integration.
func (s *StubEngine) GenAll(board api.Board, rack string, kwgPath, ruleset string) (api.MovesResponse, error) {
    if strings.TrimSpace(rack) == "" {
        return api.MovesResponse{}, errors.New("empty rack")
    }
    // Very naive set of candidates from permutations (trimmed)
    cand := []string{}
    r := strings.ReplaceAll(rack, "?", "")
    if len(r) >= 3 {
        cand = append(cand, r[:3])
    } else {
        cand = append(cand, r)
    }
    best := api.Move{ Word: strings.ToUpper(cand[0]), Row: 7, Col: 7, Dir: "H", Score: len(cand[0]) }
    return api.MovesResponse{ Best: best, Ties: []api.Move{}, All: []api.Move{best} }, nil
}

