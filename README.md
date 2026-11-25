# h5-game-gateway

H5 mobile game platform gateway with a PG-Soft-style seamless-wallet provider integration — a complete, runnable demo of the exact integration pattern used by slot aggregators (operator_code + secret_key, MD5-signed requests, seamless wallet callbacks, game launch URLs).

**This is an educational mock.** No real money, no real game-provider accounts, no gambling license involved. The provider is simulated in-process (`provider-mock`).

## What it demonstrates

| Piece | What's inside |
|---|---|
| **Seamless wallet (operator side)** | Signed callbacks a game provider calls on every bet/settle: `balance`, `withdraw` (bets), `deposit` (payouts), `pushbetdata` — with signature verification, idempotent transactions (unique provider tx ids), row-locked wallet updates and a double-entry ledger |
| **Provider integration client** | Signed `launch-game` / `provider-games` calls returning H5 game URLs, retries and timeouts |
| **Provider mock** | A fake PG-style provider: game catalog, session tokens, provably-fair RNG (HMAC-SHA256 server seed / client seed / nonce, seed hash published before play), two playable H5 mini-games (Lucky Dice, Mini Crash) |
| **Player wallet & payments** | Register/login (JWT + bcrypt), balance, deposit via a mock crypto-payment webhook (the Bitcart-style pattern), withdraw |
| **PWA shell** | manifest + service worker → installs to the phone desktop, lobby, iframe game launch |
| **Ops** | Docker Compose (gateway, provider-mock, MySQL 8, Redis 7, Nginx), SQL migrations, Makefile, CI, unit + integration tests |

## Architecture

```
                     ┌────────────────────────────────────────────┐
                     │                  Nginx :8080               │
                     │  /       → web/ (PWA static)               │
  Phone / PWA ───────►  /api/    → gateway :8081                  │
                     │  /game/  → provider-mock :8082 (H5 games)  │
                     └────────────────────────────────────────────┘
                           │                        │
                    ┌──────▼──────┐          ┌──────▼───────┐
                    │  gateway    │          │ provider-mock│
                    │ Go, JWT     │◄─────────│ Go, provably │
                    │ wallet,     │ seamless │ fair RNG,    │
                    │ ledger      │ wallet   │ dice/crash   │
                    └──┬───────┬──┘  calls   └──────────────┘
                       │       │
                ┌──────▼──┐ ┌──▼───┐
                │ MySQL 8 │ │Redis │
                └─────────┘ └──────┘
```

Full sequence diagrams: [docs/architecture.md](docs/architecture.md). Wire contract: [docs/api-contract.md](docs/api-contract.md).

## Quick start

```bash
make up          # docker compose: mysql, redis, gateway, provider-mock, nginx
make migrate     # apply SQL migrations
make smoke       # end-to-end: register → deposit → launch → play a round
open http://localhost:18081
```

> Port note: nginx is published as `127.0.0.1:18081:80` — IPv4-only. Docker
> Desktop's IPv6 loopback forwarding is broken on some Windows hosts (a
> dllhost.exe shadow listener resets `[::1]` connections), which makes
> `localhost` fail intermittently in browsers if the port is published
> dual-stack. IPv4-only binding + Chromium's automatic IPv4 fallback avoids
> that.

Then open the site on a phone (or Chrome DevTools mobile view) → "Add to home screen".

## The seamless wallet contract (short version)

The provider holds `operator_code` + `secret_key`. Every request is signed:

```
sign = md5_hex(operator_code + request_time + secret_key + method_word)
```

The provider calls the operator on every round:

| Endpoint | Meaning |
|---|---|
| `POST /seamless/balance` | get player balance |
| `POST /seamless/withdraw` | debit bets (`action: "bet"`) |
| `POST /seamless/deposit` | credit payouts (`action: "settled"`) |
| `POST /seamless/pushbetdata` | round audit push |

Every transaction carries a provider-side unique `id`; the operator ledger enforces idempotency with a unique index, replays return the current balance with `code: 0`.

Error codes: `0 OK`, `1001 INVALID_SIGN`, `1002 UNKNOWN_OPERATOR`, `1003 BAD_REQUEST`, `1004 PLAYER_NOT_FOUND`, `1005 INSUFFICIENT_BALANCE`, `2000 INTERNAL`.

## Layout

```
cmd/gateway         operator service entry point
cmd/provider-mock   fake game provider entry point
cmd/migrate         tiny SQL migration runner
internal/gateway    auth, wallet, ledger, seamless callbacks, provider client, admin
internal/provider   provider mock: sessions, provably fair dice/crash
internal/sign       the MD5 signing scheme
internal/money      int64 minor units (scale 1e4)
migrations/         SQL schema
web/                PWA shell (vanilla JS)
deploy/             nginx.conf
```

## Tests

```bash
make test           # unit tests, no infra needed
make test-integration   # needs docker compose mysql/redis up
```

## License

MIT — see [LICENSE](LICENSE).
