# Marspay

A digital wallet built to be explained, not just demoed. Consumers hold a balance, pay
merchants, split bills and settle utilities. Merchants accept payments and — this is the part
that differs — **get paid within seconds instead of the next business day**.

The money is simulated. The system is not.

> **Status: in progress.** The ledger, payments API, risk scoring, instant payout, webhook
> dispatcher, reconciler and transactional outbox are built and tested — 108 tests across 11
> packages, including integration tests against real PostgreSQL and Redis. Kafka is designed
> but not yet wired; the outbox that feeds it is. Authentication is a development stand-in.
> Everything below states plainly what is proven and what is not.

## Why this exists

Most wallet side projects stop at "I can transfer balance between two rows". The interesting
problems in payments are the ones that only appear when things go wrong: a callback that never
arrives, a request that times out after the money already moved, a cache that disagrees with
the ledger, a merchant who disappears after being paid.

Marspay is built to make those situations happen on purpose, and to prove they are handled.

## The one differentiator: instant payout

| | Typical Indonesian wallet | Marspay |
|---|---|---|
| Merchant receives funds | T+1 or T+2 batch | seconds, p95 target 6 s |
| Risk handling | hold everything for a day | hold a computed percentage per merchant |
| Rate | flat for everyone | 1.5% – 45%, recomputed every 5 minutes |

Paying early means lending our own money until funds are collected, so the platform tracks a
**float position** with a hard limit. Above 85% utilisation, instant payout degrades to batch
automatically. It degrades — it does not fail.

This one decision is what makes the project worth building: it turns a nightly batch job into
a latency-bound streaming system, and it forces a risk engine and a treasury view into
existence. See [ARCHITECTURE.md](ARCHITECTURE.md) for how the three fit together.

## Payment rails are simulated, and that is the point

Moving real money in Indonesia requires a PJP licence from Bank Indonesia; BI-FAST and the
QRIS switch have no sandbox for individuals. So `provider-sim` stands in for banks, the QRIS
switch and billers.

This is better than a real sandbox for this purpose. Real sandboxes always succeed, respond
instantly, and deliver every callback — so reconciliation, retry and pending-state logic never
run. The simulator is configured to lose callbacks, time out after succeeding, deliver
duplicates, and reorder events.

## What "it works" will mean here

Claims in this repo are meant to be checkable. The ones that matter:

| Claim | How it is proven | Status |
|---|---|---|
| The ledger balances | concurrent postings, then `SUM(amount_minor) = 0` | done |
| The database refuses an unbalanced commit | post one bypassing the Go check | done |
| Redis is disposable | drop the cache; balances rebuild from the ledger | done |
| Reconciliation works | inject lost callbacks via `provider-sim`; they appear as differences | done |
| Instant payout degrades, never fails | push float past 85%; everyone drops to batch | done |
| Money survives a crash | `kill -9` the Postgres primary mid-payment | done |
| Throughput is real | k6 run with p50/p95/p99 published alongside the numbers | done |

None of these require a single real rupiah.

### The load test

```sh
./scripts/load-test.sh
```

Brings up PostgreSQL and Redis, migrates, seeds one merchant, starts the API, runs k6 against
the real `POST /v1/payments` endpoint, then audits the books.

Measured on an M-series laptop, PostgreSQL 15, `synchronous_commit=on`, 50 virtual users:

```
http_reqs .......... 251,982   6,294/s
http_req_duration .. med=4.26ms  p(95)=8.51ms  p(99)=16.7ms  max=351ms
http_req_failed .... 0.00%   0 out of 251,982

 payments | ledger_transactions | ledger_entries | idempotency_keys | global_sum
   251982 |              251982 |         755946 |           251982 |          0
```

Three numbers matter more than the rate. `payments` equals `ledger_transactions` exactly, so
no request produced a payment without a ledger transaction or the other way round.
`ledger_entries` is exactly three times that, so every payment posted its full debit, credit
and fee. And `global_sum` is zero after a quarter of a million concurrent writes.

This is a single-node laptop figure, not a capacity claim for production. Run it yourself; the
script takes about a minute end to end.

### The crash test

```sh
./scripts/crash-test.sh
```

It builds a dedicated cluster, runs 16 concurrent writers, `kill -9`s the postmaster
mid-flight, restarts it so PostgreSQL replays its write-ahead log, and then checks the books.
The driver keeps its own fsynced journal of every commit the database acknowledged, so the
question it answers is precise: **did anything the database said was committed fail to come
back?**

A real run:

