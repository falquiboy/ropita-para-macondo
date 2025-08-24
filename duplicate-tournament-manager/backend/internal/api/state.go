package api

import (
    "sync"
)

type memState struct {
    mu          sync.RWMutex
    tournaments map[string]*Tournament
    players     map[string]*Player           // by ID
    playersByT  map[string]map[string]*Player // tid -> pid -> Player
    roundsByT   map[string][]*Round          // tid -> rounds (index by number-1)
    subsByRound map[string][]*Submission     // rid -> submissions
}

func newMemState() *memState {
    return &memState{
        tournaments: make(map[string]*Tournament),
        players:     make(map[string]*Player),
        playersByT:  make(map[string]map[string]*Player),
        roundsByT:   make(map[string][]*Round),
        subsByRound: make(map[string][]*Submission),
    }
}

