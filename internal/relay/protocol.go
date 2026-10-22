package relay

import (
	"encoding/json"
	"fmt"
	"time"
)

// Message types sent by clients.
const (
	TypeHello        = "hello"
	TypePing         = "ping"
	TypeCreateLobby  = "create_lobby"
	TypeJoinLobby    = "join_lobby"
	TypeListLobbies  = "list_lobbies"
	TypeLeaveLobby   = "leave_lobby"
	TypeUpdateLobby  = "update_lobby"
	TypeKick         = "kick"
	TypeTransferHost = "transfer_host"
	TypeSend         = "send"
)

// Message types sent by the server.
const (
	TypeWelcome      = "welcome"
	TypePong         = "pong"
	TypeLobbyJoined  = "lobby_joined"
	TypeLobbyList    = "lobby_list"
	TypeLobbyLeft    = "lobby_left"
	TypeLobbyUpdated = "lobby_updated"
	TypePeerJoined   = "peer_joined"
	TypePeerLeft     = "peer_left"
	TypeHostChanged  = "host_changed"
	TypeMessage      = "message"
	TypeError        = "error"
)

// Lobby visibility values.
const (
	VisibilityPrivate = "private"
	VisibilityPublic  = "public"
)

// Reasons attached to peer_left and lobby_left.
const (
	ReasonLeft         = "left"
	ReasonDisconnected = "disconnected"
	ReasonKicked       = "kicked"
	ReasonHostLeft     = "host_left"
)

// Error codes returned in the "error" field of an error message.
const (
	ErrBadRequest     = "bad_request"
	ErrUnknownType    = "unknown_type"
	ErrHelloRequired  = "hello_required"
	ErrInvalidGameID  = "invalid_game_id"
	ErrGameNotAllowed = "game_not_allowed"
	ErrAlreadyInLobby = "already_in_lobby"
	ErrNotInLobby     = "not_in_lobby"
	ErrLobbyNotFound  = "lobby_not_found"
	ErrLobbyFull      = "lobby_full"
	ErrLobbyLocked    = "lobby_locked"
	ErrNotHost        = "not_host"
	ErrPeerNotFound   = "peer_not_found"
	ErrRateLimited    = "rate_limited"
	ErrServerFull     = "server_full"
)

// Error is a protocol-level error reported to the client.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func errf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// request is the union of every field a client message may carry.
type request struct {
	Type string          `json:"type"`
	Rid  json.RawMessage `json:"rid,omitempty"`

	// hello
	GameID string `json:"game_id"`

	// hello (player name), create_lobby / update_lobby (lobby name)
	Name *string `json:"name"`

	// create_lobby / update_lobby
	Visibility       *string         `json:"visibility"`
	MaxPlayers       *int            `json:"max_players"`
	Locked           *bool           `json:"locked"`
	Metadata         json.RawMessage `json:"metadata"`
	CloseOnHostLeave bool            `json:"close_on_host_leave"`

	// join_lobby
	Code string `json:"code"`

	// list_lobbies
	Filter      map[string]json.RawMessage `json:"filter"`
	Limit       int                        `json:"limit"`
	IncludeFull bool                       `json:"include_full"`

	// kick / transfer_host
	PeerID uint32 `json:"peer_id"`

	// send
	To   uint32          `json:"to"`
	Data json.RawMessage `json:"data"`
}

// response is the union of fields used by server messages other than
// lobby_list. Empty fields are omitted.
type response struct {
	Type    string          `json:"type"`
	Rid     json.RawMessage `json:"rid,omitempty"`
	Error   string          `json:"error,omitempty"`
	Message string          `json:"message,omitempty"`
	PeerID  uint32          `json:"peer_id,omitempty"`
	From    uint32          `json:"from,omitempty"`
	Reason  string          `json:"reason,omitempty"`
	Time    int64           `json:"time,omitempty"`
	Limits  *Limits         `json:"limits,omitempty"`
	Lobby   *LobbyInfo      `json:"lobby,omitempty"`
	Peer    *PeerInfo       `json:"peer,omitempty"`
	Peers   []PeerInfo      `json:"peers,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type lobbyList struct {
	Type    string          `json:"type"`
	Rid     json.RawMessage `json:"rid,omitempty"`
	Lobbies []LobbyInfo     `json:"lobbies"`
}

// Limits are the server limits reported in the welcome message.
type Limits struct {
	MaxMessageBytes  int64   `json:"max_message_bytes"`
	MaxPlayers       int     `json:"max_players"`
	MaxMetadataBytes int     `json:"max_metadata_bytes"`
	RateLimit        float64 `json:"rate_limit"`
	RateBurst        int     `json:"rate_burst"`
}

// LobbyInfo is the public description of a lobby.
type LobbyInfo struct {
	Code             string          `json:"code"`
	GameID           string          `json:"game_id"`
	Name             string          `json:"name"`
	Visibility       string          `json:"visibility"`
	HostID           uint32          `json:"host_id"`
	PlayerCount      int             `json:"player_count"`
	MaxPlayers       int             `json:"max_players"`
	Locked           bool            `json:"locked"`
	CloseOnHostLeave bool            `json:"close_on_host_leave"`
	Metadata         json.RawMessage `json:"metadata"`
	CreatedAt        time.Time       `json:"created_at"`
}

// PeerInfo describes one member of a lobby.
type PeerInfo struct {
	ID   uint32 `json:"id"`
	Name string `json:"name"`
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("relay: marshal %T: %v", v, err))
	}
	return b
}
