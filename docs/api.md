# HTTP API design

Three audiences share one API surface, separated by credential type rather than by hostname:
the consumer app, merchant server-to-server integrations, and internal operations tools.

Base path `/v1`. Breaking changes get a new major path; additive fields do not.

## 1. Conventions

| | |
|---|---|
| Content type | `application/json` both directions |
| Money | integer minor units plus currency: `{"amount": 3200000, "currency": "IDR"}` — never a float, never a formatted string |
| Identifiers | prefixed ULIDs, stable and safe to log: `pay_01J7QF3K2M` |
| Timestamps | RFC 3339 UTC: `2026-09-13T09:12:04.118Z` |
| Enums | lowercase snake_case strings |
| Unknown fields | rejected with `422`, not ignored — a typo in a money field must never be silently dropped |

Object prefixes: `usr_`, `merch_`, `out_` (outlet), `pay_`, `trf_`, `tup_`, `wdr_`, `bil_`,
`ref_` (refund), `po_` (payout), `hb_` (holdback), `evt_`, `dsp_` (dispute), `led_`, `txn_`.

## 2. Authentication

| Audience | Credential | Header |
|---|---|---|
| Consumer app | access token `mp_at_`, 15 minutes; refresh token `mp_rt_`, 30 days, bound to a device | `Authorization: Bearer mp_at_…` |
| Merchant server | API key, `mp_live_` or `mp_test_` | `Authorization: Bearer mp_live_…` |
| Internal tools | SSO session plus a role claim | `Authorization: Bearer <token>` |

The prefix decides which middleware owns the credential, so the two consumer and merchant
paths never have to guess. That makes the prefixes part of the contract rather than cosmetic:
an `mp_at_` token presented to the merchant middleware is a bug, not a `401`.

Signing in is three calls:

```http
POST /v1/auth/otp      {"phone": "081200000001"}
  201 {"id": "otp_01J…", "expires_at": "2026-09-13T09:17:04Z"}

POST /v1/auth/token    {"challenge_id": "otp_01J…", "code": "418223",
                        "pin": "294715", "platform": "android", "model": "Pixel 9"}
  201 {"session_id": "sess_01J…", "device_id": "dev_01J…",
       "access_token": "mp_at_…", "refresh_token": "mp_rt_…",
       "access_expires_at": "…", "refresh_expires_at": "…", "new_device": true}

POST /v1/auth/refresh  {"refresh_token": "mp_rt_…"}
  201  a new pair; the one you sent stops working
```

Send `device_id` on a later login and the same device row is reused, which is what
`new_device` reports and what feeds the VR-02 velocity rule. The code is only marked spent once
the PIN has verified, so a mistyped PIN does not cost the user their SMS; three wrong PINs lock
entry for fifteen minutes.

Refresh tokens rotate. Presenting one that has already been rotated revokes every session
descended from it, because the server cannot distinguish a buggy client from a stolen token
being replayed, and the safe reading of the two is the same.

Only the code and the tokens' SHA-256 digests are stored. The PIN is argon2id. Nothing in the
database can be replayed as a credential.

Only the key **prefix** is stored in plaintext; the rest is hashed. The prefix is what maps a
key to a merchant without scanning the table, which makes it part of the schema rather than
decoration — renaming it later requires accepting both prefixes during a transition.

Keys in `mp_test_` mode reach `provider-sim` only and can never touch a live ledger account.
This is enforced at the service layer, not by a URL the caller chooses.

## 3. Idempotency

Required on every `POST` that moves money. Omitting it is `400`, not a default-to-unsafe.

```http
POST /v1/payments
Idempotency-Key: qr-8812-6f2a
```

| Situation | Response |
|---|---|
| First request | processed normally; the response is stored |
| Replay, same key, same body | `200` with the **original** response, byte for byte, same `id` |
| Replay, same key, different body | `422 idempotency_key_reused` |
| Replay while the first is still running | `409 idempotency_key_in_progress` with `Retry-After: 1` |

