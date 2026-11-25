package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"h5-game-gateway/internal/sign"
)

// session is one launch of a game by one player (mock: kept in memory).
type session struct {
	Token      string
	OperatorCode string
	MemberAccount string
	GameCode   string
	Currency   string
	Style      string // "gsc" | "pgsoft" — settlement contract used for this session
	Fairness   Fairness
	ClientSeed string
	Nonce      int64
	Status     string // "active" | "closed"
	CreatedAt  time.Time
}

// Server is the fake PG-style game provider.
type Server struct {
	cfg   Config
	http  *http.Client
	mu    sync.Mutex
	sess  map[string]*session // by token
	stats Stats
}

type Config struct {
	Addr         string
	OperatorCode string
	OperatorSecret string
	CallbackBase string // operator gateway /seamless base url
	PublicBase   string // optional absolute base for launch urls

	// PG Soft agent credentials (mirrored with the gateway).
	PGOperatorCode  string
	PGOperatorToken string
	PGSecretKey     string
}

func timeNow() time.Time { return time.Now() }

type Stats struct {
	mu            sync.Mutex
	RoundsServed  int64
	BetMinorSum   float64
	WinMinorSum   float64
}

func NewServer(cfg Config) *Server {
	return &Server{
		cfg:  cfg,
		http: &http.Client{Timeout: 10 * time.Second},
		sess: make(map[string]*session),
	}
}

// ---- operator-facing API (signed) ----

type operatorEnvelope struct {
	OperatorCode string `json:"operator_code"`
	RequestTime  string `json:"request_time"`
	Sign         string `json:"sign"`
}

func (s *Server) authOperator(env *operatorEnvelope, methodWord string) (int, string) {
	if env.OperatorCode != s.cfg.OperatorCode {
		return 1002, "unknown operator"
	}
	if !sign.Valid(env.OperatorCode, env.RequestTime, s.cfg.OperatorSecret, methodWord, env.Sign) {
		return 1001, "invalid sign"
	}
	return 0, ""
}

// games lists the catalog (the dice/crash thumbnails are inline SVG data
// URIs so the demo needs no asset hosting).
func (s *Server) games(w http.ResponseWriter, r *http.Request) {
	var in operatorEnvelope
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"code": 1003, "message": "bad json"})
		return
	}
	if code, msg := s.authOperator(&in, sign.MethodGames); code != 0 {
		writeJSON(w, http.StatusOK, map[string]any{"code": code, "message": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code": 0,
		"games": []map[string]string{
			{"game_code": "dice", "name": "Lucky Dice", "game_type": "DICE",
				"thumbnail": "/game/static/dice.svg"},
			{"game_code": "crash", "name": "Mini Crash", "game_type": "CRASH",
				"thumbnail": "/game/static/crash.svg"},
		},
	})
}

