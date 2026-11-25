package provider

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"h5-game-gateway/internal/sign"
)

// pgSoftGameList serves the PG-style catalog (numeric game ids, PG envelope).
func (s *Server) pgGameList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"data": []map[string]string{
			{"game_id": "2024", "game_name": "PG Lucky Dice", "game_type": "SLOT", "thumbnail": "/game/static/dice.svg"},
			{"game_id": "2025", "game_name": "PG Mini Crash", "game_type": "CRASH", "thumbnail": "/game/static/crash.svg"},
		},
		"error": nil,
	})}

// pgGetLaunchURLHTML mirrors PG Soft's GetLaunchURLHTML: form-urlencoded
// request (operator_token, path, extra_args, url_type, client_ip, sign),
// response = HTML to stream into the player's iframe. The `ops` extra arg
// carries the operator session token; PG passes it back in wallet callbacks
// as player_session_id.
func (s *Server) pgGetLaunchURLHTML(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" &&
		!strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		http.Error(w, "content-type must be form-urlencoded", http.StatusUnsupportedMediaType)
		return
	}
	if r.Form.Get("operator_token") != s.cfg.PGOperatorToken {
		http.Error(w, "invalid operator token", http.StatusForbidden)
		return
	}
	path := r.Form.Get("path") // "/2024/index.html" | "/web-lobby/games/"
	gameCode := "dice"
	switch {
	case strings.HasPrefix(path, "/2025/"):
		gameCode = "crash"
	case strings.HasPrefix(path, "/2024/"):
		gameCode = "dice"
	default:
		if !strings.Contains(path, "web-lobby") {
			http.Error(w, "unknown game path", http.StatusBadRequest)
			return
		}
		gameCode = "dice"
	}
	ops := ""
	if extra := r.Form.Get("extra_args"); extra != "" {
		if ev, err := url.ParseQuery(extra); err == nil {
			ops = ev.Get("ops")
		}
	}
	if ops == "" {
		http.Error(w, "missing ops in extra_args", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	sess, exists := s.sess[ops]
	if !exists {
		seed, _ := newSeed()
		tokRaw, _ := newSeed()
		sess = &session{
			Token:         ops,
			OperatorCode:  s.cfg.PGOperatorCode,
			MemberAccount: "pg-" + ops[:8],
			GameCode:      gameCode,
			Currency:      "USD",
			Style:         "pgsoft",
			Fairness:      Fairness{ServerSeed: seed, ServerSeedHash: hashSeed(seed)},
			Status:        "active",
			CreatedAt:     timeNow(),
		}
		_ = tokRaw
		s.sess[ops] = sess
	} else {
		sess.GameCode = gameCode
	}
	s.mu.Unlock()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = playTmpl.Execute(w, map[string]string{
		"Token":          ops,
		"GameCode":       gameCode,
		"ServerSeedHash": sess.Fairness.ServerSeedHash,
	})
}

// pgSettle performs one PG-style round settlement: a signed, form-urlencoded
// Cash/TransferInOut call with the NET amount (transfer = win - bet).
func (s *Server) pgSettle(sess *session, roundID string, betAmount, winAmount float64) (newBalance float64, pgCode string, err error) {
	traceGUID, _ := newGUIDv4()
	betID := fmt.Sprintf("bet-%s", roundID)
	params := map[string]string{
		"operator_token":      s.cfg.PGOperatorToken,
		"bet_amount":          fmt.Sprintf("%.4f", betAmount),
		"win_amount":          fmt.Sprintf("%.4f", winAmount),
		"transfer_amount":     fmt.Sprintf("%.4f", winAmount-betAmount),
		"real_transfer_amount": fmt.Sprintf("%.4f", winAmount-betAmount),
		"currency":            "USD",
		"wallet_currency":     "USD",
		"bet_type":            "1",
		"transfer_type":       "0",
		"platform_type":       "web",
		"game_id":             pgGameID(sess.GameCode),
		"round_id":            roundID,
		"bet_id":              betID,
		"player_name":         sess.MemberAccount,
		"player_session_id":   sess.Token,
		"updated_time":        fmt.Sprintf("%d", timeNow().UnixMilli()),
		"trace_id":            traceGUID,
	}
	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	form.Set("sign", sign.PGSign(params, s.cfg.PGSecretKey))

	resp, err := s.http.Post(s.cfg.CallbackBase+"/pgsoft/bet-payout",
		"application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	var env struct {
		Data *struct {
			CurrencyAmount string `json:"currency_amount"`
		} `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&env); err != nil {
		return 0, "", err
	}
	if env.Error != nil {
		return 0, env.Error.Code, fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
	}
	if env.Data == nil {
		return 0, "", fmt.Errorf("empty data")
	}
	fmt.Sscanf(env.Data.CurrencyAmount, "%g", &newBalance)
	return newBalance, "0", nil
}

// pgGetBalance calls Cash/Get for the current balance.
func (s *Server) pgGetBalance(sess *session) (float64, string, error) {
	params := map[string]string{
		"operator_token":    s.cfg.PGOperatorToken,
		"player_name":       sess.MemberAccount,
		"player_session_id": sess.Token,
		"currency":          "USD",
	}
	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	form.Set("sign", sign.PGSign(params, s.cfg.PGSecretKey))
	resp, err := s.http.Post(s.cfg.CallbackBase+"/pgsoft/wallet",
		"application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	var env struct {
		Data *struct {
			CurrencyAmount string `json:"currency_amount"`
		} `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return 0, "", err
	}
	if env.Error != nil {
		return 0, env.Error.Code, fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
	}
	var bal float64
	if env.Data != nil {
		fmt.Sscanf(env.Data.CurrencyAmount, "%g", &bal)
	}
	return bal, "0", nil
}

// newGUIDv4 returns a RFC-4122 version-4 GUID (version nibble 4, variant
// 10x) — PG validates trace_id against this shape.
func newGUIDv4() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10x
	h := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32]), nil
}

func pgGameID(code string) string {
	if code == "crash" {
		return "2025"
	}
	return "2024"
}