Keys are scoped per merchant or per user, so two callers cannot collide. Retention is 24
hours. The key table is a convenience, not a source of truth — the ledger is.

The different-body case is the one people skip. A client that reuses a key with a changed
amount has a bug, and silently replaying the old response hides it until it becomes a support
ticket about a missing payment.

## 4. Errors

One envelope, always:

```json
{
  "error": {
    "type": "insufficient_balance",
    "message": "Wallet balance is below the requested amount.",
    "request_id": "req_01J7QF3K2M",
    "details": { "available": 2480500, "required": 3200000, "currency": "IDR" }
  }
}
```

| Status | Meaning |
|---|---|
| `400` | malformed request, missing idempotency key |
| `401` / `403` | bad credential / valid credential without the scope |
| `404` | not found, or found but not visible to this caller |
| `409` | state conflict: already refunded, idempotent request in progress |
| `422` | semantically invalid: unknown field, key reuse with a different body |
| `429` | rate limited, with `Retry-After` |
| `500` | our fault |
| `503` | dependency down; safe to retry with the same idempotency key |

`request_id` appears in the response, the logs, and the Admin search box. Support should never
need to ask a customer for a screenshot.

## 5. Pagination and rate limits

Cursor-based, never offset — offset pagination on an append-heavy table skips and repeats rows
as new ones arrive.

```http
GET /v1/payments?limit=50&starting_after=pay_01J7QF3K2M
```

```json
{ "data": [ … ], "has_more": true, "next_cursor": "pay_01J7QDK81N" }
```

Limits are per credential and returned on every response as `RateLimit-Limit`,
`RateLimit-Remaining`, `RateLimit-Reset`. Counters live in Redis.

## 6. Consumer endpoints

| Method | Path | Notes |
|---|---|---|
| `POST` | `/v1/auth/otp` · `/v1/auth/token` | phone code, then PIN; three wrong PINs lock entry for 15 minutes |
| `POST` | `/v1/auth/refresh` · `/v1/auth/logout` | rotation with replay detection; logout ends one session |
| `GET` | `/v1/devices` · `POST /v1/devices/{id}/revoke` | revoking a device signs out every session on it |
| `GET` | `/v1/me` | profile, KYC tier, limits, usage |
| `POST` | `/v1/me/kyc` | submit for review; returns `pending` |
| `GET` | `/v1/balance` | `available`, `held`, `currency` |
| `GET` | `/v1/transactions` | unified history across all operation types |
| `POST` | `/v1/topups` | returns VA number and instructions; balance moves on provider callback |
| `POST` | `/v1/transfers` | P2P, requires PIN confirmation token |
| `POST` | `/v1/payments` | QRIS or payment link |
| `POST` | `/v1/withdrawals` | asynchronous, may fail at the bank |
| `GET` | `/v1/billers` · `POST /v1/billers/{code}/inquire` | inquire before pay; returns the amount due |
| `POST` | `/v1/bill-payments` | can land in `pending` |
| `POST` | `/v1/money-requests` · `POST /v1/bill-splits` | requests, not transactions |
| `GET` | `/v1/points` · `POST /v1/points/redeem` | separate ledger from money |
| `GET` | `/v1/promos` · `POST /v1/promos/{id}/activate` | quota decremented atomically in Redis |
| `GET` | `/v1/notifications` | inbox |

### Creating a payment

```json
POST /v1/payments
{
  "merchant_id": "merch_8812",
  "outlet_id": "out_0912",
  "method": "qris",
  "amount": 3200000,
  "currency": "IDR",
  "pin_token": "pintok_01J7QF2X…"
}
```

```json
201 Created
{
  "id": "pay_01J7QF3K2M",
  "status": "succeeded",
  "amount": 3200000,
  "fee": 22400,
  "currency": "IDR",
  "merchant": { "id": "merch_8812", "name": "Kopi Tuku Senopati" },
  "ledger_transaction_id": "txn_01J7QF3K2M",
  "balance_after": 2480500,
  "created_at": "2026-09-13T09:12:04.118Z"
}
```

