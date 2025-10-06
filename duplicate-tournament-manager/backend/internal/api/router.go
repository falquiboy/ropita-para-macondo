package api

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static/* static/**
var staticFS embed.FS

func Router(eng Engine) http.Handler {
	mux := http.NewServeMux()
	h := NewHandlers(eng)
	mh := NewMatchHandlers(eng)

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/moves", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		h.Moves(w, r)
	})

	mux.HandleFunc("/reset", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		h.Reset(w, r)
	})

	mux.HandleFunc("/klv2/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		serveKLV2(w, r)
	})

	// Tournaments subtree (register both root and subtree)
	tournamentsHandler := func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		// Create tournament: accept both /tournaments and /tournaments/
		if r.Method == http.MethodPost && (p == "/tournaments" || p == "/tournaments/") {
			h.CreateTournament(w, r)
			return
		}
		// Normalize: ensure we only handle subtree here
		if !strings.HasPrefix(p, "/tournaments/") {
			http.NotFound(w, r)
			return
		}
		// /tournaments/{tid}/...
		// direct GET tournament: accept with or without trailing slash
		if r.Method == http.MethodGet {
			parts := strings.Split(strings.TrimSuffix(p, "/"), "/")
			// ["", "tournaments", "{tid}"]
			if len(parts) == 3 && parts[1] == "tournaments" {
				h.GetTournament(w, r)
				return
			}
		}
		if r.Method == http.MethodPost && strings.Contains(p, "/players") {
			h.AddPlayer(w, r)
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(p, "/rounds") {
			h.StartRound(w, r)
			return
		}
		if r.Method == http.MethodPost && strings.Contains(p, "/rounds/") && strings.HasSuffix(p, "/submit") {
			h.SubmitMove(w, r)
			return
		}
		if r.Method == http.MethodPost && strings.Contains(p, "/rounds/") && strings.HasSuffix(p, "/close") {
			h.CloseRound(w, r)
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(p, "/board") {
			h.SetBoard(w, r)
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(p, "/standings") {
			h.GetStandings(w, r)
			return
		}
		http.NotFound(w, r)
	}
	mux.HandleFunc("/tournaments/", tournamentsHandler)
	mux.HandleFunc("/tournaments", tournamentsHandler)

	// Matches (head-to-head vs AI)
	mux.HandleFunc("/matches", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			mh.Create(w, r)
		default:
			http.NotFound(w, r)
		}
	})

	// Load GCG for analysis
	mux.HandleFunc("/matches/load-gcg", func(w http.ResponseWriter, r *http.Request) {
		mh.LoadGCG(w, r)
	})

	mux.HandleFunc("/matches/", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/bag"):
			mh.Bag(w, r)
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/unseen"):
			mh.Unseen(w, r)
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/scoresheet"):
			mh.ScoreSheet(w, r)
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/events"):
			mh.Events(w, r)
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/position"):
			mh.Position(w, r)
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/gcg"):
			mh.GCG(w, r)
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/moves"):
			mh.MovesAt(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/abort"):
			mh.Abort(w, r)
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/logs"):
			mh.LogStream(w, r)
		case r.Method == http.MethodGet:
			mh.Get(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/play"):
			mh.Play(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/exchange"):
			mh.Exchange(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/pass"):
			mh.Pass(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/ai_move"):
			mh.AIMove(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/analysis/set_rack"):
			mh.SetRackAnalysis(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/set_rack"):
			mh.SetRack(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/analysis/apply"):
			mh.ApplyManual(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/analysis/undo"):
			mh.Undo(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/analysis/redo"):
			mh.Redo(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/analysis/undo_all"):
			mh.UndoAll(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/analysis/redo_all"):
			mh.RedoAll(w, r)
		default:
			http.NotFound(w, r)
		}
	})

	// Static UI fallback (serve embedded static/ at site root)
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	// Wrap static file server to disable browser caching during active dev
	static := http.FileServer(http.FS(sub))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		static.ServeHTTP(w, r)
	})

	return mux
}
