// Command relay runs the game lobby relay server.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/danotchuuy/game-relay/internal/relay"
)

func main() {
	addr := flag.String("addr", defaultAddr(), "listen address (defaults to :$PORT when PORT is set)")
	tlsCert := flag.String("tls-cert", "", "TLS certificate file (enables wss://)")
	tlsKey := flag.String("tls-key", "", "TLS key file")
	maxPlayers := flag.Int("max-players", 64, "largest max_players a lobby may request")
	defaultPlayers := flag.Int("default-max-players", 8, "max_players when a lobby does not set one")
	maxLobbies := flag.Int("max-lobbies", 10000, "maximum open lobbies across all games")
	maxMessage := flag.Int64("max-message-bytes", 64<<10, "largest accepted message in bytes")
	rate := flag.Float64("rate", 120, "messages per second per connection (-1 disables)")
	burst := flag.Int("burst", 240, "rate limiter burst size")
	allowedGames := flag.String("allowed-games", "", "comma-separated game IDs to accept (empty accepts any)")
	origins := flag.String("origins", "*", "comma-separated allowed browser origin patterns")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	srv := relay.NewServer(relay.Config{
		MaxMessageBytes:   *maxMessage,
		MaxPlayers:        *maxPlayers,
		DefaultMaxPlayers: *defaultPlayers,
		MaxLobbies:        *maxLobbies,
		RateLimit:         *rate,
		RateBurst:         *burst,
		AllowedGames:      splitList(*allowedGames),
		OriginPatterns:    splitList(*origins),
		Logger:            logger,
	})
	httpSrv := &http.Server{Addr: *addr, Handler: srv, ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdownCtx)
		srv.Close()
	}()

	logger.Info("relay listening", "addr", *addr, "tls", *tlsCert != "")
	var err error
	if *tlsCert != "" {
		err = httpSrv.ListenAndServeTLS(*tlsCert, *tlsKey)
	} else {
		err = httpSrv.ListenAndServe()
	}
	if !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server failed", "err", err)
		os.Exit(1)
	}
}

func defaultAddr() string {
	if port := os.Getenv("PORT"); port != "" {
		return ":" + port
	}
	return ":8080"
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
