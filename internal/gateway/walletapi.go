package gateway

import (
	"encoding/json"
	"errors"
	"net/http"

	"h5-game-gateway/internal/money"
	"h5-game-gateway/internal/sign"
)

// walletAPI implements the player-facing wallet: balance, deposits via a
// mock crypto-payment webhook (Bitcart-style pattern) and withdrawals.
type walletAPI struct {
	store  *Store
	cfg    Config
	secret []byte
}

func (h *walletAPI) balance(w http.ResponseWriter, r *http.Request, playerID int64) {
	wlt, err := h.store.WalletByPlayerID(r.Context(), playerID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"balance": money.ToFloat(wlt.Balance),
		"currency": wlt.Currency,
	})
}

// deposit opens a pending deposit and returns its id; confirmation happens
// through the webhook below (in production this is the Bitcart/processor
// callback; here it is signed with the same md5 scheme).
func (h *walletAPI) deposit(w http.ResponseWriter, r *http.Request, playerID int64) {
	var in struct {
		Amount float64 `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Amount <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "amount must be positive"})
		return
	}
	minor := money.FromFloat(in.Amount)
	if minor <= 0 || minor > 1_000_000*money.Unit {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "amount out of range"})
		return
	}
	// The deposit id is deterministic: scope "payment", tx = dep-<id>.
	// The webhook confirms it. Here we create a pre-entry in `deposits`.
	res, err := h.store.DB.ExecContext(r.Context(),
		`INSERT INTO deposits (player_id, amount_minor, status) VALUES (?, ?, 'pending')`,
		playerID, minor)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	id, _ := res.LastInsertId()
	writeJSON(w, http.StatusOK, map[string]any{
		"deposit_id": id,
		"amount":     in.Amount,
		"status":     "pending",
		"howto":      "POST /api/wallet/webhook {deposit_id, sign} — sign = sha256(operator_code + request_time + secret_key + webhook)",
	})
}

type webhookIn struct {
	DepositID   int64  `json:"deposit_id"`
	Status      string `json:"status"` // confirmed | failed
	OperatorCode string `json:"operator_code"`
	RequestTime string `json:"request_time"`
	Sign        string `json:"sign"`
}

func (h *walletAPI) webhook(w http.ResponseWriter, r *http.Request) {
	var in webhookIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json"})
		return
	}
	if in.OperatorCode != h.cfg.OperatorCode ||
		!sign.ValidSHA256(in.OperatorCode, in.RequestTime, h.cfg.OperatorSecret, sign.MethodWebhook, in.Sign) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid sign"})
		return
	}
	// fetch pending deposit
	var playerID int64
	var amount int64
	var status string
	err := h.store.DB.QueryRowContext(r.Context(),
		`SELECT player_id, amount_minor, status FROM deposits WHERE id = ?`, in.DepositID).
		Scan(&playerID, &amount, &status)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "deposit not found"})
		return
	}
	if status != "pending" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "deposit already processed"})
		return
	}
	txID := "deposit-" + itoa(in.DepositID)
	if in.Status == "failed" {
		_, _ = h.store.DB.ExecContext(r.Context(),
			`UPDATE deposits SET status='failed', confirmed_at=NOW() WHERE id=?`, in.DepositID)
		writeJSON(w, http.StatusOK, map[string]any{"status": "failed"})
		return
	}
	balance, err := h.store.MoveMoney(r.Context(), playerID, "payment", txID, "deposit", "webhook", amount)
	if err != nil && !errors.Is(err, ErrDuplicate) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	_, _ = h.store.DB.ExecContext(r.Context(),
		`UPDATE deposits SET status='confirmed', confirmed_at=NOW() WHERE id=?`, in.DepositID)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "confirmed",
		"balance": money.ToFloat(balance),
	})
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
