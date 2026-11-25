package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"h5-game-gateway/internal/money"
	"h5-game-gateway/internal/sign"
)

// seamless implements the operator side of the seamless-wallet contract:
// the GAME PROVIDER calls these endpoints on every player round.
//
// Auth: every request carries operator_code, request_time and sign =
// md5(operator_code + request_time + secret_key + method_word). The response
// envelope mirrors slot-aggregator APIs: {code, message, data}.
type seamless struct {
	store *Store
	cfg   Config
}

// request is the common signed envelope of all seamless callbacks.
type seamlessRequest struct {
	OperatorCode string `json:"operator_code"`
	RequestTime  string `json:"request_time"`
	Sign         string `json:"sign"`
}

type txIn struct {
	ID       string  `json:"id"`        // provider-side unique transaction id
	Action   string  `json:"action"`    // "bet" | "settled" | "bonus" ...
	Amount   float64 `json:"amount"`    // minor units will be derived via money.FromFloat
	Currency string  `json:"currency"`
	RoundID  string  `json:"round_id"`
	GameCode string  `json:"game_code"`
	WagerCode string `json:"wager_code"`
}

func (s *seamless) authorize(env *seamlessRequest, methodWord string) (code int, msg string) {
	if env.OperatorCode != s.cfg.OperatorCode {
		return 1002, "unknown operator"
	}
	if !freshTimestamp(env.RequestTime, 5*time.Minute) {
		return 1003, "stale request_time"
	}
	if !sign.Valid(env.OperatorCode, env.RequestTime, s.cfg.OperatorSecret, methodWord, env.Sign) {
		return 1001, "invalid sign"
	}
	return 0, ""
}

func (s *seamless) balance(w http.ResponseWriter, r *http.Request) {
	var in struct {
		seamlessRequest
		MemberAccount string `json:"member_account"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeSeamless(w, 1003, "bad json", nil)
		return
	}
	if code, msg := s.authorize(&in.seamlessRequest, sign.MethodBalance); code != 0 {
		writeSeamless(w, code, msg, nil)
		return
	}
	p, err := s.store.PlayerByName(r.Context(), in.MemberAccount)
	if err != nil {
		writeSeamless(w, 1004, "player not found", nil)
		return
	}
	wlt, err := s.store.WalletByPlayerID(r.Context(), p.ID)
	if err != nil {
		writeSeamless(w, 2000, err.Error(), nil)
		return
	}
	writeSeamless(w, 0, "", map[string]any{"member_account": in.MemberAccount, "balance": money.ToFloat(wlt.Balance)})
}

// withdraw debits bets. Body adds transactions[] with unique provider ids.
func (s *seamless) withdraw(w http.ResponseWriter, r *http.Request) {
	var in struct {
		seamlessRequest
		MemberAccount string `json:"member_account"`
		Transactions  []txIn `json:"transactions"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeSeamless(w, 1003, "bad json", nil)
		return
	}
	if code, msg := s.authorize(&in.seamlessRequest, sign.MethodWithdraw); code != 0 {
		writeSeamless(w, code, msg, nil)
		return
	}
	if len(in.Transactions) == 0 || in.MemberAccount == "" {
		writeSeamless(w, 1003, "member_account and transactions required", nil)
		return
	}
	p, err := s.store.PlayerByName(r.Context(), in.MemberAccount)
	if err != nil {
		writeSeamless(w, 1004, "player not found", nil)
		return
	}
	lastBalance := int64(0)
	for _, tx := range in.Transactions {
		minor := money.FromFloat(tx.Amount)
		if minor <= 0 {
			writeSeamless(w, 1003, "amount must be positive", nil)
			return
		}
		balance, err := s.store.MoveMoney(r.Context(), p.ID, "provider", tx.ID, "bet",
			tx.RoundID+"|"+tx.GameCode, -minor)
		if err != nil {
			switch {
			case errors.Is(err, ErrDuplicate):
				// already applied — idempotent success
				lastBalance = balance
				continue
			case errors.Is(err, ErrInsufficient):
				writeSeamless(w, 1005, "insufficient balance", nil)
				return
			default:
				writeSeamless(w, 2000, err.Error(), nil)
				return
			}
		}
		lastBalance = balance
	}
	writeSeamless(w, 0, "", map[string]any{"member_account": in.MemberAccount, "balance": money.ToFloat(lastBalance)})
}

// deposit credits payouts/bonuses (provider pushes settled wins).
func (s *seamless) deposit(w http.ResponseWriter, r *http.Request) {
	var in struct {
		seamlessRequest
		MemberAccount string `json:"member_account"`
		Transactions  []txIn `json:"transactions"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeSeamless(w, 1003, "bad json", nil)
		return
	}
	if code, msg := s.authorize(&in.seamlessRequest, sign.MethodDeposit); code != 0 {
		writeSeamless(w, code, msg, nil)
		return
	}
	if len(in.Transactions) == 0 || in.MemberAccount == "" {
		writeSeamless(w, 1003, "member_account and transactions required", nil)
		return
	}
	p, err := s.store.PlayerByName(r.Context(), in.MemberAccount)
	if err != nil {
		writeSeamless(w, 1004, "player not found", nil)
		return
	}
	lastBalance := int64(0)
	for _, tx := range in.Transactions {
		minor := money.FromFloat(tx.Amount)
		if minor < 0 {
			writeSeamless(w, 1003, "amount must not be negative", nil)
			return
		}
		balance, err := s.store.MoveMoney(r.Context(), p.ID, "provider", tx.ID, "win",
			tx.RoundID+"|"+tx.GameCode, minor)
		if err != nil && !errors.Is(err, ErrDuplicate) {
			writeSeamless(w, 2000, err.Error(), nil)
			return
		}
		lastBalance = balance
	}
	writeSeamless(w, 0, "", map[string]any{"member_account": in.MemberAccount, "balance": money.ToFloat(lastBalance)})
}

// pushbetdata accepts the round audit push and stores it raw (simplified).
func (s *seamless) pushBetData(w http.ResponseWriter, r *http.Request) {
	var in struct {
		seamlessRequest
		MemberAccount string          `json:"member_account"`
		Payload       json.RawMessage `json:"payload"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeSeamless(w, 1003, "bad json", nil)
		return
	}
	if code, msg := s.authorize(&in.seamlessRequest, sign.MethodPushBetData); code != 0 {
		writeSeamless(w, code, msg, nil)
		return
	}
	_, _ = s.store.DB.ExecContext(r.Context(),
		`INSERT INTO bet_pushes (member_account, payload) VALUES (?, ?)`,
		in.MemberAccount, string(in.Payload))
	writeSeamless(w, 0, "", nil)
}

func writeSeamless(w http.ResponseWriter, code int, msg string, data any) {
	writeJSON(w, http.StatusOK, map[string]any{"code": code, "message": msg, "data": data})
}

func freshTimestamp(ts string, maxAge time.Duration) bool {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		// also accept unix seconds
		var unix int64
		for _, c := range ts {
			if c < '0' || c > '9' {
				return false
			}
			unix = unix*10 + int64(c-'0')
		}
		if unix == 0 {
			return false
		}
		t = time.Unix(unix, 0)
	}
	now := time.Now()
	return !t.Before(now.Add(-maxAge)) && !t.After(now.Add(maxAge))
}
