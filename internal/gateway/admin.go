package gateway

import (
	"net/http"

	"h5-game-gateway/internal/money"
)

// admin exposes minimal operational stats, guarded by a static bearer
// token (ADMIN_TOKEN). In production this would be an authed dashboard.
type admin struct {
	store *Store
	token string
}

func (a *admin) stats(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+a.token {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid admin token"})
		return
	}
	rows, err := a.store.Stats(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	var totalBets, totalWins, totalDeposits int64
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{
			"entry_type": row.EntryType,
			"count":      row.Count,
			"sum":        money.ToFloat(row.Sum),
		})
		switch row.EntryType {
		case "bet":
			totalBets = row.Sum
		case "win":
			totalWins = row.Sum
		case "deposit":
			totalDeposits = row.Sum
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries":        out,
		"ggr":            money.ToFloat(-(totalBets + totalWins)), // wins are positive payouts; bets are negative
		"total_deposits": money.ToFloat(totalDeposits),
	})
}