// launch creates a session and returns the H5 play URL.
func (s *Server) launch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		operatorEnvelope
		MemberAccount    string `json:"member_account"`
		GameCode         string `json:"game_code"`
		OperatorLobbyURL string `json:"operator_lobby_url"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"code": 1003, "message": "bad json"})
		return
	}
	if code, msg := s.authOperator(&in.operatorEnvelope, sign.MethodLaunch); code != 0 {
		writeJSON(w, http.StatusOK, map[string]any{"code": code, "message": msg})
		return
	}
	if in.GameCode != "dice" && in.GameCode != "crash" {
		writeJSON(w, http.StatusOK, map[string]any{"code": 1003, "message": "unknown game_code"})
		return
	}
	if in.MemberAccount == "" {
		writeJSON(w, http.StatusOK, map[string]any{"code": 1003, "message": "member_account required"})
		return
	}
	seed, err := newSeed()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"code": 2000, "message": err.Error()})
		return
	}
	tokRaw, _ := newSeed()
	sess := &session{
		Token:         tokRaw,
		OperatorCode:  in.OperatorCode,
		MemberAccount: in.MemberAccount,
		GameCode:      in.GameCode,
		Currency:      "USD",
		Fairness:      Fairness{ServerSeed: seed, ServerSeedHash: hashSeed(seed)},
		Status:        "active",
		CreatedAt:     time.Now(),
	}
	s.mu.Lock()
	s.sess[sess.Token] = sess
	s.mu.Unlock()

	playURL := "/game/play?token=" + sess.Token
	if s.cfg.PublicBase != "" {
		playURL = s.cfg.PublicBase + playURL
	}
	slog.Info("launch", "player", in.MemberAccount, "game", in.GameCode)
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "url": playURL})
}

// ---- player-facing game API (token-authed) ----

type betRequest struct {
	Token      string  `json:"token"`
	Amount     float64 `json:"amount"`        // whole units with up to 4 decimals
	ClientSeed string  `json:"client_seed"`
	WinUnder   int64   `json:"win_under"`     // dice: win if roll < win_under
	Target     float64 `json:"target"`        // crash: auto-cashout multiplier
}

type seamlessTx struct {
	ID        string  `json:"id"`
	Action    string  `json:"action"`
	Amount    float64 `json:"amount"`
	Currency  string  `json:"currency"`
	RoundID   string  `json:"round_id"`
	GameCode  string  `json:"game_code"`
	WagerCode string  `json:"wager_code"`
}

type seamlessResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Balance float64 `json:"balance"`
	} `json:"data"`
}

// seamlessCall POSTs a signed callback to the operator gateway.
func (s *Server) seamlessCall(methodWord, path, memberAccount string, txs []seamlessTx, extra map[string]any) (*seamlessResp, error) {
	rt := sign.Now()
	body := map[string]any{
		"operator_code":  s.cfg.OperatorCode,
		"request_time":   rt,
		"sign":           sign.Compute(s.cfg.OperatorCode, rt, s.cfg.OperatorSecret, methodWord),
		"member_account": memberAccount,
		"currency":       "USD",
	}
	if txs != nil {
		body["transactions"] = txs
	}
	for k, v := range extra {
		body[k] = v
	}
	buf, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, s.cfg.CallbackBase+path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out := &seamlessResp{}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return nil, err
	}
	return out, nil
}

// betPG settles a round through the PG Soft contract: Cash/Get before the
// round, ONE signed Cash/TransferInOut with the net amount after.
func (s *Server) betPG(w http.ResponseWriter, sess *session, roundID string, nonce int64, in betRequest) {
	bal, code, err := s.pgGetBalance(sess)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "wallet check failed", "operator_code": code})
		return
	}
	if bal < in.Amount {
		writeJSON(w, http.StatusOK, map[string]any{"error": "insufficient balance", "operator_code": "3003"})
		return
	}

	roll := Roll(sess.Fairness.ServerSeed, in.ClientSeed, nonce)
	var win bool
	var multiplier, payout float64
	switch sess.GameCode {
	case "crash":
		cp := CrashPoint(roll)
		if in.Target >= 1.01 && in.Target <= cp {
			win = true
			multiplier = in.Target
		}
	default: // dice
		win, multiplier = DiceRound(roll, in.WinUnder)
	}
	if win {
		payout = float64(int64(in.Amount*10000)) * multiplier / 10000.0
	}

	newBal, pgCode, err := s.pgSettle(sess, roundID, in.Amount, payout)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"error": "operator rejected settlement", "operator_code": pgCode, "operator_message": err.Error(),
		})
		return
	}

	s.mu.Lock()
	s.stats.RoundsServed++
	s.stats.BetMinorSum += in.Amount
	s.stats.WinMinorSum += payout
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"round_id": roundID, "roll": roll, "nonce": nonce,
		"win": win, "multiplier": multiplier, "payout": payout, "balance": newBal,
		"fairness": map[string]string{"server_seed_hash": sess.Fairness.ServerSeedHash},
	})
}

// bet plays one round: debit via seamless withdraw, resolve by provably
// fair roll, credit wins via seamless deposit, push audit data.
func (s *Server) bet(w http.ResponseWriter, r *http.Request) {
	var in betRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json"})
		return
	}
	s.mu.Lock()
	sess := s.sess[in.Token]
	if sess == nil || sess.Status != "active" {
		s.mu.Unlock()
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid or closed session"})
		return
	}
	sess.Nonce++
	nonce := sess.Nonce
	sess.ClientSeed = in.ClientSeed
	style := sess.Style
	s.mu.Unlock()

	amountMinor := int64(in.Amount * 10000)
	if amountMinor <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "amount must be positive"})
		return
	}
	roundID := fmt.Sprintf("%s-%d", sess.Token[:8], nonce)

	if style == "pgsoft" {
		s.betPG(w, sess, roundID, nonce, in)
		return
	}
	txID := fmt.Sprintf("%s-%d", sess.Token[:12], nonce)

	// 1. debit the bet
	resp, err := s.seamlessCall(sign.MethodWithdraw, "/seamless/withdraw", sess.MemberAccount,
		[]seamlessTx{{
			ID: txID + "-bet", Action: "bet", Amount: in.Amount, Currency: "USD",
			RoundID: roundID, GameCode: sess.GameCode, WagerCode: roundID,
		}}, nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "operator unreachable"})
		return
	}
	if resp.Code != 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"error": "operator rejected bet", "operator_code": resp.Code, "operator_message": resp.Message,
		})
		return
	}
	balance := resp.Data.Balance

	// 2. resolve the round
	roll := Roll(sess.Fairness.ServerSeed, in.ClientSeed, nonce)
	var win bool
	var multiplier float64
	var payout float64
	switch sess.GameCode {
	case "dice":
		win, multiplier = DiceRound(roll, in.WinUnder)
	case "crash":
		cp := CrashPoint(roll)
		if in.Target >= 1.01 && in.Target <= cp {
			win = true
			multiplier = in.Target
		} else {
			win = false
			multiplier = 0
		}
	}
	if win {
		payout = float64(amountMinor) * multiplier / 10000.0
	}

	// 3. credit the win (skip zero-payout deposits: aggregators skip them too)
	if payout > 0 {
		resp2, err := s.seamlessCall(sign.MethodDeposit, "/seamless/deposit", sess.MemberAccount,
			[]seamlessTx{{
				ID: txID + "-win", Action: "settled", Amount: payout, Currency: "USD",
				RoundID: roundID, GameCode: sess.GameCode, WagerCode: roundID,
			}}, nil)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": "operator unreachable on payout"})
			return
		}
		if resp2.Code != 0 {
			writeJSON(w, http.StatusOK, map[string]any{
				"error": "operator rejected payout", "operator_code": resp2.Code, "operator_message": resp2.Message,
				"note": "bet already debited — in production this triggers a recovery queue",
			})
			return
		}
		balance = resp2.Data.Balance
	}

	// 4. audit push (signed, with the round payload)
	_, _ = s.seamlessCall(sign.MethodPushBetData, "/seamless/pushbetdata", sess.MemberAccount, nil, map[string]any{
		"payload": map[string]any{
			"round_id": roundID, "game_code": sess.GameCode, "roll": roll,
			"amount": in.Amount, "payout": payout, "nonce": nonce,
		},
	})

	s.mu.Lock()
	s.stats.RoundsServed++
	s.stats.BetMinorSum += in.Amount
	s.stats.WinMinorSum += payout
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"round_id": roundID, "roll": roll, "nonce": nonce,
		"win": win, "multiplier": multiplier, "payout": payout, "balance": balance,
		"fairness": map[string]string{"server_seed_hash": sess.Fairness.ServerSeedHash},
	})
}

// fairness reveals the server seed for a closed/active session so the
// player can verify every roll against the published hash.
func (s *Server) fairness(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	s.mu.Lock()
	sess := s.sess[token]
	s.mu.Unlock()
	if sess == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "session not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"server_seed":      sess.Fairness.ServerSeed,
		"server_seed_hash": sess.Fairness.ServerSeedHash,
		"client_seed":      sess.ClientSeed,
		"nonce":            sess.Nonce,
		"verify":           "roll = int(hmac_sha256(server_seed, client_seed:nonce)[:8]) mod 10000",
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "rounds_served": s.stats.RoundsServed})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
