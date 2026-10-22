package relay

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type testClient struct {
	t    *testing.T
	conn *websocket.Conn
	id   uint32
}

// msg is a loosely-typed decoded server message.
type msg map[string]any

func (m msg) str(k string) string { s, _ := m[k].(string); return s }
func (m msg) num(k string) uint32 { f, _ := m[k].(float64); return uint32(f) }
func (m msg) obj(k string) msg    { o, _ := m[k].(map[string]any); return o }

func newTestServer(t *testing.T, cfg Config) (*Server, string) {
	t.Helper()
	cfg.Logger = discardLogger()
	s := NewServer(cfg)
	ts := httptest.NewServer(s)
	t.Cleanup(func() { s.Close(); ts.Close() })
	return s, ts.URL
}

func dial(t *testing.T, url string) *testClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(url, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return &testClient{t: t, conn: conn}
}

func connect(t *testing.T, url, gameID, name string) *testClient {
	t.Helper()
	c := dial(t, url)
	c.send(msg{"type": "hello", "game_id": gameID, "name": name, "rid": 1})
	w := c.expect("welcome")
	if w.num("rid") != 1 {
		t.Fatalf("welcome rid = %v, want 1", w["rid"])
	}
	c.id = w.num("peer_id")
	return c
}

func (c *testClient) send(m msg) {
	c.t.Helper()
	if err := c.conn.Write(context.Background(), websocket.MessageText, mustJSON(m)); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

func (c *testClient) read() (websocket.MessageType, []byte) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	typ, data, err := c.conn.Read(ctx)
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	return typ, data
}

func (c *testClient) expect(typ string) msg {
	c.t.Helper()
	_, data := c.read()
	var m msg
	if err := json.Unmarshal(data, &m); err != nil {
		c.t.Fatalf("decode %s: %v", data, err)
	}
	if m.str("type") != typ {
		c.t.Fatalf("got %s, want type %q", data, typ)
	}
	return m
}

func (c *testClient) expectError(code string) msg {
	c.t.Helper()
	m := c.expect("error")
	if m.str("error") != code {
		c.t.Fatalf("error = %q (%s), want %q", m.str("error"), m.str("message"), code)
	}
	return m
}

func (c *testClient) create(opts msg) msg {
	c.t.Helper()
	m := msg{"type": "create_lobby"}
	for k, v := range opts {
		m[k] = v
	}
	c.send(m)
	return c.expect("lobby_joined").obj("lobby")
}

func (c *testClient) join(code string) msg {
	c.t.Helper()
	c.send(msg{"type": "join_lobby", "code": code})
	return c.expect("lobby_joined")
}

func TestPrivateLobbyByCode(t *testing.T) {
	_, url := newTestServer(t, Config{})
	host := connect(t, url, "chess", "alice")
	guest := connect(t, url, "chess", "bob")

	lobby := host.create(msg{"name": "Friends only"})
	code := lobby.str("code")
	if len(code) != codeLength || lobby.str("visibility") != "private" || lobby.num("host_id") != host.id {
		t.Fatalf("unexpected lobby: %v", lobby)
	}

	// Human-typed variations of the code work.
	joined := guest.join(strings.ToLower(code[:3]) + "-" + code[3:])
	if joined.num("peer_id") != guest.id || len(joined["peers"].([]any)) != 2 {
		t.Fatalf("unexpected lobby_joined: %v", joined)
	}
	pj := host.expect("peer_joined").obj("peer")
	if pj.num("id") != guest.id || pj.str("name") != "bob" {
		t.Fatalf("unexpected peer_joined: %v", pj)
	}

	// Private lobbies never appear in listings.
	host.send(msg{"type": "list_lobbies", "rid": "x"})
	if l := host.expect("lobby_list"); len(l["lobbies"].([]any)) != 0 || l.str("rid") != "x" {
		t.Fatalf("private lobby listed: %v", l)
	}
}

func TestGameIsolation(t *testing.T) {
	_, url := newTestServer(t, Config{})
	a := connect(t, url, "game-a", "a")
	b := connect(t, url, "game-b", "b")

	code := a.create(msg{"visibility": "public"}).str("code")
	b.send(msg{"type": "join_lobby", "code": code})
	b.expectError(ErrLobbyNotFound)
	b.send(msg{"type": "list_lobbies"})
	if l := b.expect("lobby_list"); len(l["lobbies"].([]any)) != 0 {
		t.Fatalf("other game's lobby listed: %v", l)
	}
}