`ledger_transaction_id` is exposed deliberately. It is the handle a support agent uses to pull
the exact entries that moved, and it makes the API auditable from the outside.

## 7. Merchant endpoints

| Method | Path | Notes |
|---|---|---|
| `POST` | `/v1/charges` | payment link or dynamic QRIS with an expiry |
| `GET` | `/v1/payments` · `/v1/payments/{id}` | filter by status, method, outlet, date |
| `POST` | `/v1/refunds` | full or partial; new ledger entries, never a delete |
| `GET` | `/v1/payouts` · `/v1/payouts/{id}` | includes `mode`, `rail`, `latency_ms` |
| `GET` | `/v1/payouts/config` | current holdback rate and the factors behind it |
| `PUT` | `/v1/payouts/config` | switch between `instant` and `batch` |
| `GET` | `/v1/settlements` | batch history |
| `GET` `POST` | `/v1/outlets` · `/v1/outlets/{id}/staff` | multi-outlet and cashier roles |
| `GET` `POST` `DELETE` | `/v1/api-keys` | the secret is returned exactly once |
| `GET` `PUT` | `/v1/webhook-endpoints` | URL, subscribed events, signing secret |
| `GET` | `/v1/webhook-deliveries` | attempt log, response codes, DLQ |
| `GET` | `/v1/volume` | payments per hour for the last 24 hours, always 24 buckets |
| `GET` `POST` | `/v1/charges` · `POST /v1/charges/{id}/cancel` | payment links with an amount and an expiry |
| `POST` | `/v1/webhook-deliveries/{id}/retry` | manual replay |

### A payment link is paid by id, not by amount

```http
POST /v1/charges          {"description": "Table 4", "amount": 4800000, "expires_in": "2h"}
  201 {"id": "chg_01J…", "reference": "ref_01J…", "status": "open", "expires_at": "…"}

GET  /v1/charges/chg_01J…                      the payer reads what they are about to pay

POST /v1/payments         {"charge_id": "chg_01J…"}
  201 a normal payment; the amount came from the link, not from the client
```

The payer never sends an amount. `POST /v1/payments` with a `charge_id` resolves the merchant,
outlet and amount from the link, which is what stops a client paying one rupiah against a
million-rupiah invoice. The link is marked paid inside the same transaction as the ledger
posting and behind a row lock, so concurrent attempts produce exactly one payment and the rest
get `409`.

### Payout status reflects the differentiator

```json
GET /v1/payouts/po_01J7QF88
{
  "id": "po_01J7QF88",
  "payment_id": "pay_01J7QF3K2M",
  "mode": "instant",
  "gross": 3177600,
  "holdback": 127104,
  "net": 3050496,
  "holdback_bps": 400,
  "holdback_release_at": "2026-09-14T09:12:05Z",
  "rail": "bifast",
  "status": "settled",
  "latency_ms": 2900,
  "settled_at": "2026-09-13T09:12:07.018Z"
}
```

`latency_ms` is part of the public contract because the payout SLA is a product promise. A
merchant can hold us to it without asking for a report.

When platform float crosses its threshold, `mode` comes back as `batch` and `status` as
`degraded_to_batch`. The API says plainly what happened rather than quietly delaying.

## 8. Webhooks

Delivery is **at-least-once**. Exactly-once across a network boundary does not exist, so the
contract is explicit: receivers must be idempotent on `event.id`.

```json
POST https://merchant.example/hooks/marspay
Marspay-Signature: t=1789254724,v1=5f2a…
{
  "id": "evt_9K2M4Q",
  "type": "payment.succeeded",
  "created_at": "2026-09-13T09:12:05.003Z",
  "data": { "object": "payment", "id": "pay_01J7QF3K2M", … }
}
```

Signature: `HMAC-SHA256(secret, "<timestamp>.<raw body>")`. Receivers must compare in constant
time and reject timestamps older than five minutes, which is what stops a captured request
being replayed later.

