#!/usr/bin/env bash
# End-to-end smoke: register → deposit → games → launch → bet → stats.
# Assumes the compose stack is up (make up) and nginx listens on :8080.
set -euo pipefail

BASE="${BASE:-http://localhost:8080}"
SECRET="${OPERATOR_SECRET:-dev-operator-secret}"
USER="smoke-$(date +%s)"

say() { printf '\n\033[1;34m== %s\033[0m\n' "$*"; }

md5() { printf '%s' "$1" | md5sum | cut -d' ' -f1; }

say "1/6 health"
curl -fsS "$BASE/healthz"; echo

say "2/6 register $USER"
TOKEN=$(curl -fsS -X POST "$BASE/api/auth/register" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$USER\",\"password\":\"smoke-pass-1\"}" \
  | python -c 'import sys,json;print(json.load(sys.stdin)["token"])')
echo "token ok (${#TOKEN} chars)"

say "3/6 deposit 10.00 (mock webhook)"
DEP=$(curl -fsS -X POST "$BASE/api/wallet/deposit" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"amount":10.0}')
DEP_ID=$(echo "$DEP" | python -c 'import sys,json;print(json.load(sys.stdin)["deposit_id"])')
RT=$(date +%s)
SIGN=$(printf '%s' "DEMO${RT}${SECRET}webhook" | sha256sum | cut -d' ' -f1)
curl -fsS -X POST "$BASE/api/wallet/webhook" -H 'Content-Type: application/json' \
  -d "{\"deposit_id\":$DEP_ID,\"status\":\"confirmed\",\"operator_code\":\"DEMO\",\"request_time\":\"$RT\",\"sign\":\"$SIGN\"}"; echo

say "4/6 games catalog"
curl -fsS "$BASE/api/games" -H "Authorization: Bearer $TOKEN"; echo

say "5/6 launch dice + play 3 rounds"
URL=$(curl -fsS -X POST "$BASE/api/games/launch" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"game_code":"dice"}' | python -c 'import sys,json;print(json.load(sys.stdin)["url"])')
echo "play url: $URL"
TOKEN_GAME="${URL##*token=}"
for i in 1 2 3; do
  curl -fsS -X POST "$BASE/game/api/bet" -H 'Content-Type: application/json' \
    -d "{\"token\":\"$TOKEN_GAME\",\"amount\":0.5,\"client_seed\":\"smoke-$i\",\"win_under\":4500}" | head -c 400; echo
done

say "6/6 admin stats"
curl -fsS "$BASE/api/admin/stats" -H "Authorization: Bearer dev-admin-token" | head -c 600; echo

say "SMOKE OK"
