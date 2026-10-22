package relay

import (
	"encoding/json"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Config controls server limits. Zero values are replaced by defaults.
type Config struct {
	MaxMessageBytes   int64         // largest accepted WebSocket message
	MaxPlayers        int           // upper bound for a lobby's max_players
	DefaultMaxPlayers int           // max_players when create_lobby omits it
	MaxLobbies        int           // total open lobbies across all games
	RateLimit         float64       // sustained messages per second per connection; <0 disables
	RateBurst         int           // burst size for the rate limiter
	SendBuffer        int           // queued outbound messages before a slow client is dropped
	HelloTimeout      time.Duration // time allowed between connect and hello
	PingInterval      time.Duration // WebSocket keepalive ping interval
	WriteTimeout      time.Duration
	AllowedGames      []string // if non-empty, only these game IDs may connect
	OriginPatterns    []string // allowed browser origins; empty or "*" allows all
	Logger            *slog.Logger
}

func (c Config) withDefaults() Config {
	setDefault(&c.MaxMessageBytes, 64<<10)
	setDefault(&c.MaxPlayers, 64)
	setDefault(&c.DefaultMaxPlayers, 8)
	setDefault(&c.MaxLobbies, 10000)
	setDefault(&c.RateLimit, 120)
	setDefault(&c.RateBurst, 240)
	setDefault(&c.SendBuffer, 256)
	setDefault(&c.HelloTimeout, 10*time.Second)
	setDefault(&c.PingInterval, 30*time.Second)
	setDefault(&c.WriteTimeout, 10*time.Second)
	c.DefaultMaxPlayers = min(c.DefaultMaxPlayers, c.MaxPlayers)
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

func setDefault[T comparable](v *T, def T) {
	var zero T
	if *v == zero {
		*v = def
	}
}

// Hub owns all connections and lobbies.
//
// Lock order: Hub.mu before Lobby.mu. Code that holds a Lobby.mu never
// acquires Hub.mu.
type Hub struct {
	cfg     Config
	allowed map[string]bool
	nextID  atomic.Uint32

	mu         sync.Mutex
	closed     bool
	clients    map[*Client]struct{}
	games      map[string]map[string]*Lobby // game_id → code → lobby
	lobbyCount int
}

func newHub(cfg Config) *Hub {
	h := &Hub{
		cfg:     cfg,
		clients: make(map[*Client]struct{}),
		games:   make(map[string]map[string]*Lobby),
	}
	if len(cfg.AllowedGames) > 0 {
		h.allowed = make(map[string]bool)
		for _, g := range cfg.AllowedGames {
			h.allowed[g] = true
		}
	}
	return h
}

func (h *Hub) limits() *Limits {
	return &Limits{
		MaxMessageBytes:  h.cfg.MaxMessageBytes,
		MaxPlayers:       h.cfg.MaxPlayers,
		MaxMetadataBytes: maxMetadataBytes,
		RateLimit:        max(h.cfg.RateLimit, 0),
		RateBurst:        h.cfg.RateBurst,
	}
}

func (h *Hub) checkGameID(id string) *Error {
	if !gameIDPattern.MatchString(id) {
		return errf(ErrInvalidGameID, "game_id must be 1-64 characters of A-Z a-z 0-9 . _ : -")
	}
	if h.allowed != nil && !h.allowed[id] {
		return errf(ErrGameNotAllowed, "game_id %q is not allowed on this server", id)
	}
	return nil
}

func (h *Hub) register(c *Client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	h.clients[c] = struct{}{}
	return true
}

func (h *Hub) unregister(c *Client) {
	if l := c.lobby.Load(); l != nil {
		l.leave(c, ReasonDisconnected, nil)
	}
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

// close disconnects every client and rejects new ones.
func (h *Hub) close() {
	h.mu.Lock()
	h.closed = true
	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()

	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Go(func() { c.conn.Close(websocket.StatusGoingAway, "server shutting down") })
	}
	wg.Wait()
}

// Stats is a snapshot of server load.
type Stats struct {
	Connections int `json:"connections"`
	Lobbies     int `json:"lobbies"`
	Games       int `json:"games"`
}

func (h *Hub) stats() Stats {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Stats{Connections: len(h.clients), Lobbies: h.lobbyCount, Games: len(h.games)}
}

func (h *Hub) createLobby(c *Client, req *request) error {
	if c.lobby.Load() != nil {
		return errf(ErrAlreadyInLobby, "leave your current lobby first")
	}
	l := &Lobby{
		hub:              h,
		gameID:           c.gameID,
		createdAt:        time.Now().UTC(),
		closeOnHostLeave: req.CloseOnHostLeave,
		visibility:       VisibilityPrivate,
		maxPlayers:       h.cfg.DefaultMaxPlayers,
		metadata:         json.RawMessage("{}"),
		metaDecoded:      map[string]any{},
		members:          []*Client{c},
		host:             c,
	}
	if req.Locked != nil && *req.Locked {
		return errf(ErrBadRequest, "a lobby cannot be created locked")
	}
	if err := l.apply(req); err != nil {
		return err
	}

	h.mu.Lock()
	if h.lobbyCount >= h.cfg.MaxLobbies {
		h.mu.Unlock()
		return errf(ErrServerFull, "the server has reached its lobby limit; try again later")
	}
	codes := h.games[l.gameID]
	if codes == nil {
		codes = make(map[string]*Lobby)
		h.games[l.gameID] = codes
	}
	for l.code = newCode(); codes[l.code] != nil; l.code = newCode() {
	}
	if l.name == "" {
		l.name = c.name + "'s lobby"
	}
	codes[l.code] = l
	h.lobbyCount++
	// Take the lobby lock before it becomes reachable by joiners so the
	// creator always sees lobby_joined before any peer_joined.
	l.mu.Lock()
	h.mu.Unlock()
	defer l.mu.Unlock()

	c.lobby.Store(l)
	c.send(response{Type: TypeLobbyJoined, Rid: req.Rid, PeerID: c.id, Lobby: l.infoLocked(), Peers: l.peersLocked()})
	h.cfg.Logger.Info("lobby created", "game_id", l.gameID, "code", l.code, "visibility", l.visibility)
	return nil
}

func (h *Hub) joinLobby(c *Client, req *request) error {
	if c.lobby.Load() != nil {
		return errf(ErrAlreadyInLobby, "leave your current lobby first")
	}
	code := normalizeCode(req.Code)
	h.mu.Lock()
	l := h.games[c.gameID][code]
	h.mu.Unlock()
	if l == nil {
		return errf(ErrLobbyNotFound, "no lobby with code %q for this game", code)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case l.closed:
		return errf(ErrLobbyNotFound, "no lobby with code %q for this game", code)
	case l.locked:
		return errf(ErrLobbyLocked, "the lobby is locked")
	case len(l.members) >= l.maxPlayers:
		return errf(ErrLobbyFull, "the lobby is full (%d/%d)", len(l.members), l.maxPlayers)
	}
	l.members = append(l.members, c)
	c.lobby.Store(l)
	c.send(response{Type: TypeLobbyJoined, Rid: req.Rid, PeerID: c.id, Lobby: l.infoLocked(), Peers: l.peersLocked()})
	l.broadcastLocked(response{Type: TypePeerJoined, Peer: &PeerInfo{ID: c.id, Name: c.name}}, c, nil, nil)
	return nil
}

func (h *Hub) listLobbies(gameID string, req *request) ([]LobbyInfo, error) {
	want, err := parseFilter(req.Filter)
	if err != nil {
		return nil, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	limit = min(limit, maxListLimit)

	h.mu.Lock()
	candidates := make([]*Lobby, 0, len(h.games[gameID]))
	for _, l := range h.games[gameID] {
		candidates = append(candidates, l)
	}
	h.mu.Unlock()

	out := []LobbyInfo{}
	for _, l := range candidates {
		l.mu.Lock()
		ok := !l.closed && l.visibility == VisibilityPublic &&
			(req.IncludeFull || (!l.locked && len(l.members) < l.maxPlayers)) &&
			matchesFilter(l.metaDecoded, want)
		if ok {
			out = append(out, *l.infoLocked())
		}
		l.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (h *Hub) removeLobby(l *Lobby) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if codes := h.games[l.gameID]; codes[l.code] == l {
		delete(codes, l.code)
		h.lobbyCount--
		if len(codes) == 0 {
			delete(h.games, l.gameID)
		}
	}
	h.cfg.Logger.Info("lobby closed", "game_id", l.gameID, "code", l.code)
}
