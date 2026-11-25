package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"h5-game-gateway/internal/money"
	"h5-game-gateway/internal/sign"
)

// pgsoft implements the operator side of the PG Soft agent API
// ("seamless wallet"): the provider calls these callbacks with
// form-urlencoded bodies and expects a JSON {data, error} envelope with
// HTTP 200 in both success and protocol-error cases (per PG integration
// guide).
//
// Callbacks (operator registers them in the PG portal):
//   VerifySession        → /pgsoft/verify-session
//   Cash/Get (GetWallet) → /pgsoft/wallet
//   Cash/TransferInOut   → /pgsoft/bet-payout
type pgsoft struct {
	store *Store
	cfg   Config
	tickets sync.Map // ticket -> pgTicket
}

type pgTicket struct {
	html string
	exp  time.Time
}

var guidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func pgOK(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "error": nil})
}

func pgErr(w http.ResponseWriter, code, message string) {
	writeJSON(w, http.StatusOK, map[string]any{
		"data":  nil,
		"error": map[string]any{"code": code, "message": message},
	})
}

// parseFormBody reads the urlencoded body (PG sends form-urlencoded, and
// the raw body is needed for hash-auth signature verification).
func parseFormBody(r *http.Request) (url.Values, string, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, "", err
	}
	v, err := url.ParseQuery(string(raw))
	if err != nil {
		return nil, "", err
	}
	return v, string(raw), nil
}

// authorizePG validates operator_token (+ classic md5 sign, or token+secret
// fallback per the simplified agent scheme) and optional PWS-HMAC-SHA256
// hash headers. Error codes follow PG conventions: 1034 invalid request.
// Empty code means OK.
func (p *pgsoft) authorizePG(r *http.Request, form url.Values) (code, message string) {
	if form.Get("operator_token") != p.cfg.PGSoftOperatorToken {
		return "1034", "invalid operator token"
	}
	// classic sign over the sorted parameter set
	if s := form.Get("sign"); s != "" {
		m := map[string]string{}
		for k := range form {
			m[k] = form.Get(k)
		}
		if !sign.PGVerify(m, p.cfg.PGSoftSecretKey, s) {
			return "1034", "invalid sign"
		}
		return "", ""
	}
	// simplified agent scheme: secret_key carried in the body
	if sk := form.Get("secret_key"); sk != "" {
		if !constEQ(sk, p.cfg.PGSoftSecretKey) {
			return "1034", "invalid secret key"
		}
		return "", ""
	}
	return "1034", "missing signature"
}

func constEQ(a, b string) bool {
	ab, bb := []byte(a), []byte(b)
	if len(ab) != len(bb) {
		return false
	}
	var v byte
	for i := range ab {
		v |= ab[i] ^ bb[i]
	}
	return v == 0
}

// verifyHashHeaders implements the optional PWS-HMAC-SHA256 request signing:
// Authorization: PWS-HMAC-SHA256 Credential={x-date}/{operator_token}/pws/v1,
// SignedHeaders=host;x-content-sha256;x-date, Signature=hmac_sha256(salt, host+sha256(body)+x-date)
func (p *pgsoft) verifyHashHeaders(r *http.Request, rawBody string) bool {
	if !p.cfg.PGSoftHashAuth {
		return true
	}
	xDate := r.Header.Get("x-date")
	xSha := strings.ToLower(r.Header.Get("x-content-sha256"))
	auth := r.Header.Get("authorization")
	if xDate == "" || xSha == "" || auth == "" {
		return false
	}
	sha := sha256.Sum256([]byte(rawBody))
	if !constEQ(hex.EncodeToString(sha[:]), xSha) {
		return false
	}
	// Credential={x-date}/{token}/pws/v1, SignedHeaders=host;x-content-sha256;x-date, Signature={hex}
	const prefix = "PWS-HMAC-SHA256 Credential="
	if !strings.HasPrefix(auth, prefix) {
		return false
	}
	rest := auth[len(prefix):]
	var cred, signed, sig string
	for _, part := range strings.Split(rest, ",") {
		part = strings.TrimSpace(part)
		switch {
		case strings.HasPrefix(part, "SignedHeaders="):
			signed = strings.TrimPrefix(part, "SignedHeaders=")
		case strings.HasPrefix(part, "Signature="):
			sig = strings.TrimPrefix(part, "Signature=")
		default:
			cred = strings.TrimSuffix(part, "/pws/v1")
		}
	}
	if signed != "host;x-content-sha256;x-date" || !strings.HasPrefix(cred, xDate+"/") {
		return false
	}
	host := r.Host
	mac := hmac.New(sha256.New, []byte(p.cfg.PGSoftHashSalt))
	mac.Write([]byte(host + xSha + xDate))
	want := hex.EncodeToString(mac.Sum(nil))
	return constEQ(want, strings.ToLower(sig))
}

