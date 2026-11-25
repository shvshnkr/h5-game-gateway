// Command gateway is the operator service: player auth/wallet, the
// seamless-wallet callbacks called by the game provider, the provider
// integration client and admin stats.
package main

import (
	"log/slog"
	"net/http"
	"os"

	"h5-game-gateway/internal/gateway"
)

func main() {
	cfg := gateway.LoadConfig()
	store, err := gateway.OpenStore(cfg.MySQLDSN)
	if err != nil {
		slog.Error("cannot open store", "error", err)
		os.Exit(1)
	}
	srv := gateway.NewServer(cfg, store)
	slog.Info("gateway starting", "addr", cfg.Addr, "db", cfg.DBName())
	if err := http.ListenAndServe(cfg.Addr, srv.Handler()); err != nil {
		slog.Error("gateway stopped", "error", err)
		os.Exit(1)
	}
}
