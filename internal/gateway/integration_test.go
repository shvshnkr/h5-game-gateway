package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"h5-game-gateway/internal/sign"
)

// integrationStore connects to the dockerized MySQL (TEST_MYSQL_DSN),
// recreates the schema and returns a clean Store. Skipped when the var is
// unset, so `go test ./...` works without infra.
func integrationStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN not set; integration test skipped")
	}
	s, err := OpenStore(dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	schema, err := os.ReadFile("../../migrations/0001_init.up.sql")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	if _, err := s.DB.Exec(string(schema)); err != nil {
		if !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("apply schema: %v", err)
		}
	}
	_, _ = s.DB.Exec(`DELETE FROM bet_pushes; DELETE FROM deposits; DELETE FROM ledger_entries;
	                  DELETE FROM wallets; DELETE FROM players;`)
	t.Cleanup(func() { _ = s.DB.Close() })
	return s
}

func registerPlayer(t *testing.T, s *Store, name string) int64 {
	t.Helper()
	id, err := s.CreatePlayer(context.Background(), name, "not-a-real-hash", "USD")
	if err != nil {
		t.Fatalf("create player: %v", err)
	}
	return id
}

func TestIntegrationMoveMoneyIdempotentAndGuarded(t *testing.T) {
	s := integrationStore(t)
	ctx := context.Background()
	id := registerPlayer(t, s, "alice")

	// deposit 10.00
	if _, err := s.MoveMoney(ctx, id, "payment", "d-1", "deposit", "", moneyUnit(10)); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	// replay must be flagged as duplicate
	if _, err := s.MoveMoney(ctx, id, "payment", "d-1", "deposit", "", moneyUnit(10)); !isDup(err) {
		t.Fatalf("replay not detected as duplicate: %v", err)
	}

	// bet 4.00 → balance 6.00
	b, err := s.MoveMoney(ctx, id, "provider", "tx-1", "bet", "r1", -moneyUnit(4))
	if err != nil {
		t.Fatalf("bet: %v", err)
	}
	if b != moneyUnit(6) {
		t.Fatalf("balance after bet = %d, want %d", b, moneyUnit(6))
	}

	// overdraft must fail
	if _, err := s.MoveMoney(ctx, id, "provider", "tx-2", "bet", "r2", -moneyUnit(100)); !isInsufficient(err) {
		t.Fatalf("overdraft not rejected: %v", err)
	}

	// provider tx replay (new scope entry, same tx id) → duplicate
	if _, err := s.MoveMoney(ctx, id, "provider", "tx-1", "bet", "r1", -moneyUnit(4)); !isDup(err) {
		t.Fatalf("provider replay not detected: %v", err)
	}
}

func TestIntegrationConcurrentBetsNoDoubleSpend(t *testing.T) {
	s := integrationStore(t)
	ctx := context.Background()
	id := registerPlayer(t, s, "bob")

	if _, err := s.MoveMoney(ctx, id, "payment", "d-0", "deposit", "", moneyUnit(100)); err != nil {
		t.Fatalf("deposit: %v", err)
	}

	// 20 concurrent bets of 10.00 with only 100.00 in the wallet:
	// exactly 10 must succeed, the rest must report insufficient funds —
	// and the final balance must be exactly 0.
	const workers = 20
	results := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.MoveMoney(ctx, id, "provider", fmt.Sprintf("c-tx-%d", i), "bet", fmt.Sprintf("cr-%d", i), -moneyUnit(10))
			results[i] = err
		}(i)
	}
	wg.Wait()

	oks, insuff := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			oks++
		case isInsufficient(err):
			insuff++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if oks != 10 || insuff != 10 {
		t.Fatalf("oks=%d insuff=%d, want 10/10", oks, insuff)
	}
	w, err := s.WalletByPlayerID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if w.Balance != 0 {
		t.Fatalf("final balance = %d, want 0", w.Balance)
	}
}

func TestIntegrationSeamlessEndToEnd(t *testing.T) {
	s := integrationStore(t)
	cfg := LoadConfig()
	cfg.MySQLDSN = "unused"

	id := registerPlayer(t, s, "carol")
	if _, err := s.MoveMoney(context.Background(), id, "payment", "d-2", "deposit", "", moneyUnit(50)); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(NewServer(cfg, s).Handler())
	defer srv.Close()

	// 1. signed balance probe
	body := signedBody(cfg, "balance", map[string]any{"member_account": "carol"})
	resp := postJSON(t, srv.URL+"/seamless/balance", body)
	if resp["code"].(float64) != 0 || resp["data"].(map[string]any)["balance"].(float64) != 50 {
		t.Fatalf("balance probe: %v", resp)
	}

	// 2. signed withdraw 10.00
	body = signedBody(cfg, "withdraw", map[string]any{
		"member_account": "carol",
		"transactions": []map[string]any{
			{"id": "e2e-bet-1", "action": "bet", "amount": 10, "currency": "USD",
				"round_id": "r-1", "game_code": "dice", "wager_code": "r-1"},
		},
	})
	resp = postJSON(t, srv.URL+"/seamless/withdraw", body)
	if resp["code"].(float64) != 0 || resp["data"].(map[string]any)["balance"].(float64) != 40 {
		t.Fatalf("withdraw: %v", resp)
	}

	// 3. replay the same tx id → idempotent success, balance unchanged
	resp = postJSON(t, srv.URL+"/seamless/withdraw", body)
	if resp["code"].(float64) != 0 || resp["data"].(map[string]any)["balance"].(float64) != 40 {
		t.Fatalf("withdraw replay: %v", resp)
	}

	// 4. bad signature must be rejected
	body["sign"] = "badsign"
	resp = postJSON(t, srv.URL+"/seamless/withdraw", body)
	if resp["code"].(float64) != 1001 {
		t.Fatalf("bad sign accepted: %v", resp)
	}
}

// --- helpers ---

func moneyUnit(v float64) int64 { return int64(v * 10000) }

func isDup(err error) bool      { return err != nil && err.Error() == ErrDuplicate.Error() }
func isInsufficient(err error) bool { return err != nil && err.Error() == ErrInsufficient.Error() }

func signedBody(cfg Config, method string, extra map[string]any) map[string]any {
	rt := sign.Now()
	body := map[string]any{
		"operator_code": cfg.OperatorCode,
		"request_time":  rt,
		"sign":          sign.Compute(cfg.OperatorCode, rt, cfg.OperatorSecret, method),
	}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func postJSON(t *testing.T, url string, body map[string]any) map[string]any {
	t.Helper()
	buf, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", strings.NewReader(string(buf)))
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}