func TestPublicListing(t *testing.T) {
	_, url := newTestServer(t, Config{})
	h1 := connect(t, url, "g", "h1")
	h2 := connect(t, url, "g", "h2")
	h3 := connect(t, url, "g", "h3")
	browser := connect(t, url, "g", "browser")

	h1.create(msg{"visibility": "public", "metadata": msg{"mode": "ctf", "map": "dust"}})
	h2.create(msg{"visibility": "public", "metadata": msg{"mode": "dm"}})
	h3.create(msg{"visibility": "public", "max_players": 1}) // already full

	browser.send(msg{"type": "list_lobbies"})
	if n := len(browser.expect("lobby_list")["lobbies"].([]any)); n != 2 {
		t.Fatalf("listed %d lobbies, want 2", n)
	}
	browser.send(msg{"type": "list_lobbies", "include_full": true})
	if n := len(browser.expect("lobby_list")["lobbies"].([]any)); n != 3 {
		t.Fatalf("listed %d lobbies with include_full, want 3", n)
	}
	browser.send(msg{"type": "list_lobbies", "filter": msg{"mode": "ctf"}})
	lobbies := browser.expect("lobby_list")["lobbies"].([]any)
	if len(lobbies) != 1 || msg(lobbies[0].(map[string]any)).obj("metadata").str("map") != "dust" {
		t.Fatalf("filtered listing: %v", lobbies)
	}

	// The HTTP endpoint returns the same data.
	resp, err := http.Get(url + `/v1/lobbies?game_id=g&filter={"mode":"dm"}`)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct{ Lobbies []LobbyInfo }
	json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Lobbies) != 1 || body.Lobbies[0].HostID != h2.id {
		t.Fatalf("HTTP listing: %+v", body)
	}

	// Locking removes a lobby from the default listing.
	h1.send(msg{"type": "update_lobby", "locked": true, "rid": 9})
	if u := h1.expect("lobby_updated"); !u.obj("lobby")["locked"].(bool) || u.num("rid") != 9 {
		t.Fatalf("lobby_updated: %v", u)
	}
	browser.send(msg{"type": "list_lobbies"})
	if n := len(browser.expect("lobby_list")["lobbies"].([]any)); n != 1 {
		t.Fatalf("listed %d lobbies after lock, want 1", n)
	}
}

func TestRelayJSONAndBinary(t *testing.T) {
	_, url := newTestServer(t, Config{})
	a := connect(t, url, "g", "a")
	b := connect(t, url, "g", "b")
	c := connect(t, url, "g", "c")
	code := a.create(nil).str("code")
	b.join(code)
	a.expect("peer_joined")
	c.join(code)
	a.expect("peer_joined")
	b.expect("peer_joined")

	// Broadcast reaches everyone but the sender.
	a.send(msg{"type": "send", "data": msg{"x": 1}})
	for _, p := range []*testClient{b, c} {
		m := p.expect("message")
		if m.num("from") != a.id || m.obj("data")["x"].(float64) != 1 {
			t.Fatalf("message: %v", m)
		}
	}

	// Direct message reaches only the target.
	b.send(msg{"type": "send", "to": c.id, "data": "hi"})
	if m := c.expect("message"); m.num("from") != b.id || m.str("data") != "hi" {
		t.Fatalf("direct message: %v", m)
	}

	// Binary: header is target on the way in, sender on the way out.
	frame := make([]byte, 4, 7)
	binary.BigEndian.PutUint32(frame, a.id)
	frame = append(frame, 1, 2, 3)
	if err := c.conn.Write(context.Background(), websocket.MessageBinary, frame); err != nil {
		t.Fatal(err)
	}
	typ, data := a.read()
	if typ != websocket.MessageBinary || binary.BigEndian.Uint32(data) != c.id || string(data[4:]) != "\x01\x02\x03" {
		t.Fatalf("binary relay: typ=%v data=%v", typ, data)
	}

	// b must not have received the direct or binary messages: the next thing
	// it sees is this ping reply.
	b.send(msg{"type": "ping"})
	b.expect("pong")

	a.send(msg{"type": "send", "to": 9999, "data": 1})
	a.expectError(ErrPeerNotFound)
}

