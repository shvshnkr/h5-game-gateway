package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"h5-game-gateway/internal/sign"
)

// providerClient talks to the game provider (operator-side integration):
// signed launch-game / provider-games requests, bounded by timeouts and
// small retries. In production this is where PG/JILI-style merchant
// endpoints sit.
type providerClient struct {
	base string
	http *http.Client
	cfg  Config
}

func newProviderClient(cfg Config) *providerClient {
	return &providerClient{
		base: cfg.ProviderBase,
		cfg:  cfg,
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

// post sends a signed request to the provider and decodes the envelope.
func (c *providerClient) post(ctx context.Context, methodWord, path string, extra map[string]any, out any) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(300 * time.Millisecond):
			}
		}
		rt := sign.Now()
		body := map[string]any{
			"operator_code": c.cfg.OperatorCode,
			"request_time":  rt,
			"sign":          sign.Compute(c.cfg.OperatorCode, rt, c.cfg.OperatorSecret, methodWord),
		}
		for k, v := range extra {
			body[k] = v
		}
		buf, _ := json.Marshal(body)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(buf))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("provider %s: %d %s", path, resp.StatusCode, string(raw))
			continue
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("provider %s: decode: %w (%s)", path, err, string(raw))
		}
		return nil
	}
	return lastErr
}

type providerGame struct {
	GameCode  string `json:"game_code"`
	Name      string `json:"name"`
	GameType  string `json:"game_type"`
	Thumbnail string `json:"thumbnail"`
}

type gamesEnvelope struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Games   []providerGame `json:"games"`
}

func (c *providerClient) Games(ctx context.Context) ([]providerGame, error) {
	var env gamesEnvelope
	if err := c.post(ctx, sign.MethodGames, "/api/operators/provider-games", nil, &env); err != nil {
		return nil, err
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("provider games: code %d %s", env.Code, env.Message)
	}
	return env.Games, nil
}

type launchEnvelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	URL     string `json:"url"`
}

func (c *providerClient) LaunchGame(ctx context.Context, memberAccount, gameCode, lobbyURL string) (string, error) {
	var env launchEnvelope
	err := c.post(ctx, sign.MethodLaunch, "/api/operators/launch-game", map[string]any{
		"member_account":      memberAccount,
		"game_code":           gameCode,
		"operator_lobby_url":  lobbyURL,
	}, &env)
	if err != nil {
		return "", err
	}
	if env.Code != 0 {
		return "", fmt.Errorf("provider launch: code %d %s", env.Code, env.Message)
	}
	return env.URL, nil
}
