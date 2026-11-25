# PG Soft agent API (seamless wallet) — adapter contract

The gateway speaks **two provider dialects**; the active one is picked by
`PROVIDER_STYLE=gsc|pgsoft` (env). Both callback families are mounted
simultaneously, so a real provider can be wired without redeploying the
other side.

## Credentials (what a real PG merchant account provides)

| env | meaning |
|---|---|
| `PGSOFT_API_DOMAIN` | provider API base (in the demo: provider-mock) |
| `PGSOFT_OPERATOR_TOKEN` | agent account token |
| `PGSOFT_SECRET_KEY` | agent secret key |
| `PGSOFT_HASH_SALT` | salt for PWS-HMAC-SHA256 request signing (recommended in production) |
| `PGSOFT_CALLBACK_ALLOWED_IPS` | provider outgoing IP whitelist (empty = bypass) |

## Launch: GetLaunchURLHTML

```
POST {PGSOFT_API_DOMAIN}/external-game-launcher/api/v1/GetLaunchURLHTML?trace_id={GUID}
Content-Type: application/x-www-form-urlencoded

operator_token=…&path=/2024/index.html&extra_args=ops={session}&l=en
&url_type=game-entry&client_ip=…&sign={md5(sorted params + secret)}
```

Response: **HTML** (`text/html`) — the operator streams it into the
player's iframe. Our gateway stores it behind a one-time ticket and serves
`/pgsoft/play?ticket=…` (the ticket pattern mirrors production launch
flows).

## Wallet callbacks (provider → operator)

Form-urlencoded in, JSON out, **HTTP 200 always** (success and errors).

| PG portal name | demo route | meaning |
|---|---|---|
| VerifySession | `POST /pgsoft/verify-session` | session/player validation |
| Cash/Get (GetWallet) | `POST /pgsoft/wallet` | current balance |
| Cash/TransferInOut (Bet & Payout) | `POST /pgsoft/bet-payout` | **one call per round with the NET amount** |
| Cash/Adjustment | *(not in demo)* | manual balance correction |
| Update Bet Detail | *(not in demo)* | post-settlement bet details |

### Cash/Get

```
operator_token=…&player_name=…&player_session_id=…&currency=USD&sign=…
→ {"data":{"currency_amount":"30.0000","updated_time":1796000000},"error":null}
```

### Cash/TransferInOut

```
bet_amount=5.0000&win_amount=0.0000&transfer_amount=-5.0000
&real_transfer_amount=-5.0000&currency=USD&wallet_currency=USD
&bet_type=1&transfer_type=0&platform_type=web&game_id=2024
&round_id=…&bet_id=…&player_name=…&player_session_id=…
&updated_time=…&trace_id={GUID}&operator_token=…&sign=…
→ {"data":{"currency_amount":"25.0000","updated_time":…},"error":null}
```

Validations enforced by the gateway (per PG integration guide):

- `transfer_amount = win_amount − bet_amount` → else `1034`
- `trace_id` must be a GUID → else `1034`
- idempotency by `bet_id` — a replay returns the current balance without
  double-applying
- insufficient balance → `{"error":{"code":"3003","message":"Insufficient Balance"}}`
- all balance movements go through the same locked ledger as the GSC flow

Error codes: `1034` invalid request/sign/token, `3003` insufficient
balance, `2000` internal.

## Signature

Classic PG agent scheme (`internal/sign/pgsoft.go`):

```
sign = md5_hex( sorted("key=urlencoded(value)" joined "&", empty values and
        `sign` itself excluded ) + secret_key )
```

Auth accepts either the classic `sign` or the simplified agent scheme
(`secret_key` present in the body — timing-safe compared).

## PWS-HMAC-SHA256 request signing (optional, `PGSOFT_HASH_AUTH=true`)

```
x-date: 20261126
x-content-sha256: {sha256 hex of raw body}
authorization: PWS-HMAC-SHA256 Credential={x-date}/{operator_token}/pws/v1, SignedHeaders=host;x-content-sha256;x-date, Signature={hex}
Signature = hmac_sha256(PGSOFT_HASH_SALT, host + x-content-sha256 + x-date)
```

Implemented in `pgsoft.verifyHashHeaders`, covered by a unit test
(tampered body rejected).

## Dialect map (GSC+ ↔ PG Soft)

| concern | GSC+ style | PG Soft style |
|---|---|---|
| launch | `launch-game` → JSON `{url}` | `GetLaunchURLHTML` → HTML via ticket |
| balance | `seamless/balance` (JSON) | `pgsoft/wallet` (form → JSON) |
| bets | `seamless/withdraw` per bet + `seamless/deposit` per payout | single `pgsoft/bet-payout` per round (net) |
| signature | `md5(code+time+secret+method)` | `md5(sorted params + secret)` |
| amounts | float strings, 4 dp | float strings, 4 dp (`real_transfer_amount` for base-1000 currencies) |

## Going live with a real PG merchant account

1. Get `PGSOFT_API_DOMAIN` / `PGSOFT_OPERATOR_TOKEN` / `PGSOFT_SECRET_KEY`
   from PG (the demo never contains them).
2. Register callback URLs on your public gateway host (`/pgsoft/*`), pin
   PG's outgoing IPs in `PGSOFT_CALLBACK_ALLOWED_IPS`, enable
   `PGSOFT_HASH_AUTH`.
3. Switch `PROVIDER_STYLE=pgsoft` and point `PROVIDER_BASE` at the PG
   domain; the ledger, wallet and PWA sides stay untouched.
