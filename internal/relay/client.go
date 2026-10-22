package relay

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

type frame struct {
	binary bool
	data   []byte
}

// Client is one WebSocket connection. Requests from a client are handled on
// its own read goroutine; other goroutines reach it only through enqueue and
// the lobby pointer.
type Client struct {
	hub    *Hub
	conn   *websocket.Conn
	id     uint32
	gameID string
	name   string
	lobby  atomic.Pointer[Lobby]
	out    chan frame
	cancel context.CancelFunc

	// Rate limiter state, touched only by the read goroutine.
	tokens      float64
	lastRefill  time.Time
	lastRateErr time.Time
}

// enqueue queues f for delivery without blocking. A client whose queue is
// full is too slow to keep up and is disconnected.
func (c *Client) enqueue(f frame) {
	select {
	case c.out <- f:
	default:
		c.cancel()
	}
}

func (c *Client) send(v any) { c.enqueue(frame{data: mustJSON(v)}) }

func (c *Client) sendError(rid json.RawMessage, err error) {
	var e *Error
	if !errors.As(err, &e) {
		e = errf(ErrBadRequest, "%v", err)
	}
	c.send(response{Type: TypeError, Rid: rid, Error: e.Code, Message: e.Message})
}

func (c *Client) run(ctx context.Context) {
	ctx, c.cancel = context.WithCancel(ctx)
	defer c.cancel()
	if err := c.handshake(ctx); err != nil {
		return
	}
	go c.writeLoop(ctx)
	go c.pingLoop(ctx)
	c.readLoop(ctx)
}

// handshake waits for hello and answers it synchronously, before the write
// loop starts, so a rejection is written before the connection closes.
func (c *Client) handshake(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.hub.cfg.HelloTimeout)
	defer cancel()
	typ, data, err := c.conn.Read(ctx)
	if err != nil {
		return err
	}
	var req request
	if typ != websocket.MessageText || json.Unmarshal(data, &req) != nil || req.Type != TypeHello {
		return c.reject(ctx, req.Rid, errf(ErrHelloRequired, `the first message must be {"type":"hello","game_id":"..."}`))
	}
	if e := c.hub.checkGameID(req.GameID); e != nil {
		return c.reject(ctx, req.Rid, e)
	}
	c.name = fmt.Sprintf("Player %d", c.id)
	if req.Name != nil {
		name, err := cleanText(*req.Name, "name", maxPeerName)
		if err != nil {
			return c.reject(ctx, req.Rid, err.(*Error))
		}
		if name != "" {
			c.name = name
		}
	}
	c.gameID = req.GameID
	msg := mustJSON(response{Type: TypeWelcome, Rid: req.Rid, PeerID: c.id, Limits: c.hub.limits()})
	return c.conn.Write(ctx, websocket.MessageText, msg)
}

func (c *Client) reject(ctx context.Context, rid json.RawMessage, e *Error) error {
	msg := mustJSON(response{Type: TypeError, Rid: rid, Error: e.Code, Message: e.Message})
	c.conn.Write(ctx, websocket.MessageText, msg)
	c.conn.Close(websocket.StatusPolicyViolation, e.Code)
	return e
}

func (c *Client) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case f := <-c.out:
			typ := websocket.MessageText
			if f.binary {
				typ = websocket.MessageBinary
			}
			wctx, cancel := context.WithTimeout(ctx, c.hub.cfg.WriteTimeout)
			err := c.conn.Write(wctx, typ, f.data)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		}
	}
}

func (c *Client) pingLoop(ctx context.Context) {
	t := time.NewTicker(c.hub.cfg.PingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, c.hub.cfg.PingInterval)
			err := c.conn.Ping(pctx)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		}
	}
}

func (c *Client) readLoop(ctx context.Context) {
	c.tokens, c.lastRefill = float64(c.hub.cfg.RateBurst), time.Now()
	for {
		typ, data, err := c.conn.Read(ctx)
		if err != nil {
			return
		}
		if !c.allow() {
			if time.Since(c.lastRateErr) >= time.Second {
				c.lastRateErr = time.Now()
				c.sendError(nil, errf(ErrRateLimited, "messages are being dropped: over %g messages/second", c.hub.cfg.RateLimit))
			}
			continue
		}
		if typ == websocket.MessageBinary {
			if err := c.handleBinary(data); err != nil {
				c.sendError(nil, err)
			}
			continue
		}
		var req request
		if err := json.Unmarshal(data, &req); err != nil {
			c.sendError(nil, errf(ErrBadRequest, "invalid JSON: %v", err))
			continue
		}
		if err := c.dispatch(&req); err != nil {
			c.sendError(req.Rid, err)
		}
	}
}

// allow applies the token-bucket rate limit.
func (c *Client) allow() bool {
	cfg := c.hub.cfg
	if cfg.RateLimit < 0 {
		return true
	}
	now := time.Now()
	c.tokens = min(float64(cfg.RateBurst), c.tokens+now.Sub(c.lastRefill).Seconds()*cfg.RateLimit)
	c.lastRefill = now
	if c.tokens < 1 {
		return false
	}
	c.tokens--
	return true
}

func (c *Client) currentLobby() (*Lobby, error) {
	l := c.lobby.Load()
	if l == nil {
		return nil, errf(ErrNotInLobby, "you are not in a lobby")
	}
	return l, nil
}

func (c *Client) dispatch(req *request) error {
	switch req.Type {
	case TypePing:
		c.send(response{Type: TypePong, Rid: req.Rid, Time: time.Now().UnixMilli()})
		return nil
	case TypeHello:
		return errf(ErrBadRequest, "hello was already received")
	case TypeCreateLobby:
		return c.hub.createLobby(c, req)
	case TypeJoinLobby:
		return c.hub.joinLobby(c, req)
	case TypeListLobbies:
		lobbies, err := c.hub.listLobbies(c.gameID, req)
		if err != nil {
			return err
		}
		c.send(lobbyList{Type: TypeLobbyList, Rid: req.Rid, Lobbies: lobbies})
		return nil
	case TypeLeaveLobby, TypeUpdateLobby, TypeKick, TypeTransferHost, TypeSend:
		l, err := c.currentLobby()
		if err != nil {
			return err
		}
		return c.dispatchLobby(l, req)
	}
	return errf(ErrUnknownType, "unknown message type %q", req.Type)
}

func (c *Client) dispatchLobby(l *Lobby, req *request) error {
	switch req.Type {
	case TypeLeaveLobby:
		return l.leave(c, ReasonLeft, req.Rid)
	case TypeUpdateLobby:
		return l.update(c, req)
	case TypeKick:
		return l.kick(c, req)
	case TypeTransferHost:
		return l.transferHost(c, req)
	case TypeSend:
		if req.Data == nil {
			return errf(ErrBadRequest, "send requires a data field")
		}
		msg := mustJSON(response{Type: TypeMessage, From: c.id, Data: req.Data})
		return l.relay(c, req.To, frame{data: msg})
	}
	return errf(ErrUnknownType, "unknown message type %q", req.Type)
}

// handleBinary relays a binary frame. The first 4 bytes (big-endian uint32)
// name the target peer, 0 meaning everyone else; the server overwrites them
// with the sender's peer ID before delivery.
func (c *Client) handleBinary(data []byte) error {
	l, err := c.currentLobby()
	if err != nil {
		return err
	}
	if len(data) < 4 {
		return errf(ErrBadRequest, "binary frames need a 4-byte peer ID header")
	}
	to := binary.BigEndian.Uint32(data)
	binary.BigEndian.PutUint32(data, c.id)
	return l.relay(c, to, frame{binary: true, data: data})
}
