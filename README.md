# game-relay

A WebSocket relay server for multiplayer game lobbies. Players connect to the
relay instead of to each other, so nobody needs port forwarding or a public IP.

- **Games are isolated by `game_id`.** Many games can share one server.
- **Private lobbies** are joined with a relay-generated code, such as `K7QX2M`.
- **Public lobbies** can be browsed and filtered by metadata, over the
  WebSocket or plain HTTP.
- Host migration, kick, lock, and transfer host.
- JSON messages, plus a compact binary frame format for high-rate data.
- Per-connection rate limiting, size limits, and slow-client protection.

**Integrating a game?** Read **[docs/SPEC.md](docs/SPEC.md)**. It is written
for developers and AI coding agents, and it includes a reference JavaScript
client. A running server also serves it at `/spec.md`.

## Run

```sh
go run ./cmd/relay                 # listens on :8080 (or :$PORT)
# or
docker build -t game-relay . && docker run -p 8080:8080 game-relay
# or, with the sample compose file
docker compose up -d
```

Clients connect to `ws://localhost:8080/ws`.

### Publish the image

```sh
docker login                                   # once, for your registry
make docker-push                               # pushes <git describe> and latest
make docker-push VERSION=1.2.0                 # pushes 1.2.0 and latest
make docker-push IMAGE=ghcr.io/you/game-relay VERSION=1.2.0
```

`docker-push` uses `docker buildx` to build `linux/amd64` and `linux/arm64`.
Set `PLATFORMS` to change that. `make docker-build` builds for the local
machine only.

### Flags

| Flag | Default | |
|---|---|---|
| `-addr` | `:8080` or `:$PORT` | Listen address |
| `-tls-cert`, `-tls-key` | | Serve `wss://` directly. You can also put a TLS proxy in front. |
| `-max-players` | `64` | Largest `max_players` a lobby may request |
| `-default-max-players` | `8` | Used when `create_lobby` omits `max_players` |
| `-max-lobbies` | `10000` | Total open lobbies |
| `-max-message-bytes` | `65536` | Largest accepted message |
| `-rate`, `-burst` | `120`, `240` | Messages per second per connection (`-rate -1` disables) |
| `-allowed-games` | any | Comma-separated `game_id` allow-list |
| `-origins` | `*` | Allowed browser origins, for example `mygame.com,*.mygame.com` |

## Layout

```
cmd/relay/          entry point and flags
internal/relay/     protocol, hub, lobbies, connections, and tests
docs/SPEC.md        protocol specification (embedded into the binary)
```

## Test

```sh
go test -race ./...
```
