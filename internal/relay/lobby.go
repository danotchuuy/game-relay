package relay

import (
	"encoding/json"
	"slices"
	"sync"
	"time"
)

// Lobby is a group of clients of the same game that relay messages to each
// other. Fields below mu are guarded by it.
type Lobby struct {
	hub              *Hub
	code             string
	gameID           string
	createdAt        time.Time
	closeOnHostLeave bool

	mu          sync.Mutex
	closed      bool
	name        string
	visibility  string
	maxPlayers  int
	locked      bool
	metadata    json.RawMessage
	metaDecoded map[string]any
	members     []*Client // in join order; the oldest member inherits host
	host        *Client
}

// apply validates and applies the settable fields of a create_lobby or
// update_lobby request. Nothing changes if any field is invalid.
func (l *Lobby) apply(req *request) error {
	name, vis, maxPlayers := l.name, l.visibility, l.maxPlayers
	meta, decoded := l.metadata, l.metaDecoded
	var err error
	if req.Name != nil {
		if name, err = cleanText(*req.Name, "name", maxLobbyName); err != nil {
			return err
		}
	}
	if req.Visibility != nil {
		vis = *req.Visibility
		if vis != VisibilityPublic && vis != VisibilityPrivate {
			return errf(ErrBadRequest, `visibility must be "public" or "private"`)
		}
	}
	if req.MaxPlayers != nil {
		maxPlayers = *req.MaxPlayers
		if limit := l.hub.cfg.MaxPlayers; maxPlayers < 1 || maxPlayers > limit {
			return errf(ErrBadRequest, "max_players must be between 1 and %d", limit)
		}
		if maxPlayers < len(l.members) {
			return errf(ErrBadRequest, "max_players cannot be below the current player count (%d)", len(l.members))
		}
	}
	if req.Metadata != nil {
		if meta, decoded, err = parseMetadata(req.Metadata); err != nil {
			return err
		}
	}
	l.name, l.visibility, l.maxPlayers = name, vis, maxPlayers
	l.metadata, l.metaDecoded = meta, decoded
	if req.Locked != nil {
		l.locked = *req.Locked
	}
	return nil
}

func (l *Lobby) infoLocked() *LobbyInfo {
	return &LobbyInfo{
		Code:             l.code,
		GameID:           l.gameID,
		Name:             l.name,
		Visibility:       l.visibility,
		HostID:           l.host.id,
		PlayerCount:      len(l.members),
		MaxPlayers:       l.maxPlayers,
		Locked:           l.locked,
		CloseOnHostLeave: l.closeOnHostLeave,
		Metadata:         l.metadata,
		CreatedAt:        l.createdAt,
	}
}

func (l *Lobby) peersLocked() []PeerInfo {
	peers := make([]PeerInfo, len(l.members))
	for i, m := range l.members {
		peers[i] = PeerInfo{ID: m.id, Name: m.name}
	}
	return peers
}

func (l *Lobby) memberLocked(id uint32) *Client {
	for _, m := range l.members {
		if m.id == id {
			return m
		}
	}
	return nil
}

// broadcastLocked sends msg to every member except skip. If reqBy is a member,
// its copy carries rid so it can correlate the event with its request.
func (l *Lobby) broadcastLocked(msg response, skip, reqBy *Client, rid json.RawMessage) {
	plain := mustJSON(msg)
	withRid := plain
	if reqBy != nil && rid != nil {
		msg.Rid = rid
		withRid = mustJSON(msg)
	}
	for _, m := range l.members {
		switch m {
		case skip:
		case reqBy:
			m.enqueue(frame{data: withRid})
		default:
			m.enqueue(frame{data: plain})
		}
	}
}

// checkHostLocked returns an error unless c is a member and the host.
func (l *Lobby) checkHostLocked(c *Client) error {
	if !slices.Contains(l.members, c) {
		return errf(ErrNotInLobby, "you are not in a lobby")
	}
	if l.host != c {
		return errf(ErrNotHost, "only the host can do that")
	}
	return nil
}

// leave removes c from the lobby. For a voluntary leave, c receives
// lobby_left with rid; for a disconnect it receives nothing.
func (l *Lobby) leave(c *Client, reason string, rid json.RawMessage) error {
	l.mu.Lock()
	if !slices.Contains(l.members, c) {
		l.mu.Unlock()
		return errf(ErrNotInLobby, "you are not in a lobby")
	}
	if reason != ReasonDisconnected {
		c.send(response{Type: TypeLobbyLeft, Rid: rid, Reason: reason})
	}
	l.dropLocked(c, reason, nil, nil)
	closed := l.closed
	l.mu.Unlock()
	if closed {
		l.hub.removeLobby(l)
	}
	return nil
}

// dropLocked removes c from the member list, notifies the remaining members,
// and handles host migration or closing the lobby.
func (l *Lobby) dropLocked(c *Client, reason string, reqBy *Client, rid json.RawMessage) {
	l.members = slices.DeleteFunc(l.members, func(m *Client) bool { return m == c })
	c.lobby.CompareAndSwap(l, nil)

	if len(l.members) == 0 {
		l.closed = true
		return
	}
	if l.host == c && l.closeOnHostLeave {
		for _, m := range l.members {
			m.lobby.CompareAndSwap(l, nil)
			m.send(response{Type: TypeLobbyLeft, Reason: ReasonHostLeft})
		}
		l.members = nil
		l.closed = true
		return
	}
	l.broadcastLocked(response{Type: TypePeerLeft, PeerID: c.id, Reason: reason}, nil, reqBy, rid)
	if l.host == c {
		l.host = l.members[0]
		l.broadcastLocked(response{Type: TypeHostChanged, PeerID: l.host.id}, nil, nil, nil)
	}
}

func (l *Lobby) update(c *Client, req *request) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.checkHostLocked(c); err != nil {
		return err
	}
	if err := l.apply(req); err != nil {
		return err
	}
	l.broadcastLocked(response{Type: TypeLobbyUpdated, Lobby: l.infoLocked()}, nil, c, req.Rid)
	return nil
}

func (l *Lobby) kick(c *Client, req *request) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.checkHostLocked(c); err != nil {
		return err
	}
	if req.PeerID == c.id {
		return errf(ErrBadRequest, "the host cannot kick itself; use leave_lobby")
	}
	target := l.memberLocked(req.PeerID)
	if target == nil {
		return errf(ErrPeerNotFound, "peer %d is not in this lobby", req.PeerID)
	}
	target.send(response{Type: TypeLobbyLeft, Reason: ReasonKicked})
	l.dropLocked(target, ReasonKicked, c, req.Rid)
	return nil
}

func (l *Lobby) transferHost(c *Client, req *request) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.checkHostLocked(c); err != nil {
		return err
	}
	target := l.memberLocked(req.PeerID)
	if target == nil {
		return errf(ErrPeerNotFound, "peer %d is not in this lobby", req.PeerID)
	}
	l.host = target
	l.broadcastLocked(response{Type: TypeHostChanged, PeerID: target.id}, nil, c, req.Rid)
	return nil
}

// relay delivers f from sender to peer `to`, or to every other member when to
// is 0.
func (l *Lobby) relay(from *Client, to uint32, f frame) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !slices.Contains(l.members, from) {
		return errf(ErrNotInLobby, "you are not in a lobby")
	}
	if to == 0 {
		for _, m := range l.members {
			if m != from {
				m.enqueue(f)
			}
		}
		return nil
	}
	target := l.memberLocked(to)
	if target == nil {
		return errf(ErrPeerNotFound, "peer %d is not in this lobby", to)
	}
	target.enqueue(f)
	return nil
}
