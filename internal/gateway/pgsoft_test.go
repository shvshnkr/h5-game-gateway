package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"h5-game-gateway/internal/sign"
)

// pgSignedForm builds a PG-style urlencoded callback body (operator_token +
// classic sorted-md5 sign).
func pgSignedForm(cfg Config, extra map[string]string) string {
	params := map[string]string{"operator_token": cfg.PGSoftOperatorToken}
	for k, v := range extra {
		params[k] = v
	}
	form := strings.Builder{}
	// emulate url.Values.Encode ordering — sign is computed on the map, so
	// form order does not matter for verification
	pairs := make([]string, 0)
	for k, v := range params {
		pairs = append(pairs, k+"="+v)
	}
	form.WriteString(strings.Join(pairs, "&"))
	form.WriteString("&sign=" + sign.PGSign(params, cfg.PGSoftSecretKey))
	return form.String()
}

func TestIntegrationPGSoftWalletAndTransfer(t *testing.T) {
	s := integrationStore(t)
	cfg := LoadConfig()
	cfg.MySQLDSN = "unused"
	cfg.ProviderStyle = "pgsoft"

	id := registerPlayer(t, s, "dave")
	if _, err := s.MoveMoney(t.Context(), id, "payment", "d-pg-1", "deposit", "", moneyUnit(30)); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(NewServer(cfg, s).Handler())
	defer srv.Close()

	// build an ops token via the pgsoft issuer
	pg := &pgsoft{store: s, cfg: cfg}
	ops, err := pg.issueOpsToken(id, "2024")
	if err != nil {
		t.Fatal(err)
	}

	// 1. Cash/Get → balance 30
	body := pgSignedForm(cfg, map[string]string{
		"player_session_id": ops, "player_name": "pg_dave", "currency": "USD",
	})
	resp := postForm(t, srv.URL+"/pgsoft/wallet", body)
	if e := resp["error"]; e != nil {
		t.Fatalf("wallet error: %v", resp)
	}
	data := resp["data"].(map[string]any)
	if data["currency_amount"] != "30.0000" {
		t.Fatalf("balance = %v, want 30.0000", data["currency_amount"])
	}

	// 2. TransferInOut: bet 5, win 0 → transfer -5 → balance 25
	body = pgSignedForm(cfg, map[string]string{
		"player_session_id": ops, "player_name": "pg_dave", "currency": "USD",
		"bet_amount": "5", "win_amount": "0", "transfer_amount": "-5",
		"real_transfer_amount": "-5", "wallet_currency": "USD", "bet_type": "1",
		"transfer_type": "0", "platform_type": "web", "game_id": "2024",
		"round_id": "r-1", "bet_id": "bet-r-1", "updated_time": "1796000000000",
		"trace_id": "0d0e9a51-abcd-4a2b-9c3d-1f2e3a4b5c6d",
	})
	resp = postForm(t, srv.URL+"/pgsoft/bet-payout", body)
	if resp["error"] != nil {
		t.Fatalf("transfer error: %v", resp)
	}
	if resp["data"].(map[string]any)["currency_amount"] != "25.0000" {
		t.Fatalf("balance after bet = %v, want 25.0000", resp["data"].(map[string]any)["currency_amount"])
	}

	// 3. same bet_id replay → idempotent success, balance unchanged
	resp = postForm(t, srv.URL+"/pgsoft/bet-payout", body)
	if resp["error"] != nil {
		t.Fatalf("replay error: %v", resp)
	}
	if resp["data"].(map[string]any)["currency_amount"] != "25.0000" {
		t.Fatalf("replay changed balance: %v", resp["data"].(map[string]any)["currency_amount"])
	}

	// 4. win-bet=transfer mismatch → 1034
	bad := pgSignedForm(cfg, map[string]string{
		"player_session_id": ops, "player_name": "pg_dave", "currency": "USD",
		"bet_amount": "5", "win_amount": "2", "transfer_amount": "9",
		"real_transfer_amount": "9", "wallet_currency": "USD", "bet_type": "1",
		"transfer_type": "0", "platform_type": "web", "game_id": "2024",
		"round_id": "r-2", "bet_id": "bet-r-2", "updated_time": "1796000000001",
		"trace_id": "0d0e9a51-abcd-4a2b-9c3d-1f2e3a4b5c6e",
	})
	resp = postForm(t, srv.URL+"/pgsoft/bet-payout", bad)
	if resp["error"] == nil || resp["error"].(map[string]any)["code"] != "1034" {
		t.Fatalf("consistency check not enforced: %v", resp)
	}

	// 5. insufficient balance → 3003
	resp = postForm(t, srv.URL+"/pgsoft/bet-payout", pgSignedForm(cfg, map[string]string{
		"player_session_id": ops, "player_name": "pg_dave", "currency": "USD",
		"bet_amount": "500", "win_amount": "0", "transfer_amount": "-500",
		"real_transfer_amount": "-500", "wallet_currency": "USD", "bet_type": "1",
		"transfer_type": "0", "platform_type": "web", "game_id": "2024",
		"round_id": "r-3", "bet_id": "bet-r-3", "updated_time": "1796000000002",
		"trace_id": "0d0e9a51-abcd-4a2b-9c3d-1f2e3a4b5c6f",
	}))
	errObj, ok := resp["error"].(map[string]any)
	if !ok || errObj["code"] != "3003" {
		t.Fatalf("insufficient balance not reported as 3003: %v", resp)
	}

	// 6. bad token → 1034
	resp = postForm(t, srv.URL+"/pgsoft/wallet", "operator_token=WRONG&player_session_id="+ops+"&sign=x")
	if resp["error"] == nil || resp["error"].(map[string]any)["code"] != "1034" {
		t.Fatalf("bad token not rejected: %v", resp)
	}
}

