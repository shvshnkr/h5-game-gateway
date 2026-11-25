package gateway

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
)

// Server wires all handlers into one mux.
type Server struct {
	cfg      Config
	store    *Store
	auth     *authHandler
	wallet   *walletAPI
	seam     *seamless
	adm      *admin
	provider *providerClient
	rdb      *redis.Client
}

func NewServer(cfg Config, store *Store) *Server {
	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	return &Server{
		cfg:      cfg,
		store:    store,
		auth:     &authHandler{store: store, secret: []byte(cfg.JWTSecret)},
		wallet:   &walletAPI{store: store, cfg: cfg, secret: []byte(cfg.JWTSecret)},
		seam:     &seamless{store: store, cfg: cfg},
		adm:      &admin{store: store, token: cfg.AdminToken},
		provider: newProviderClient(cfg),
		rdb:      rdb,
	}
}

// Handler builds the routing table. Go 1.22 pattern mux gives us
// method+path routing with no external router dependency.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// player API (JWT)
	mux.HandleFunc("POST /api/auth/register", s.auth.register)
	mux.HandleFunc("POST /api/auth/login", s.auth.login)
	mux.HandleFunc("GET /api/wallet", requireAuth([]byte(s.cfg.JWTSecret), s.wallet.balance))
	mux.HandleFunc("POST /api/wallet/deposit", requireAuth([]byte(s.cfg.JWTSecret), s.wallet.deposit))
	mux.HandleFunc("POST /api/wallet/webhook", s.wallet.webhook)

	// games proxy (JWT): catalog + launch
	mux.HandleFunc("GET /api/games", requireAuth([]byte(s.cfg.JWTSecret), s.games))
	mux.HandleFunc("POST /api/games/launch", requireAuth([]byte(s.cfg.JWTSecret), s.launch))

	// seamless wallet — called by the provider, signed, no JWT
	mux.HandleFunc("POST /seamless/balance", s.seam.balance)
	mux.HandleFunc("POST /seamless/withdraw", s.seam.withdraw)
	mux.HandleFunc("POST /seamless/deposit", s.seam.deposit)
	mux.HandleFunc("POST /seamless/pushbetdata", s.seam.pushBetData)

	// admin
	mux.HandleFunc("GET /api/admin/stats", s.adm.stats)

	// health
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.store.DB.PingContext(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "db down", "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})

	// credential-stuffing guard on auth endpoints (Redis-backed)
	limiter := newRateLimiter(s.rdb, 30, time.Minute)
	handler := http.NewServeMux()
	handler.Handle("/api/auth/", limiter.Middleware(mux))
	handler.Handle("/api/wallet/webhook", limiter.Middleware(mux))
	handler.Handle("/", mux)

	return logMiddleware(handler)
}

// games proxies the provider catalog.
func (s *Server) games(w http.ResponseWriter, r *http.Request, _ int64) {
	games, err := s.provider.Games(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"games": games})
}

// launch proxies launch-game for the current player.
func (s *Server) launch(w http.ResponseWriter, r *http.Request, playerID int64) {
	var in struct {
		GameCode string `json:"game_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.GameCode == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "game_code required"})
		return
	}
	p, err := s.store.PlayerByID(r.Context(), playerID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	lobby := s.cfg.ProviderBase + "/lobby-return"
	url, err := s.provider.LaunchGame(r.Context(), p.Username, in.GameCode, lobby)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": url, "game_code": in.GameCode})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		slog.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"took_ms", time.Since(start).Milliseconds())
	})
}
