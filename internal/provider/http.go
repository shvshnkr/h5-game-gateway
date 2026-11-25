package provider

import (
	"html/template"
	"net/http"
)

// Handler wires the provider routes: operator-facing signed API, the H5
// play pages and static assets. The play page is a tiny embedded template
// — in a real provider these are big H5 game bundles; the contract
// (token in URL, iframe-embeddable) is the same.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// operator-facing, signed
	mux.HandleFunc("POST /api/operators/provider-games", s.games)
	mux.HandleFunc("POST /api/operators/launch-game", s.launch)

	// PG Soft agent API (operator-facing): GetLaunchURLHTML + GameList
	mux.HandleFunc("POST /pgsoft/external-game-launcher/api/v1/GetLaunchURLHTML", s.pgGetLaunchURLHTML)
	mux.HandleFunc("POST /pgsoft/api/v1/GameList", s.pgGameList)

	// player-facing, session token
	mux.HandleFunc("POST /game/api/bet", s.bet)
	mux.HandleFunc("GET /game/api/fairness", s.fairness)
	mux.HandleFunc("GET /game/play", s.play)
	mux.HandleFunc("GET /game/healthz", s.health)

	// static: mini thumbnails + shared js
	mux.HandleFunc("GET /game/static/", s.static)

	return mux
}

var playTmpl = template.Must(template.New("play").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, user-scalable=no">
<title>{{.GameCode}} — h5-game-gateway demo</title>
<style>
  body { font-family: system-ui, sans-serif; background:#10141c; color:#e8eaf0;
         margin:0; display:flex; flex-direction:column; align-items:center; min-height:100vh; }
  h1 { font-size:1.1rem; } .card { background:#1a2130; border-radius:12px; padding:16px;
        margin:8px; width:min(92vw,420px); box-sizing:border-box; }
  input { width:100%; box-sizing:border-box; padding:10px; margin:4px 0; border-radius:8px;
          border:1px solid #2b3550; background:#0d1118; color:#e8eaf0; }
  button { width:100%; padding:12px; margin-top:8px; border:0; border-radius:8px;
           background:#3b6cff; color:#fff; font-size:1rem; }
  .res { margin-top:10px; padding:10px; border-radius:8px; background:#0d1118; min-height:1.2em; }
  .win { color:#4ade80; } .lose { color:#f87171; }
  small { color:#8b93a7; }
</style>
</head>
<body>
<h1>{{.GameCode}} — provably fair demo</h1>
<div class="card">
  <small>server_seed_hash: {{.ServerSeedHash}}</small>
  <label>amount (USD)</label><input id="amount" value="0.10">
  {{if eq .GameCode "dice"}}
  <label>win if roll &lt; (1–9999)</label><input id="win_under" value="5000">
  {{else}}
  <label>auto-cashout multiplier (≥1.01)</label><input id="target" value="2.00">
  {{end}}
  <label>client seed</label><input id="client_seed" value="my-seed">
  <button onclick="play()">PLAY ROUND</button>
  <div class="res" id="res">—</div>
  <small id="fair"></small>
</div>
<script>
async function play() {
  const body = { token: {{.Token}}, amount: parseFloat(document.getElementById('amount').value),
                 client_seed: document.getElementById('client_seed').value
                 {{if eq .GameCode "dice"}}, win_under: parseInt(document.getElementById('win_under').value||"5000")
                 {{else}}, target: parseFloat(document.getElementById('target').value||"2")
                 {{end}} };
  const r = await fetch('/game/api/bet', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify(body)});
  const j = await r.json();
  const el = document.getElementById('res');
  if (j.error) { el.innerHTML = '⛔ ' + j.error + ' ' + (j.operator_message||''); el.className='res lose'; return; }
  el.innerHTML = (j.win ? '✅ WIN ' + j.payout.toFixed(4) : '❌ lose') +
                 ' — roll ' + j.roll + ' (x' + j.multiplier + ') — balance ' + j.balance.toFixed(4);
  el.className = 'res ' + (j.win ? 'win' : 'lose');
}
</script>
</body>
</html>
`))

func (s *Server) play(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	s.mu.Lock()
	sess := s.sess[token]
	s.mu.Unlock()
	if sess == nil {
		http.Error(w, "unknown session token", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = playTmpl.Execute(w, map[string]string{
		"Token":           sess.Token,
		"GameCode":        sess.GameCode,
		"ServerSeedHash":  sess.Fairness.ServerSeedHash,
	})
}

var statics = map[string]string{
	"dice.svg": `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="12" fill="#1a2130"/><circle cx="20" cy="20" r="6" fill="#3b6cff"/><circle cx="44" cy="20" r="6" fill="#e8eaf0"/><circle cx="32" cy="32" r="6" fill="#4ade80"/><circle cx="20" cy="44" r="6" fill="#e8eaf0"/><circle cx="44" cy="44" r="6" fill="#3b6cff"/></svg>`,
	"crash.svg": `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="12" fill="#1a2130"/><polyline points="8,52 24,40 36,44 52,16" fill="none" stroke="#4ade80" stroke-width="4" stroke-linecap="round"/><circle cx="52" cy="16" r="5" fill="#f87171"/></svg>`,
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Path[len("/game/static/"):]
	svg, ok := statics[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write([]byte(svg))
}