func TestPGHashAuthHeaders(t *testing.T) {
	cfg := LoadConfig()
	cfg.PGSoftHashAuth = true
	cfg.PGSoftHashSalt = "salt-123"
	pg := &pgsoft{cfg: cfg}

	body := "operator_token=x&player_session_id=y"
	sha := sha256.Sum256([]byte(body))
	xSha := hex.EncodeToString(sha[:])
	xDate := "20261126"

	mac := hmac.New(sha256.New, []byte(cfg.PGSoftHashSalt))
	mac.Write([]byte("127.0.0.1:8081" + xSha + xDate))
	sig := hex.EncodeToString(mac.Sum(nil))

	r := httptest.NewRequest(http.MethodPost, "/pgsoft/wallet", strings.NewReader(body))
	r.Host = "127.0.0.1:8081"
	r.Header.Set("x-date", xDate)
	r.Header.Set("x-content-sha256", xSha)
	r.Header.Set("authorization", fmt.Sprintf(
		"PWS-HMAC-SHA256 Credential=%s/pg-operator-token/pws/v1, SignedHeaders=host;x-content-sha256;x-date, Signature=%s",
		xDate, sig))
	if !pg.verifyHashHeaders(r, body) {
		t.Fatal("valid hash headers rejected")
	}

	r2 := httptest.NewRequest(http.MethodPost, "/pgsoft/wallet", strings.NewReader(body+"tampered"))
	r2.Host = r.Host
	r2.Header.Set("x-date", xDate)
	r2.Header.Set("x-content-sha256", xSha)
	r2.Header.Set("authorization", r.Header.Get("authorization"))
	if pg.verifyHashHeaders(r2, body+"tampered") {
		t.Fatal("tampered body accepted")
	}
}

func postForm(t *testing.T, url, body string) map[string]any {
	t.Helper()
	resp, err := http.Post(url, "application/x-www-form-urlencoded", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v (%d)", err, resp.StatusCode)
	}
	return out
}
