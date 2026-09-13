# Tech stack and how a request flows

`ARCHITECTURE.md` describes the system as designed. This document describes what is actually
running: which processes exist, what each piece of the stack was chosen for, and the exact path
a request takes through the code. Where the two disagree, this one is right, and §6 lists every
place they disagree on purpose.

## 1. What runs

Two deployables. They share nothing but HTTP.

```
┌──────────────────────────┐        ┌──────────────────────────┐
│  web  ·  Next.js 16      │        │  api  ·  Go 1.26         │
│  node server.js          │        │  cmd/marspay             │
│  :3000                   │        │  :8080                   │
│                          │        │                          │
│  React 19, TypeScript 5  │───────▶│  net/http, no framework  │
│  tokens in sessionStorage│  HTTPS │  70 routes               │
└──────────────────────────┘        └───────────┬──────────────┘
                                                │
                          ┌─────────────────────┼─────────────────────┐
                          ▼                     ▼                     ▼
                  ┌───────────────┐    ┌────────────────┐    ┌───────────────┐
                  │ PostgreSQL 15 │    │ Redis 8        │    │ Redpanda      │
                  │ the truth     │    │ the fast path  │    │ Kafka API     │
                  └───────────────┘    └────────────────┘    └───────────────┘
```

`next build` with `output: "standalone"` produces `.next/standalone/server.js` carrying its own
`node_modules`. `go build ./cmd/marspay` produces a single static binary. Neither needs the
repository to run, and deploying one does not deploy the other.

The interface calls the API from the browser, so the API carries a CORS allowlist
(`MARSPAY_CORS_ORIGINS`) and the Next server never sits in the data path. That is a deliberate
choice with a known cost — see §6.

## 2. The stack, and why each piece

| Layer | Choice | Why this one |
|---|---|---|
| API language | Go 1.26 | one static binary, no runtime to install, goroutines make the concurrency tests cheap to write |
| HTTP | `net/http` and `http.ServeMux` | Go 1.22's mux does method and path patterns; a framework would add indirection over 70 routes and nothing else |
| Postgres driver | `pgx/v5` | native protocol rather than `database/sql`, which is what makes `pgxpool.Stat()` able to report why requests queue |
| Cache and counters | `go-redis/v9` | Lua scripting for the atomic balance reservation; a check-then-decrement in two round trips is a race |
| Kafka client | `franz-go` | pure Go, no CGo, no librdkafka in the build |
| Password hashing | `golang.org/x/crypto/argon2` | the only dependency added for the PIN, and the only one that needs to be slow |
| Interface | Next.js 16, React 19, TypeScript 5 | App Router for file-based routes per role; TypeScript because the response shapes are the contract |
| Styling | one hand-written stylesheet | ported unchanged from the prototype; no utility framework, no CSS-in-JS, no build step beyond Next's |

The Go module has **four direct dependencies**: the three drivers above and `x/crypto` for
argon2. Everything else — idempotency, the ledger, velocity rules, admission control, the
outbox, session handling — is in `internal/`, because each one is a design decision this project
exists to show rather than a library call to hide.

## 3. A payment, through the actual code

`POST /v1/payments` with an `Idempotency-Key`. Middleware runs outermost first:

```
CORS                       allowlist check; preflight answered and stopped here
  WithRequestID            mints req_…, echoed in the response and every log line
    admission              bounded in-flight + queue; past both, 503 + Retry-After
      auth.Bearer          mp_at_ token → Redis, then Postgres; sets user, device, role
        merchant.APIKeyAuth  mp_live_/mp_test_ only; consumer tokens pass through untouched
          ServeMux         route match
            idempotency    claim the key, or replay the stored response byte for byte
              handler      payment.Service.Create
```

`/healthz` and `/internal/v1/saturation` skip admission, because diagnostics have to answer
precisely when everything else is refusing to.

Inside `payment.Service.Create`, in order:

1. **`assertNotBlocked`** — Redis flag first, falling back to `account_blocks` in Postgres when
   the flag is absent. If the check itself errors the request is refused rather than allowed
   through: *"block check failed, refusing to proceed blind"*.
2. **`loadMerchant`** — the merchant must exist and be active.
3. **`guard.Check`** — velocity rules against Redis counters. VR-08 refuses with
   `403 step_up_required` past Rp 50,000,000 outbound per hour.
