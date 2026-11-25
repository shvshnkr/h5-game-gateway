package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

type authHandler struct {
	store *Store
	secret []byte
}

func (h *authHandler) register(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json"})
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if len(in.Username) < 3 || len(in.Password) < 6 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "username >= 3 chars, password >= 6 chars"})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "hash"})
		return
	}
	id, err := h.store.CreatePlayer(r.Context(), in.Username, string(hash), "USD")
	if err != nil {
		if errors.Is(err, ErrPlayerExists) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "username taken"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "create"})
		return
	}
	token, err := h.issueToken(id, in.Username)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "token"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "player_id": id, "username": in.Username})
}

func (h *authHandler) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json"})
		return
	}
	p, err := h.store.PlayerByName(r.Context(), strings.TrimSpace(in.Username))
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "wrong credentials"})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(p.PasswordHash), []byte(in.Password)) != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "wrong credentials"})
		return
	}
	token, err := h.issueToken(p.ID, p.Username)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "token"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "player_id": p.ID, "username": p.Username})
}

func (h *authHandler) issueToken(playerID int64, username string) (string, error) {
	claims := jwt.MapClaims{
		"sub":  playerID,
		"name": username,
		"exp":  time.Now().Add(24 * time.Hour).Unix(),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(h.secret)
}

// requireAuth is middleware validating the JWT and injecting player_id.
func requireAuth(secret []byte, next func(w http.ResponseWriter, r *http.Request, playerID int64)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hdr := r.Header.Get("Authorization")
		if !strings.HasPrefix(hdr, "Bearer ") {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "missing bearer token"})
			return
		}
		tok, err := jwt.Parse(strings.TrimPrefix(hdr, "Bearer "), func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, errors.New("bad signing method")
			}
			return secret, nil
		})
		if err != nil || !tok.Valid {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid token"})
			return
		}
		claims, ok := tok.Claims.(jwt.MapClaims)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid claims"})
			return
		}
		sub, ok := claims["sub"].(float64)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid sub"})
			return
		}
		next(w, r, int64(sub))
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
