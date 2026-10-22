# Game Relay Protocol Specification

**Protocol version:** 1  
**Audience:** game developers, and AI coding agents writing client code for them.

A running server also serves this document at `GET /spec.md`.

> **For AI agents:** This document is the full contract. Everything a client
> can send and everything the server can send is listed here. Field names are
> exact and case-sensitive. If you build a client, implement the
> [state machine](#9-client-state-machine), handle every
> [server message](#6-server--client-messages) (ignore unknown types and fields),
> and follow the [checklist](#13-implementation-checklist).

---

## 1. Overview

The relay connects players of the same game without either side needing a
public IP or port forwarding. Every client opens one WebSocket to the relay.
Clients join a **lobby**, and the relay forwards messages between lobby members.

- **Games are isolated by `game_id`.** A client declares its `game_id` once, in
  `hello`. It can only see, create, and join lobbies with that same `game_id`.
  Use a stable, unique ID per game, and add a version if incompatible builds
  must not meet (e.g. `com.example.spacegame:v3`).
- **Private lobbies** are joined with a **lobby code** that the relay generates
  (six characters, e.g. `K7QX2M`). They never appear in listings. Players share
  the code out of band (voice chat, text message, an invite screen).
- **Public lobbies** appear in the lobby list for their `game_id`. They can
  also be joined by code.
- The relay **does not interpret game data.** It forwards `data` payloads and
  binary frames unchanged. Game logic and authority belong to your clients,
  typically to the lobby **host**.

## 2. Connecting

| Item | Value |
|---|---|
| WebSocket URL | `ws://<host>:<port>/ws`, or `wss://` when the server runs with TLS |
| Subprotocol | none |
| Text frames | UTF-8 JSON objects, one message per frame |
| Binary frames | optional fast path, see [§7](#7-binary-frames) |
| Max message size | from `welcome.limits.max_message_bytes` (default 65536). Larger frames close the connection with WebSocket close code **1009** |
| Keepalive | the server sends WebSocket pings every 30 s. Standard WebSocket libraries answer them automatically. |

The **first** message on a new connection must be `hello`, and it must arrive
within 10 seconds. Any other first message gets an `error` with code
`hello_required`, and the server closes the connection (close code 1008).

One connection is one player, and a player can be in **at most one lobby at a
time**. When a connection drops, the player leaves their lobby. There is no
session resumption: to come back, reconnect, send `hello`, and join again. The
player gets a new `peer_id`.

## 3. Message envelope

Every JSON message has a `type`. Client messages may also include `rid`.

```json
{ "type": "join_lobby", "rid": 42, "code": "K7QX2M" }
```

- **`type`** (string, required): the message type.
- **`rid`** (string or number, optional): a request ID. The server copies it
  into the reply that answers this request.

**Reply rule:** every client request that includes a `rid` gets **exactly one**
reply carrying that `rid`. The reply is either the success message listed for
the request, or an `error`. The one exception is `send`, which gets a reply
only when it fails. Events caused by other players never carry a `rid`.

Clients should ignore unknown message types and unknown fields. Future
versions may add both.

## 4. Identifiers

| Name | Type | Notes |
|---|---|---|
| `game_id` | string | 1-64 chars from `A-Z a-z 0-9 . _ : -`. Case-sensitive. |
| `peer_id` | uint32 | Assigned by the server in `welcome`, unique per connection, never `0`. |
| lobby `code` | string | 6 chars from `ABCDEFGHJKLMNPQRSTUVWXYZ23456789` (no `0`, `O`, `1`, `I`). Unique within a `game_id`. On join, the server ignores case, spaces, and `-`, so `k7q-x2m` works. |

## 5. Client → server messages

### 5.1 `hello`

Must be the first message.

| Field | Type | Required | Notes |
|---|---|---|---|
| `game_id` | string | yes | See §4. |
| `name` | string | no | Display name, max 32 chars, no control chars. Defaults to `"Player <peer_id>"`. |

Success reply: `welcome`.
Errors: `hello_required`, `invalid_game_id`, `game_not_allowed`, `bad_request`.
A failed `hello` closes the connection.

```json
{ "type": "hello", "rid": 1, "game_id": "com.example.spacegame:v3", "name": "Alice" }
```

### 5.2 `create_lobby`

Creates a lobby and joins it as **host**.

| Field | Type | Default | Notes |
|---|---|---|---|
| `visibility` | `"private"` \| `"public"` | `"private"` | Private lobbies are joinable by code only. |
| `name` | string | `"<player name>'s lobby"` | Max 64 chars. Shown in listings. |
| `max_players` | int | `8` (server-configurable) | 1 to `welcome.limits.max_players`. Includes the host. |
| `metadata` | object | `{}` | Free-form JSON object, max 4096 bytes compacted. Shown in listings and filterable. Good for map, mode, region, build version. |
| `close_on_host_leave` | bool | `false` | If true, the lobby closes when the host leaves. If false, host passes to the longest-present member. |

Success reply: `lobby_joined`. The lobby `code` is in `lobby.code`.
Errors: `already_in_lobby`, `bad_request`, `server_full`.

```json
{ "type": "create_lobby", "rid": 2, "visibility": "public", "name": "Casual CTF",
  "max_players": 6, "metadata": { "mode": "ctf", "map": "dust", "region": "eu" } }
```

### 5.3 `join_lobby`

| Field | Type | Required |
|---|---|---|
| `code` | string | yes |

Success reply: `lobby_joined`. All existing members receive `peer_joined`.
Errors: `already_in_lobby`, `lobby_not_found`, `lobby_full`, `lobby_locked`.
If the code belongs to another `game_id`, the error is `lobby_not_found`.

### 5.4 `list_lobbies`

Lists **public** lobbies for the caller's `game_id`. You do not need to be in a
lobby to call it.

| Field | Type | Default | Notes |
|---|---|---|---|
| `filter` | object | none | Only lobbies whose `metadata` has every listed key with an equal JSON value. Example: `{"mode":"ctf","region":"eu"}`. |
| `include_full` | bool | `false` | By default, full and locked lobbies are left out. Set to true to include them. |
| `limit` | int | `50` | Max `200`. |

Success reply: `lobby_list`, newest lobby first.

The same list is available over plain HTTP with no WebSocket. See [§8](#8-http-endpoints).

### 5.5 `leave_lobby`

No fields. Success reply: `lobby_left` with `reason: "left"`. Remaining members
receive `peer_left`, and if you were host they also receive `host_changed`, or
`lobby_left` with reason `host_left` when `close_on_host_leave` is set.
Errors: `not_in_lobby`.

### 5.6 `update_lobby` (host only)

Changes lobby settings. Every field is optional. Omitted fields stay as they
are. If any field is invalid, nothing changes.

| Field | Type | Notes |
|---|---|---|
| `name` | string | |
| `visibility` | `"private"` \| `"public"` | Switch between listed and code-only. |
| `max_players` | int | Must be at least the current player count. |
| `locked` | bool | A locked lobby rejects joins (`lobby_locked`) and is hidden from default listings. Typical use: lock when the match starts. |
| `metadata` | object | **Replaces** the whole metadata object. |

Success reply: `lobby_updated`, which is also sent to every member.
Errors: `not_in_lobby`, `not_host`, `bad_request`.

### 5.7 `kick` (host only)

| Field | Type | Required |
|---|---|---|
| `peer_id` | uint32 | yes |

The kicked player receives `lobby_left` with `reason: "kicked"`. Everyone else
receives `peer_left` with `reason: "kicked"`. The host's copy of `peer_left` is
its reply. Kicked players can rejoin with the code unless the lobby is locked.
Errors: `not_in_lobby`, `not_host`, `peer_not_found`, `bad_request` (the host
tried to kick itself).

### 5.8 `transfer_host` (host only)

| Field | Type | Required |
|---|---|---|
| `peer_id` | uint32 | yes |

All members receive `host_changed`, and the requester's copy is its reply.
Errors: `not_in_lobby`, `not_host`, `peer_not_found`.

### 5.9 `send`

Relays game data to other members of your lobby.

| Field | Type | Required | Notes |
|---|---|---|---|
| `data` | any JSON value | yes | Delivered unchanged. |
| `to` | uint32 | no | Target `peer_id`. Omit it or use `0` to send to **every other member**. The sender never gets its own broadcast. |

There is no reply on success. Recipients get `message`.
Errors: `not_in_lobby`, `peer_not_found`, `bad_request` (missing `data`).

```json
{ "type": "send", "data": { "op": "move", "x": 10, "y": 4 } }
{ "type": "send", "to": 7, "data": { "op": "state_sync", "tick": 1200, "state": "..." } }
```

### 5.10 `ping`

No fields. Reply: `pong` with `time`, the server clock in Unix milliseconds.
Use it to measure round-trip time. You do not need it for keepalive.

## 6. Server → client messages

### `welcome`
Reply to `hello`.
```json
{ "type": "welcome", "rid": 1, "peer_id": 12,
  "limits": { "max_message_bytes": 65536, "max_players": 64, "max_metadata_bytes": 4096,
              "rate_limit": 120, "rate_burst": 240 } }
```
`rate_limit` is in messages per second. `0` means no limit.

### `lobby_joined`
Reply to `create_lobby` or `join_lobby`.
```json
{ "type": "lobby_joined", "rid": 2, "peer_id": 12,
  "lobby": { /* Lobby object */ },
  "peers": [ { "id": 12, "name": "Alice" }, { "id": 15, "name": "Bob" } ] }
```
`peers` lists every member, including you, in join order. `peer_id` is your
own ID.

### `lobby_list`
Reply to `list_lobbies`. `{ "type": "lobby_list", "rid": 3, "lobbies": [ /* Lobby objects */ ] }`

### `lobby_updated`
`{ "type": "lobby_updated", "lobby": { /* Lobby object */ } }`. Sent to all
members after `update_lobby`.

### `peer_joined`
`{ "type": "peer_joined", "peer": { "id": 15, "name": "Bob" } }`

### `peer_left`
`{ "type": "peer_left", "peer_id": 15, "reason": "left" | "disconnected" | "kicked" }`

### `host_changed`
`{ "type": "host_changed", "peer_id": 15 }`. `peer_id` is the new host. This
is sent after the old host's `peer_left` when the host leaves, and after
`transfer_host`.

### `lobby_left`
`{ "type": "lobby_left", "reason": "left" | "kicked" | "host_left" }`. You are
no longer in a lobby. With `left`, it is the reply to your `leave_lobby`. With
`kicked` or `host_left`, it happens without a request.

### `message`
`{ "type": "message", "from": 12, "data": <any JSON> }`. Relayed from `send`.

### `pong`
`{ "type": "pong", "rid": 4, "time": 1761161234567 }`

### `error`
```json
{ "type": "error", "rid": 2, "error": "lobby_full", "message": "the lobby is full (8/8)" }
```
`error` is a stable, machine-readable code. Branch on it. `message` is
human-readable and may change. `rid` is present when the request that failed
had one. Errors do not close the connection, except during `hello`.

### Lobby object
```json
{
  "code": "K7QX2M",
  "game_id": "com.example.spacegame:v3",
  "name": "Casual CTF",
  "visibility": "public",
  "host_id": 12,
  "player_count": 3,
  "max_players": 6,
  "locked": false,
  "close_on_host_leave": false,
  "metadata": { "mode": "ctf", "map": "dust", "region": "eu" },
  "created_at": "2026-10-22T19:30:00Z"
}
```

### Error codes

| Code | Meaning |
|---|---|
| `bad_request` | Malformed JSON, a missing or invalid field, or `hello` sent twice. |
| `unknown_type` | Unrecognised `type`. |
| `hello_required` | The first message was not `hello`. The connection is closed. |
| `invalid_game_id` | `game_id` does not match §4. The connection is closed. |
| `game_not_allowed` | The server only accepts certain game IDs. The connection is closed. |
| `already_in_lobby` | `create_lobby` or `join_lobby` while already in a lobby. Leave first. |
| `not_in_lobby` | A lobby operation or `send` while not in a lobby. |
| `lobby_not_found` | No lobby with that code for your `game_id`, or it closed. |
| `lobby_full` | `player_count` has reached `max_players`. |
| `lobby_locked` | The host locked the lobby. |
| `not_host` | Host-only operation. |
| `peer_not_found` | The target `peer_id` is not in your lobby. |
| `rate_limited` | You exceeded the message rate, and messages are being **dropped**. Sent at most once per second. |
| `server_full` | The server's lobby limit was reached. Retry later. |

## 7. Binary frames

For high-frequency data such as positions or inputs, binary WebSocket frames
avoid JSON overhead. They are relayed only within your lobby and are subject to
the same size and rate limits.

```
offset 0      4                               N
       +------+-------------------------------+
       | peer | payload (opaque bytes)        |
       +------+-------------------------------+
```

- **peer** is a uint32, **big-endian**.
  - **Client → server:** the target `peer_id`, or `0` for every other member.
  - **Server → client:** the **sender's** `peer_id`. The server overwrites the field.
- Frames shorter than 4 bytes are rejected with `bad_request`.
- JSON `send` and binary frames can be mixed freely.

## 8. HTTP endpoints

| Method and path | Description |
|---|---|
| `GET /ws` | WebSocket endpoint. |
| `GET /v1/lobbies?game_id=<id>[&filter=<url-encoded JSON object>][&include_full=true][&limit=N]` | Public lobby list: `{"lobbies":[...]}`, the same as `list_lobbies`. CORS is open. Errors return 400 with `{"error":"<code>","message":"..."}`. |
| `GET /v1/stats` | `{"connections":N,"lobbies":N,"games":N}` |
| `GET /healthz` | `ok` |
| `GET /spec.md` | This document. |

A lobby browser can poll `/v1/lobbies` before the player commits to
connecting. A refresh every 2-5 seconds is reasonable.

## 9. Client state machine

```
 CONNECTING ──open──▶ send hello ──welcome──▶ IDLE ◀───────────────────────┐
                           │                   │  list_lobbies (any time) │
                        error → closed         │                          │
                                     create_lobby / join_lobby            │
                                               │                          │
                                         lobby_joined                     │
                                               ▼                          │
                                            IN_LOBBY ── lobby_left ───────┘
                                     (send, update_lobby, kick, …)

 Any state ──socket closed──▶ DISCONNECTED (lobby membership is gone)
```

Track the following in the client:

- `myPeerId` from `welcome`.
- `lobby` (a Lobby object) and `peers` (a map from id to name) from
  `lobby_joined`. Then apply updates:
  `peer_joined` adds a peer, `peer_left` removes one, `host_changed` sets
  `lobby.host_id`, `lobby_updated` replaces `lobby`, and `lobby_left` clears
  everything.
- `isHost = (lobby.host_id == myPeerId)`. Check it again after every
  `host_changed`. Host migration can make **you** the host at any time.

## 10. Typical flows

**Private game with friends**
1. Host: `hello`, then `create_lobby {visibility:"private"}`. Show `lobby.code` on screen.
2. Friend: `hello`, then `join_lobby {code}`. The host receives `peer_joined`.
3. When ready, the host sends `update_lobby {locked:true}` and a game-defined
   start message with `send`.

**Public matchmaking**
1. Host: `create_lobby {visibility:"public", metadata:{mode, region}}`.
2. Other players: `list_lobbies {filter:{mode}}` (or `GET /v1/lobbies`), show
   the list, then `join_lobby {code}` with the chosen lobby's `code`.
3. If a join fails with `lobby_full` or `lobby_not_found`, refresh the list.
   Lobbies change quickly.

**Quick match**: list with a filter, join the first result, and if the list is
empty or every join fails, create a public lobby with the same metadata.

## 11. Network topologies on top of the relay

The relay is a dumb pipe, so choose a topology in your game code:

- **Host-authoritative (recommended):** clients `send` inputs `to: hostId`, and
  the host simulates and broadcasts state (`to` omitted). On `host_changed`,
  the new host takes over from its last known state, or sets
  `close_on_host_leave: true` if the game cannot migrate.
- **Peer-to-peer broadcast:** every client broadcasts its own state. This is
  simple and works well for casual or turn-based games.

The relay does not validate or authenticate game data. Treat every incoming
payload as untrusted, and validate it in the receiving client or host.

## 12. Delivery guarantees and limits

- **Ordered and reliable per sender→receiver pair**, as long as both stay
  connected (one WebSocket over TCP). There is no ordering guarantee across
  different senders.
- **No persistence or replay.** Messages for a peer that has left are dropped.
  Players who join late see only messages sent after they joined, so the host
  should send them a state snapshot on `peer_joined`.
- **Slow consumers are disconnected.** If a client stops reading and about 256
  messages queue up for it, the server drops that connection.
- **Rate limiting** is a token bucket of `rate_limit` messages per second with
  `rate_burst` burst. Messages over the limit are dropped, not queued. A
  60-tick game should batch each tick into one message.
- Lobbies close automatically when their last member leaves. Codes can be
  reused afterwards.

## 13. Implementation checklist

- [ ] Open a WebSocket to `/ws`, and send `hello` with your `game_id` right away.
- [ ] Store `peer_id` and `limits` from `welcome`.
- [ ] Include a `rid` on requests that need a reply, and match replies by `rid`.
- [ ] Handle `error` and branch on the `error` code, not on `message`.
- [ ] Keep lobby and peer state from `lobby_joined`, `peer_joined`,
      `peer_left`, `host_changed`, `lobby_updated`, and `lobby_left`.
- [ ] Re-evaluate `isHost` after every `host_changed`.
- [ ] Treat a socket close as leaving the lobby. Reconnect with backoff, and
      show the player what happened.
- [ ] Keep messages under `max_message_bytes` and the rate under `rate_limit`.
- [ ] Ignore unknown message types and fields.
- [ ] Never trust relayed `data`. Validate it.

## 14. Reference client (JavaScript, browser or Node 22+)

```js
class RelayClient {
  constructor(url, gameId, name) {
    this.url = url; this.gameId = gameId; this.name = name;
    this.pending = new Map(); this.nextRid = 1;
    this.peerId = null; this.lobby = null; this.peers = new Map();
    this.onMessage = (from, data) => {};      // game data
    this.onBinary = (from, bytes) => {};      // binary frames
    this.onLobbyChange = () => {};            // lobby, peers, or host changed
  }

  connect() {
    return new Promise((resolve, reject) => {
      this.ws = new WebSocket(this.url);
      this.ws.binaryType = "arraybuffer";
      this.ws.onopen = () =>
        this.request({ type: "hello", game_id: this.gameId, name: this.name })
          .then(w => { this.peerId = w.peer_id; this.limits = w.limits; resolve(w); }, reject);
      this.ws.onmessage = ev => this.#onFrame(ev.data);
      this.ws.onclose = () => { this.lobby = null; this.peers.clear(); this.onLobbyChange(); };
      this.ws.onerror = reject;
    });
  }

  // Sends a request and resolves with the reply that carries the same rid.
  request(msg) {
    const rid = this.nextRid++;
    this.ws.send(JSON.stringify({ ...msg, rid }));
    return new Promise((resolve, reject) => this.pending.set(rid, { resolve, reject }));
  }

  createLobby(opts = {}) { return this.request({ type: "create_lobby", ...opts }); }
  joinLobby(code)         { return this.request({ type: "join_lobby", code }); }
  listLobbies(opts = {})  { return this.request({ type: "list_lobbies", ...opts }).then(r => r.lobbies); }
  leaveLobby()            { return this.request({ type: "leave_lobby" }); }
  send(data, to = 0)      { this.ws.send(JSON.stringify({ type: "send", to, data })); }
  sendBinary(bytes, to = 0) {
    const buf = new Uint8Array(4 + bytes.length);
    new DataView(buf.buffer).setUint32(0, to);            // big-endian
    buf.set(bytes, 4);
    this.ws.send(buf);
  }
  get isHost() { return this.lobby?.host_id === this.peerId; }

  #onFrame(raw) {
    if (raw instanceof ArrayBuffer) {
      const from = new DataView(raw).getUint32(0);
      return this.onBinary(from, new Uint8Array(raw, 4));
    }
    const m = JSON.parse(raw);
    switch (m.type) {
      case "lobby_joined":
        this.lobby = m.lobby;
        this.peers = new Map(m.peers.map(p => [p.id, p.name])); break;
      case "lobby_updated": this.lobby = m.lobby; break;
      case "peer_joined":   this.peers.set(m.peer.id, m.peer.name); this.lobby.player_count++; break;
      case "peer_left":     this.peers.delete(m.peer_id); this.lobby.player_count--; break;
      case "host_changed":  this.lobby.host_id = m.peer_id; break;
      case "lobby_left":    this.lobby = null; this.peers.clear(); break;
      case "message":       this.onMessage(m.from, m.data); break;
    }
    if (["lobby_joined", "lobby_updated", "peer_joined", "peer_left", "host_changed", "lobby_left"].includes(m.type)) {
      this.onLobbyChange(m);
    }
    if (m.rid !== undefined && this.pending.has(m.rid)) {
      const p = this.pending.get(m.rid); this.pending.delete(m.rid);
      m.type === "error" ? p.reject(Object.assign(new Error(m.message), { code: m.error })) : p.resolve(m);
    }
  }
}

// Usage
const relay = new RelayClient("wss://relay.example.com/ws", "com.example.spacegame:v3", "Alice");
await relay.connect();
const { lobby } = await relay.createLobby({ visibility: "private", max_players: 4 });
console.log("Share this code:", lobby.code);
relay.onMessage = (from, data) => console.log(from, data);
relay.send({ op: "hello-world" });
```

Clients in other engines (Unity/C#, Godot/GDScript, Unreal, Rust, and so on)
follow the same shape: one WebSocket, a JSON encoder, a `rid → callback` map,
and a switch on `type`.