```
==> synchronous_commit is on
==> writing for 12s with 16 concurrent writers
==> kill -9 98352 (the postmaster) mid-flight
acknowledged=886 rejected=800
==> confirmed: the server performed crash recovery

acknowledged by the driver  : 886
transactions after recovery : 886
acknowledged but missing    : 0
recovered unbalanced        : 0
transactions without entries: 0
entries without transaction : 0
global sum                  : 0

PASS: every acknowledged commit survived, nothing partial, nothing invented
```

The 800 rejected writes are the in-flight ones that met a dead database. Those are supposed
to fail, and none of them left a trace.

Do not read 886 as a throughput figure. The driver fsyncs its journal under a single mutex on
every acknowledgement, so every commit is serialised behind one disk flush. That is the
opposite of what the load test does, and the gap between the two numbers — roughly 180/s here
against 6,294/s there — is entirely that serialisation, not the database.

## Repository layout

```
ARCHITECTURE.md      service map, consistency models, failure modes
docs/
  database.md        schema design and rationale
  api.md             HTTP contract, idempotency, webhooks
cmd/marspay/         API server entrypoint
cmd/crashdriver/     load and verify phases for the crash test
internal/
  money/             minor units, fee rounding
  ledger/            double-entry postings, Postgres repository
  wallet/            balance reservation (Redis Lua, and an in-memory twin)
  idempotency/       key store and middleware
  payment/           payment service and handler
  risk/              merchant scoring and the holdback formula
  rail/              bank rail interface and the fault-injecting simulator
  payout/            instant payout, float guard, rail selection
  reconcile/         internal books vs provider statement
  webhook/           signing, backoff schedule, dispatcher with dead letter queue
  outbox/            transactional outbox and the relay that drains it
  api/               router and wiring
  httpx/             error envelope, request ids, strict JSON decoding
  id/                prefixed ULIDs
  testdb/            per-package throwaway database for tests
migrations/          numbered SQL, up and down
scripts/             test infrastructure, ledger invariant check
mockup/              clickable UI prototype, 38 screens, no backend
```

## Running the API locally

```sh
./scripts/test-db.sh up                # PostgreSQL and Redis on non-default ports
for f in migrations/*.up.sql; do psql "$(./scripts/test-db.sh dsn)" -f "$f"; done

MARSPAY_DATABASE_URL="$(./scripts/test-db.sh dsn)" \
MARSPAY_REDIS_ADDR="$(./scripts/test-db.sh redis-addr)" \
MARSPAY_ADDR=127.0.0.1:8099 \
  go run ./cmd/marspay
```

`POST /v1/payments` is implemented. Authentication is a development stand-in: the bearer
token is taken as the user id until the real auth service exists. It is named
`DevBearerAuth` so nobody mistakes it for the real thing.

## Running the tests

Unit tests need nothing. Ledger integration tests need a PostgreSQL 15+ cluster, and skip
themselves when `MARSPAY_TEST_DATABASE_URL` is unset rather than failing.

```sh
go test ./...                          # unit tests only, integration tests skip

./scripts/test-db.sh up                # throwaway cluster, prints the DSN
export MARSPAY_TEST_DATABASE_URL="$(./scripts/test-db.sh dsn)"
go test ./...                          # now the integration tests run too
./scripts/test-db.sh down              # stop and delete
```

The integration tests apply every migration from scratch on each run, so they also serve as
the migration test. Three of them are the ones worth reading:

| Test | What it proves |
|---|---|
| `TestPostWritesBalancedPayment` | a payment lands as three entries summing to zero |
| `TestDatabaseRejectsUnbalancedPostingBypassingGoValidation` | the database refuses an unbalanced commit even when the Go check is skipped |
| `TestConcurrentPostingsKeepGlobalSumZero` | 400 concurrent postings leave `SUM(amount_minor) = 0` |

## Running the mockup

The mockup is static HTML with no build step and no dependencies.

```sh
cd mockup
python3 -m http.server 8932
```

Open <http://127.0.0.1:8932>. Switch between the four roles — Consumer, Merchant, Admin/Ops,
Risk/Compliance — using the tabs in the header. All data is fictional.

## Design decisions worth knowing before reading the code

- **Balance is not a column.** It is the sum of an account's ledger entries. The Redis value
  is a projection, and when the two disagree the ledger wins.
- **Nothing is deleted.** A failed or reversed payment gets new, opposite ledger entries. The
  audit trail is append-only, including for administrators.
- **Consistency models differ per subsystem on purpose.** The ledger is strict serializable;
  the balance cache is eventually consistent; webhook delivery is at-least-once. Each choice
  is defended in ARCHITECTURE.md.
- **Redis may be lost at any time.** If a feature needs Redis to be durable, the feature is
  designed wrong.
- **Points have their own ledger.** Mixing loyalty points into the money ledger would break
  the zero-sum invariant that protects everything else.

## Licence

MIT. See [LICENSE](LICENSE).