func TestHostMigrationAndKick(t *testing.T) {
	_, url := newTestServer(t, Config{})
	a := connect(t, url, "g", "a")
	b := connect(t, url, "g", "b")
	c := connect(t, url, "g", "c")
	code := a.create(nil).str("code")
	b.join(code)
	a.expect("peer_joined")
	c.join(code)
	a.expect("peer_joined")
	b.expect("peer_joined")

	b.send(msg{"type": "kick", "peer_id": c.id})
	b.expectError(ErrNotHost)

	// Host leaves: oldest remaining member (b) becomes host.
	a.send(msg{"type": "leave_lobby", "rid": 5})
	if m := a.expect("lobby_left"); m.str("reason") != ReasonLeft || m.num("rid") != 5 {
		t.Fatalf("lobby_left: %v", m)
	}
	for _, p := range []*testClient{b, c} {
		if m := p.expect("peer_left"); m.num("peer_id") != a.id || m.str("reason") != ReasonLeft {
			t.Fatalf("peer_left: %v", m)
		}
		if m := p.expect("host_changed"); m.num("peer_id") != b.id {
			t.Fatalf("host_changed: %v", m)
		}
	}

	b.send(msg{"type": "kick", "peer_id": c.id, "rid": 7})
	if m := c.expect("lobby_left"); m.str("reason") != ReasonKicked {
		t.Fatalf("kicked lobby_left: %v", m)
	}
	if m := b.expect("peer_left"); m.num("peer_id") != c.id || m.num("rid") != 7 {
		t.Fatalf("kick confirmation: %v", m)
	}

	// The kicked client is free to create its own lobby.
	c.create(nil)
}

func TestCloseOnHostLeave(t *testing.T) {
	_, url := newTestServer(t, Config{})
	a := connect(t, url, "g", "a")
	b := connect(t, url, "g", "b")
	code := a.create(msg{"close_on_host_leave": true}).str("code")
	b.join(code)
	a.expect("peer_joined")

	a.conn.Close(websocket.StatusNormalClosure, "")
	if m := b.expect("lobby_left"); m.str("reason") != ReasonHostLeft {
		t.Fatalf("lobby_left: %v", m)
	}
	b.send(msg{"type": "join_lobby", "code": code})
	b.expectError(ErrLobbyNotFound)
}

func TestDisconnectNotifiesPeers(t *testing.T) {
	s, url := newTestServer(t, Config{})
	a := connect(t, url, "g", "a")
	b := connect(t, url, "g", "b")
	b.join(a.create(nil).str("code"))
	a.expect("peer_joined")

	b.conn.Close(websocket.StatusNormalClosure, "")
	if m := a.expect("peer_left"); m.num("peer_id") != b.id || m.str("reason") != ReasonDisconnected {
		t.Fatalf("peer_left: %v", m)
	}
	a.conn.Close(websocket.StatusNormalClosure, "")
	waitFor(t, func() bool { return s.Stats() == Stats{} })
}

func TestLobbyFullAndLocked(t *testing.T) {
	_, url := newTestServer(t, Config{})
	a := connect(t, url, "g", "a")
	b := connect(t, url, "g", "b")
	c := connect(t, url, "g", "c")
	code := a.create(msg{"max_players": 2}).str("code")
	b.join(code)
	c.send(msg{"type": "join_lobby", "code": code})
	c.expectError(ErrLobbyFull)

	a.expect("peer_joined")
	a.send(msg{"type": "update_lobby", "max_players": 3, "locked": true})
	a.expect("lobby_updated")
	c.send(msg{"type": "join_lobby", "code": code})
	c.expectError(ErrLobbyLocked)
}

func TestValidation(t *testing.T) {
	_, url := newTestServer(t, Config{AllowedGames: []string{"ok"}})

	c := dial(t, url)
	c.send(msg{"type": "create_lobby"})
	c.expectError(ErrHelloRequired)

	c = dial(t, url)
	c.send(msg{"type": "hello", "game_id": "has spaces"})
	c.expectError(ErrInvalidGameID)

	c = dial(t, url)
	c.send(msg{"type": "hello", "game_id": "nope"})
	c.expectError(ErrGameNotAllowed)

	c = connect(t, url, "ok", "")
	c.send(msg{"type": "send", "data": 1})
	c.expectError(ErrNotInLobby)
	c.send(msg{"type": "dance"})
	c.expectError(ErrUnknownType)
	c.send(msg{"type": "create_lobby", "metadata": []int{1}})
	c.expectError(ErrBadRequest)
	c.send(msg{"type": "create_lobby", "visibility": "secret"})
	c.expectError(ErrBadRequest)
	c.send(msg{"type": "create_lobby", "max_players": 1000})
	c.expectError(ErrBadRequest)
	c.create(nil)
	c.send(msg{"type": "create_lobby"})
	c.expectError(ErrAlreadyInLobby)
}

func TestRateLimit(t *testing.T) {
	_, url := newTestServer(t, Config{RateLimit: 1, RateBurst: 3})
	c := connect(t, url, "g", "")
	for range 5 {
		c.send(msg{"type": "ping"})
	}
	for range 3 {
		c.expect("pong")
	}
	c.expectError(ErrRateLimited)
}

func TestSpecServed(t *testing.T) {
	_, url := newTestServer(t, Config{})
	resp, err := http.Get(url + "/spec.md")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "join_lobby") {
		t.Fatalf("spec: status %d, %d bytes", resp.StatusCode, len(body))
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }
