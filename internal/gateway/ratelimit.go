package gateway

import (
	"context"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
)

// rateLimiter is a tiny fixed-window limiter backed by Redis (INCR+EXPIRE).
// It guards the auth endpoints from credential stuffing. In a multi-node
// deployment Redis is what makes the limit global.
type rateLimiter struct {
	rdb    *redis.Client
	limit  int
	window time.Duration
	prefix string
}

func newRateLimiter(rdb *redis.Client, limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{rdb: rdb, limit: limit, window: window, prefix: "rl"}
}

// Middleware limits per client IP. On Redis failure it fails open
// (availability beats the limiter for a demo) and logs via the next handler.
func (l *rateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		key := l.prefix + ":" + ip
		ctx, cancel := context.WithTimeout(r.Context(), 300*time.Millisecond)
		defer cancel()
		n, err := l.rdb.Incr(ctx, key).Result()
		if err == nil {
			if n == 1 {
				l.rdb.Expire(ctx, key, l.window)
			}
			if n > int64(l.limit) {
				writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "too many requests"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	// nginx sets X-Forwarded-For; fall back to RemoteAddr
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		// first hop
		for i := 0; i < len(v); i++ {
			if v[i] == ',' {
				return trimSpaces(v[:i])
			}
		}
		return trimSpaces(v)
	}
	host := r.RemoteAddr
	for i := 0; i < len(host); i++ {
		if host[i] == ':' {
			return host[:i]
		}
	}
	return host
}

func trimSpaces(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
