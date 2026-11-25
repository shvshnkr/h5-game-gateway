// Command provider-mock is a fake PG-Soft-style game provider:
// signed operator API (provider-games, launch-game), provably fair
// in-memory sessions and two H5 mini-games. Educational demo only.
package main

import (
	"log/slog"
	"net/http"
	"os"

	"h5-game-gateway/internal/provider"
)

func main() {
	cfg := provider.LoadConfig()
	srv := provider.NewServer(cfg)
	slog.Info("provider-mock starting", "addr", cfg.Addr, "operator", cfg.OperatorCode, "callback", cfg.CallbackBase)
	if err := http.ListenAndServe(cfg.Addr, srv.Handler()); err != nil {
		slog.Error("provider-mock stopped", "error", err)
		os.Exit(1)
	}
}