4. **`reserve`** — a Redis Lua script does check-and-decrement in one round trip. This is the
   only thing standing between two concurrent requests and a double spend of the same balance.
5. **`commit`** — one Postgres transaction:
   - three `ledger_entries` rows plus a `ledger_transactions` header, summing to zero
   - the `payments` row
   - `charges.MarkPaid` if this settles a payment link, behind `FOR UPDATE`
   - `outbox.Write` — the event, in the same transaction as the money
6. If the commit fails, the Redis reservation is **released**, and the release failure is
   reported separately rather than swallowed.

The deferred constraint trigger `ledger_entries_balanced` fires at COMMIT, not at INSERT.
Entries are written one at a time, so the sum is briefly non-zero mid-transaction; what must
never happen is committing an unbalanced one.

Nothing on this path writes a balance. `available` is `SUM(amount_minor)` over the account's
entries; Redis holds a copy that can be thrown away and rebuilt.

## 4. Where each store's authority ends

| Store | Owns | Survives losing it? |
|---|---|---|
| PostgreSQL | the ledger, every operation row, sessions, the audit log | no; this is the system |
| Redis | balance cache, velocity counters, session lookups, promo quota | yes; every value is reconstructable, and the API fails closed rather than guessing |
| Kafka | `payment.events` for consumers that are not built yet | yes; the outbox keeps the events until something publishes them |

Redis holding the *reservation* is the one place this is subtle. The reservation is not the
truth — the ledger is — but it is what makes the check fast enough to sit on the hot path. A
Redis failure between reserve and commit leaks a reservation; the cache entry is invalidated so
the next read rebuilds from the ledger.

## 5. Where the numbers come from

Two directions, deliberately different:

- **Writes** go through the packages that own the rule: `payment`, `txn`, `payout`, `compliance`.
- **Reads for dashboards** go through `internal/console`, which holds projections and aggregates
  and nothing else — no writes, no business rules. Latency percentiles are computed by
  `percentile_disc` inside PostgreSQL, so the payout engine view is one round trip no matter how
  many payouts it summarises.

`internal/state` sits across both: every status change writes a row in the same transaction as
the change it describes, so a trail cannot exist for something that rolled back.

## 6. What is wired but not running

Stated plainly, because the code reads as though these are live and they are not:

**The outbox relay does not run in the server.** `internal/outbox` has `Relay.Sweep`, it is
tested against a real broker, and `cmd/marspay` never calls it. Events accumulate in the
`outbox` table and nothing publishes them to Kafka. The Jobs & Queues screen shows that depth
growing, which is the honest reading of the current system.

**The webhook dispatcher does not run either.** `DispatchBatch` exists and is tested; nothing
schedules it.

**No consumers exist.** `payout-svc`, `notification-svc` and `risk-scorer` in `ARCHITECTURE.md`
are designs. Payouts are settled synchronously by `payout.Service.Settle` when something calls
it, not by a worker reading `payment.events`.

**The interface is not a BFF.** Tokens live in `sessionStorage`, which means XSS could read
them. Moving to httpOnly cookies with Next route handlers proxying the API would fix that and
remove the need for CORS; it would also put the Next server in the data path. That trade has
not been made.

## 7. Building and running

```sh
./scripts/demo.sh          # both processes, seeded data, credentials printed
./scripts/check-ui.sh      # replays every call the interface makes, plus CORS and authorisation
./scripts/load-test.sh     # k6 against POST /v1/payments, then audits the books
./scripts/stress-test.sh   # concurrency ramp; names the resource that queues
./scripts/crash-test.sh    # kill -9 the postmaster mid-flight, verify against an fsynced journal
./scripts/compare-ledger.sh  # append-only ledger against a balance column
```

Test dependencies are local processes, not containers: `scripts/test-db.sh` starts PostgreSQL on
55432 and Redis on 6399 from the binaries already installed, so the suite needs no daemon
running. Redpanda is the one exception and runs in Docker; when Docker is not up, the script
says `redpanda skipped` and the Kafka-backed tests skip with it rather than failing.

Go tests use `internal/testdb`, which creates a throwaway database per package and drops it on
cleanup, so packages can run in parallel without migrating over each other.