func (p *pgsoft) verifySession(w http.ResponseWriter, r *http.Request) {
	form, raw, err := parseFormBody(r)
	if err != nil {
		pgErr(w, "1034", "bad form body")
		return
	}
	if code, msg := p.authorizePG(r, form); code != "" {
		pgErr(w, code, msg)
		return
	}
	if !p.verifyHashHeaders(r, raw) {
		pgErr(w, "1034", "hash authentication failed")
		return
	}
	playerName := form.Get("player_session_id")
	playerID, err := p.playerFromOps(playerName)
	if err != nil {
		pgErr(w, "1034", "unknown session")
		return
	}
	pl, err := p.store.PlayerByID(r.Context(), playerID)
	if err != nil {
		pgErr(w, "1034", "unknown player")
		return
	}
	pgOK(w, map[string]any{
		"player_name": playerName,
		"nickname":    pl.Username,
		"currency":    pl.Currency,
	})
}

// wallet is Cash/Get: current balance of one player.
func (p *pgsoft) wallet(w http.ResponseWriter, r *http.Request) {
	form, raw, err := parseFormBody(r)
	if err != nil {
		pgErr(w, "1034", "bad form body")
		return
	}
	if code, msg := p.authorizePG(r, form); code != "" {
		pgErr(w, code, msg)
		return
	}
	if !p.verifyHashHeaders(r, raw) {
		pgErr(w, "1034", "hash authentication failed")
		return
	}
	playerID, err := p.playerFromOps(form.Get("player_session_id"))
	if err != nil {
		pgErr(w, "1034", "unknown session")
		return
	}
	wlt, err := p.store.WalletByPlayerID(r.Context(), playerID)
	if err != nil {
		pgErr(w, "1034", "player not found")
		return
	}
	pgOK(w, map[string]any{
		"currency_amount": fmt.Sprintf("%.4f", money.ToFloat(wlt.Balance)),
		"updated_time":    time.Now().Unix(),
	})
}

// betPayout is Cash/TransferInOut: ONE call per round with the NET amount
// (transfer_amount = win_amount - bet_amount; negative = player lost).
// Validations per PG integration guide: win-bet=transfer consistency,
// trace_id GUID, idempotency by bet_id, insufficient balance rejection.
func (p *pgsoft) betPayout(w http.ResponseWriter, r *http.Request) {
	form, raw, err := parseFormBody(r)
	if err != nil {
		pgErr(w, "1034", "bad form body")
		return
	}
	if code, msg := p.authorizePG(r, form); code != "" {
		pgErr(w, code, msg)
		return
	}
	if !p.verifyHashHeaders(r, raw) {
		pgErr(w, "1034", "hash authentication failed")
		return
	}
	if tid := form.Get("trace_id"); tid != "" && !guidRe.MatchString(strings.ToLower(tid)) {
		pgErr(w, "1034", "trace_id must be a GUID")
		return
	}
	playerID, err := p.playerFromOps(form.Get("player_session_id"))
	if err != nil {
		pgErr(w, "1034", "unknown session")
		return
	}
	bet := money.FromFloat(atof(form.Get("bet_amount")))
	win := money.FromFloat(atof(form.Get("win_amount")))
	transfer := money.FromFloat(atof(form.Get("transfer_amount")))
	if bet < 0 || win < 0 {
		pgErr(w, "1034", "negative bet/win amount")
		return
	}
	if win-bet != transfer {
		pgErr(w, "1034", "transfer_amount must equal win_amount - bet_amount")
		return
	}
	betID := form.Get("bet_id")
	if betID == "" {
		pgErr(w, "1034", "bet_id required")
		return
	}

	balance, err := p.store.MoveMoney(r.Context(), playerID, "pgsoft", betID, "round",
		form.Get("round_id")+"|"+form.Get("game_id"), transfer)
	if err != nil {
		switch {
		case err.Error() == ErrDuplicate.Error():
			// idempotent replay: return current balance, no double apply
			wlt, e := p.store.WalletByPlayerID(r.Context(), playerID)
			if e != nil {
				pgErr(w, "2000", e.Error())
				return
			}
			pgOK(w, map[string]any{"currency_amount": fmt.Sprintf("%.4f", money.ToFloat(wlt.Balance)), "updated_time": time.Now().Unix()})
			return
		case err.Error() == ErrInsufficient.Error():
			pgErr(w, "3003", "Insufficient Balance")
			return
		default:
			pgErr(w, "2000", err.Error())
			return
		}
	}

	// audit push stored raw
	payload, _ := json.Marshal(map[string]any{
		"bet_id": betID, "round_id": form.Get("round_id"), "game_id": form.Get("game_id"),
		"bet": money.ToFloat(bet), "win": money.ToFloat(win), "transfer": money.ToFloat(transfer),
	})
	_, _ = p.store.DB.ExecContext(r.Context(),
		`INSERT INTO bet_pushes (member_account, payload) VALUES (?, ?)`,
		form.Get("player_name"), string(payload))

	pgOK(w, map[string]any{
		"currency_amount": fmt.Sprintf("%.4f", money.ToFloat(balance)),
		"updated_time":    time.Now().Unix(),
	})
}

