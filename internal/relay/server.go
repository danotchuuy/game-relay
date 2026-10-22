// Package relay implements a WebSocket relay server for game lobbies.
//
// The wire protocol is documented in docs/SPEC.md, which the server also
// serves at /spec.md.
package relay

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/coder/websocket"

	"github.com/danotchuuy/game-relay/docs"
)

// Server is an http.Handler serving the relay endpoints.
type Server struct {
	cfg Config
	hub *Hub
	mux *http.ServeMux
}

// NewServer returns a relay server using cfg.
func NewServer(cfg Config) *Server {
	cfg = cfg.withDefaults()
	s := &Server{cfg: cfg, hub: newHub(cfg), mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /{$}", s.handleIndex)
	s.mux.HandleFunc("GET /ws", s.handleWS)
	s.mux.HandleFunc("GET /v1/lobbies", s.handleListLobbies)
	s.mux.HandleFunc("GET /v1/stats", s.handleStats)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	s.mux.HandleFunc("GET /spec.md", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Write(docs.Spec)
	})
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// Close disconnects all clients. Call it after http.Server.Shutdown, which
// does not close upgraded WebSocket connections.
func (s *Server) Close() { s.hub.close() }

// Stats returns a snapshot of current load.
func (s *Server) Stats() Stats { return s.hub.stats() }

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, "game-relay: WebSocket lobby relay.\n\n"+
		"  WebSocket:   /ws\n"+
		"  Lobby list:  /v1/lobbies?game_id=<id>\n"+
		"  Protocol:    /spec.md\n")
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	opts := &websocket.AcceptOptions{OriginPatterns: s.cfg.OriginPatterns}
	if len(s.cfg.OriginPatterns) == 0 || slices.Contains(s.cfg.OriginPatterns, "*") {
		opts.InsecureSkipVerify = true
		opts.OriginPatterns = nil
	}
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(s.cfg.MaxMessageBytes)

	c := &Client{
		hub:  s.hub,
		conn: conn,
		id:   s.hub.nextID.Add(1),
		out:  make(chan frame, s.cfg.SendBuffer),
	}
	if !s.hub.register(c) {
		conn.Close(websocket.StatusGoingAway, "server shutting down")
		return
	}
	defer s.hub.unregister(c)
	c.run(r.Context())
}

func (s *Server) handleListLobbies(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	q := r.URL.Query()
	gameID := q.Get("game_id")
	if e := s.hub.checkGameID(gameID); e != nil {
		writeHTTPError(w, http.StatusBadRequest, e)
		return
	}
	req := &request{IncludeFull: q.Get("include_full") == "true"}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeHTTPError(w, http.StatusBadRequest, errf(ErrBadRequest, "limit must be an integer"))
			return
		}
		req.Limit = n
	}
	if v := q.Get("filter"); v != "" {
		if err := json.Unmarshal([]byte(v), &req.Filter); err != nil {
			writeHTTPError(w, http.StatusBadRequest, errf(ErrBadRequest, "filter must be a JSON object"))
			return
		}
	}
	lobbies, err := s.hub.listLobbies(gameID, req)
	if err != nil {
		writeHTTPError(w, http.StatusBadRequest, err.(*Error))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lobbies": lobbies})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.hub.stats())
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeHTTPError(w http.ResponseWriter, status int, e *Error) {
	writeJSON(w, status, map[string]string{"error": e.Code, "message": e.Message})
}
