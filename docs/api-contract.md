# Wire contract (demo, GSC+/PG-Soft-style)

Base: gateway serves `/seamless/*`; provider-mock serves `/api/operators/*`
and `/game/*`. All bodies are JSON.

## Signature

```
seamless callbacks (provider ↔ operator): sign = md5_hex(operator_code + request_time + secret_key + method_word)
payment webhook (processor ↔ operator):   sign = sha256_hex(operator_code + request_time + secret_key + "webhook")
```

- `request_time` — unix seconds as string; the server accepts ±5 min.
- `method_word` — one of: `balance`, `withdraw`, `deposit`, `pushbetdata`,
  `launch`, `games`, `webhook`.
- credentials for the demo stack: `operator_code=DEMO`,
  `secret_key=dev-operator-secret` (both sides; override via env).

## Operator endpoints (called by the provider)

### POST /seamless/balance
```json
{ "operator_code":"DEMO", "request_time":"1732500000", "sign":"…",
  "member_account":"alice" }
```
→ `{ "code":0, "message":"", "data":{ "member_account":"alice", "balance":12.5 } }`

### POST /seamless/withdraw
```json
{ "operator_code":"DEMO", "request_time":"…", "sign":"…",
  "member_account":"alice",
  "transactions":[{ "id":"prov-tx-1-bet", "action":"bet", "amount":0.1,
                    "currency":"USD", "round_id":"r-1", "game_code":"dice",
                    "wager_code":"r-1" }] }
```
→ `{ "code":0, "data":{ "balance":12.4 } }` · `1005` = insufficient funds.

### POST /seamless/deposit
Same shape, `action:"settled"`, positive amounts (payouts). Idempotent by `id`.

### POST /seamless/pushbetdata
Signed envelope + `payload` (round audit), stored raw in `bet_pushes`.

## Error codes

| code | meaning |
|-----|---------|
| 0 | OK |
| 1001 | INVALID_SIGN |
| 1002 | UNKNOWN_OPERATOR |
| 1003 | BAD_REQUEST / stale timestamp |
| 1004 | PLAYER_NOT_FOUND |
| 1005 | INSUFFICIENT_BALANCE |
| 2000 | INTERNAL |

## Provider endpoints (called by the gateway / player)

### POST /api/operators/provider-games (signed, method `games`)
→ `{ "code":0, "games":[{ "game_code":"dice", "name":"Lucky Dice",
     "game_type":"DICE", "thumbnail":"/game/static/dice.svg" }, …] }`

### POST /api/operators/launch-game (signed, method `launch`)
```json
{ "operator_code":"DEMO", "request_time":"…", "sign":"…",
  "member_account":"alice", "game_code":"dice", "operator_lobby_url":"…" }
```
→ `{ "code":0, "url":"/game/play?token=…" }` — embed in an iframe.

### POST /game/api/bet (session token)
```json
{ "token":"…", "amount":0.1, "client_seed":"my-seed",
  "win_under":5000 }      // dice
  "target":2.0 }          // crash
```
→ `{ "round_id","roll","nonce","win","multiplier","payout","balance","fairness" }`

### GET /game/api/fairness?token=…
→ reveals `server_seed` + hash + client_seed + nonce (post-hoc verification).

## Player API (gateway, JWT bearer)

| endpoint | body | note |
|---|---|---|
| `POST /api/auth/register` | `{username,password}` | bcrypt |
| `POST /api/auth/login` | `{username,password}` | → `{token}` |
| `GET /api/wallet` | — | `{balance,currency}` |
| `POST /api/wallet/deposit` | `{amount}` | opens pending deposit |
| `POST /api/wallet/webhook` | `{deposit_id,status,operator_code,request_time,sign}` | processor callback (mock), **sha256** signature |
| `GET /api/games` | — | provider catalog proxy |
| `POST /api/games/launch` | `{game_code}` | → `{url}` for iframe |
| `GET /api/admin/stats` | header `Authorization: Bearer ADMIN_TOKEN` | ledger aggregates + GGR |