// play serves the provider HTML behind a short-lived ticket (the
// GetLaunchURLHTML response is streamed to the player's iframe; the ticket
// stays valid until expiry so an iframe reload doesn't kill the page).
func (p *pgsoft) play(w http.ResponseWriter, r *http.Request) {
	ticket := r.URL.Query().Get("ticket")
	v, ok := p.tickets.Load(ticket)
	if !ok {
		http.Error(w, "unknown or expired ticket", http.StatusNotFound)
		return
	}
	t := v.(pgTicket)
	if time.Now().After(t.exp) {
		p.tickets.Delete(ticket)
		http.Error(w, "ticket expired", http.StatusGone)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(t.html))
}

func atof(s string) float64 {
	var f float64
	fmt.Sscanf(strings.TrimSpace(s), "%g", &f)
	return f
}

// playerFromOps decodes the `ops` session token the gateway issued at
// launch (PG passes it back in callbacks as player_session_id).
func (p *pgsoft) playerFromOps(ops string) (int64, error) {
	if ops == "" {
		return 0, fmt.Errorf("empty ops")
	}
	tok, err := jwt.Parse(ops, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("bad signing method")
		}
		return []byte(p.cfg.JWTSecret), nil
	})
	if err != nil || !tok.Valid {
		return 0, fmt.Errorf("invalid ops token")
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok {
		return 0, fmt.Errorf("bad claims")
	}
	sub, ok := claims["sub"].(float64)
	if !ok {
		return 0, fmt.Errorf("bad sub")
	}
	return int64(sub), nil
}

// issueOpsToken mints the session token handed to the provider at launch.
func (p *pgsoft) issueOpsToken(playerID int64, gameCode string) (string, error) {
	claims := jwt.MapClaims{
		"sub":  playerID,
		"game": gameCode,
		"exp":  time.Now().Add(2 * time.Hour).Unix(),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(p.cfg.JWTSecret))
}

// launchPG runs the PG Soft launch flow: signed GetLaunchURLHTML call to the
// provider, the returned HTML is stored behind a one-time ticket; the
// player's iframe opens /pgsoft/play?ticket=... on the gateway.
func (p *pgsoft) launchPG(ctx context.Context, playerID int64, username, gameCode string) (string, error) {
	ops, err := p.issueOpsToken(playerID, gameCode)
	if err != nil {
		return "", err
	}
	form := url.Values{}
	form.Set("operator_token", p.cfg.PGSoftOperatorToken)
	form.Set("path", "/"+gameCode+"/index.html")
	form.Set("extra_args", "ops="+url.QueryEscape(ops)+"&l=en")
	form.Set("url_type", "game-entry")
	form.Set("client_ip", "127.0.0.1")
	params := map[string]string{}
	for k := range form {
		params[k] = form.Get(k)
	}
	form.Set("sign", sign.PGSign(params, p.cfg.PGSoftSecretKey))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.cfg.ProviderBase+"/pgsoft/external-game-launcher/api/v1/GetLaunchURLHTML",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	html, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GetLaunchURLHTML: %d %s", resp.StatusCode, string(html[:min(len(html), 200)]))
	}
	ticket := newTicket()
	p.tickets.Store(ticket, pgTicket{html: string(html), exp: time.Now().Add(2 * time.Minute)})
	return "/pgsoft/play?ticket=" + ticket, nil
}

// gamesPG proxies the PG-style catalog.
func (p *pgsoft) gamesPG(ctx context.Context) ([]providerGame, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.cfg.ProviderBase+"/pgsoft/api/v1/GameList", nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var env struct {
		Data []struct {
			GameID    string `json:"game_id"`
			GameName  string `json:"game_name"`
			Thumbnail string `json:"thumbnail"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, err
	}
	out := make([]providerGame, 0, len(env.Data))
	for _, g := range env.Data {
		out = append(out, providerGame{GameCode: g.GameID, Name: g.GameName, GameType: "SLOT", Thumbnail: g.Thumbnail})
	}
	return out, nil
}

func newTicket() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