| | |
|---|---|
| Timeout | 5 s; anything slower counts as a failure |
| Retry schedule | 2 s, 4 s, 8 s, 30 s, 2 m, 10 m, 1 h, 6 h |
| After 9 attempts | one first try plus eight retries, then dead letter: visible in the dashboard, manual replay only |
| Ordering | not guaranteed — use `created_at`, not arrival order |

The signing secret is stored **recoverably**, not hashed. A password can be hashed because
verification only needs a comparison; a signing secret cannot, because we have to recompute the
HMAC on every send. In production it belongs behind a KMS-managed key, encrypted at rest.
Conflating the two is an easy mistake to make in a schema and impossible to work around later.

Events: `payment.succeeded`, `payment.failed`, `payment.pending`, `refund.created`,
`payout.settled`, `payout.failed`, `payout.degraded`, `settlement.paid`, `dispute.opened`,
`holdback.released`.

## 9. Inbound provider callbacks

`POST /v1/callbacks/{provider_code}` receives events from banks, the QRIS switch and billers —
in this project, from `provider-sim`. Every callback is written to `provider_callbacks` **before**
it is processed, including ones that fail signature verification.

That ordering matters. A callback that arrives, fails to process, and is never recorded is
indistinguishable from one that never arrived. Recording first is what lets the reconciler tell
those two cases apart the next morning.

Uniqueness is `(provider_code, external_ref, event_type)`, so duplicates are absorbed at the
database layer rather than by application logic that has to remember to check.

## 10. Internal endpoints

Under `/internal/v1`, never exposed publicly, every mutation requiring a written `reason` that
is enforced at the API layer rather than merely prompted for in the UI.

| Path | Purpose |
|---|---|
| `/search` | one box: user, merchant, payment, event, batch, bank reference |
| `/payments/{id}/ledger` | the entries, with the balanced-to-zero footer |
| `/payments/{id}/reversal` | manual reversal, reason required |
| `/reconciliation/runs` · `/discrepancies/{id}/resolve` | daily run and its unmatched rows |
| `/float` · `/float/exposure` | treasury position and per-merchant exposure |
| `/payouts/engine` | latency percentiles, rail health, failed payouts |
| `/merchants/{id}/score` | risk score with per-factor components |
| `/accounts/{id}/block` · `/unblock` | reason required |
| `/audit` | append-only, readable by everyone, writable by no one |
| `/queues` | outbox depth, webhook retries, money in flight, and the scheduled work that is due |
| `/transitions` | every recorded status change, by operation or by kind |
| `/velocity/rules` · `/velocity/alerts` | the rule catalogue with its trip counts, and what tripped |
| `/accounts/{id}` | the ledger behind any account, with its balance |
| `/saturation` | connection pool counters and goroutine count, for load work |

`/saturation` is the only one that returns no business data. It exposes what the pgx pool and
the admission limiter are doing — how many acquisitions found an empty pool, how long callers
spent waiting, how many requests were refused and why — so a load test can name the resource
that queued instead of inferring it from latency. It reads counters and holds no lock, so it
stays answerable while the pool is exhausted, which is exactly when it is worth asking. It and
`/healthz` are the only two endpoints admission control never refuses.

### Overload

Any endpoint may answer `503 service_overloaded` when the API is past its bounded queue:

```json
{"error":{"type":"service_overloaded",
          "message":"The service is at capacity.",
          "request_id":"req_01M..."}}
```

It carries `Retry-After`. The request was **not started** — no ledger entry, no idempotency
key claimed — so retrying with the same `Idempotency-Key` is both safe and expected. Honouring
the header matters: measurements in the README show that a caller which retries immediately
turns load shedding from a latency improvement into a throughput loss.

## 11. What the API deliberately does not offer

No endpoint returns or accepts a balance as a writable value. No endpoint deletes a
transaction. No endpoint lets a caller choose which ledger accounts to post to — that mapping
lives in the service layer, because an API that accepts arbitrary postings is an API that can
be used to invent money.
