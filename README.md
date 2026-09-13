# Marspay

A digital wallet built to be explained, not just demoed. Consumers hold a balance, pay
merchants, split bills and settle utilities. Merchants accept payments and — this is the part
that differs — **get paid within seconds instead of the next business day**.

The money is simulated. The system is not.

> **Status: feature complete.** 70 endpoints across consumer, merchant, operations and risk
> surfaces, backed by 308 tests across 16 packages including integration tests against real
> PostgreSQL, Redis and a Kafka broker. Both credential types are real: merchant API keys, and
> consumer sessions from phone, one-time code and PIN, bound to a device. Everything below
> states plainly what is proven and what is not.

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
| The limit is known | ramp to saturation and name the resource that queues | done |
| Overload is refused, not queued | shed past a bounded queue; measure whether it helps | done |
| A stolen token stops working | replay a rotated refresh token; the whole chain dies | done |

None of these require a single real rupiah.

### How a consumer signs in

```
POST /v1/auth/otp      phone                      -> challenge id, expires in 5 minutes
POST /v1/auth/token    challenge + code + PIN     -> access token, refresh token, device id
POST /v1/auth/refresh  refresh token              -> a new pair; the old one stops working
POST /v1/auth/logout                              -> this session only
GET  /v1/devices                                  -> every device, with the current one marked
POST /v1/devices/{id}/revoke                      -> signs that handset out everywhere
```

Four secrets pass through this system and each is stored differently, because the threat
against each one is different:

| Secret | Stored as | Why |
|---|---|---|
| PIN | argon2id, salted | six digits is one million guesses; the hash has to be slow enough that a leaked database is not a list of PINs |
| Access and refresh tokens | SHA-256 | 256 bits of entropy from `crypto/rand`; there is nothing to brute-force, and a slow hash on every request would be a denial of service against ourselves |
| One-time code | SHA-256 | also weak, but it expires in five minutes and dies after three wrong guesses; the limits are the protection, not the hash |
| Webhook signing secret | encrypted, recoverable | HMAC needs the secret back, so it cannot be hashed at all |

Two behaviours are worth singling out.

**A replayed refresh token revokes the whole chain.** Refresh tokens rotate: using one issues a
new pair and retires the old. If a retired token is presented again, that is either a client
bug or a stolen token being used alongside the real one, and there is no way to tell which from
the server. So every session descended from it is revoked and both parties have to sign in
again. `TestReplayingARotatedRefreshTokenKillsTheWholeChain` pins that.

**A mistyped PIN does not cost you the SMS.** The one-time code is marked spent inside the same
transaction that verifies the PIN and issues the tokens, so a typo leaves the code usable.
Getting this backwards is easy — the first version consumed the code first — and it would have
forced a new SMS for every fat-fingered PIN. The attempt limit that actually bounds an attacker
is the PIN counter: three wrong entries lock entry for fifteen minutes.

Sessions are resolved through Redis with a 60-second TTL, so the hot path does not spend a
PostgreSQL connection authenticating. Logout and device revocation delete the cache entry
directly, so the common case is immediate; the bound on anything else is that one minute.

### The load test

```sh
./scripts/load-test.sh
```

Brings up PostgreSQL and Redis, migrates, seeds 500 consumers and one merchant, starts the
API, runs k6 against the real `POST /v1/payments` endpoint, then audits the books.

Measured on an M-series laptop, PostgreSQL 15, `synchronous_commit=on`, 50 virtual users:

```
http_reqs .......... 216,255   5,406/s
http_req_duration .. med=4.66ms  p(95)=11.00ms  p(99)=25.07ms  max=233ms
http_req_failed .... 0.00%   0 out of 216,255

 payments | ledger_transactions | ledger_entries | idempotency_keys | global_sum
   216255 |              216255 |         648765 |           216255 |          0
```

Three numbers matter more than the rate. `payments` equals `ledger_transactions` exactly, so
no request produced a payment without a ledger transaction or the other way round.
`ledger_entries` is exactly three times that, so every payment posted its full debit, credit
and fee. And `global_sum` is zero after 216,000 concurrent writes.

**This test used to drive one consumer, and that was a bug in the test.** VR-08 caps outbound
value at Rp 50,000,000 per user per hour, and each payment here is Rp 32,000 — so a single
account can make 1,562 payments and then every further request correctly returns
`403 step_up_required`. The old published figure of 251,982 was measured before the velocity
engine existed and cannot be reproduced against this code; re-running it today yields exactly
1,562 successes and half a million refusals. The fix is to drive a population of 500
consumers, which is both what the product actually sees and what the rules are written for.
The rate dropped from 6,294/s to 5,406/s across two honest changes: 500 wallets mean 500 Redis
keys and 500 velocity counters instead of one hot pair, and every request now carries a real
access token that has to be resolved to a user. Authentication alone accounts for about 7% of
it — the same run measured 5,826/s before real sessions replaced the development stand-in.

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
against 5,406/s there — is entirely that serialisation, not the database.

### The stress test: where it stops scaling, and why

```sh
./scripts/stress-test.sh
```

The load test reports one point on a curve. This one walks the curve: it holds each
concurrency level for ten seconds and asks the API what its own connection pool is doing
between steps, so the answer to "what broke" is measured rather than guessed.

```
==> 10s per step, stepping 1..1024 across 500 consumers
==> a refused client waits 100ms before trying again
==> the API holds a pool of 10 connections on 10 cores

clients     ops/s       p50       p95       p99  offered   shed%  shed p99 poolwait/req   errors
------------------------------------------------------------------------------------------------
      1      1477     604µs     880µs     1.7ms     1477      0%        0s          0s        0
      2      2853     646µs     851µs     1.3ms     2853      0%        0s          0s        0
      4      3623       1ms     1.5ms     2.7ms     3623      0%        0s         1µs        0
      8      5436     1.4ms     1.7ms     2.1ms     5436      0%        0s         1µs        0
     16      6861     2.2ms     2.9ms     4.7ms     6861      0%        0s       241µs        0
     32      5019     5.7ms    10.3ms    16.9ms     5019      0%        0s       3.5ms        0
     64      4962    11.2ms    22.2ms    26.2ms     4962      0%        0s      10.1ms        0
    128      5489      22ms    35.1ms      41ms     5489      0%        0s      20.6ms        0
    256      5628    41.3ms    75.2ms    92.3ms     5628      0%        0s      42.6ms        0
    512      3953   132.2ms   156.7ms   469.9ms     3953      0%        0s     125.5ms        0
   1024      5082   106.6ms   165.6ms   188.7ms     9052     44%    32.6ms      97.1ms        0

Where it stops scaling (pool of 10 connections):
  peak throughput   : 6861 ops/s at 16 clients
  queue forms at    : 16 clients, where 51% of database acquisitions find an empty pool

What saturated:
  at 1024 clients the mean request takes 115.1ms, of which 97.1ms is spent waiting for a
  pooled connection (84%)

 payments | ledger_transactions | ledger_entries | global_sum
   579668 |              579668 |        1739004 |          0

PASS: the books stayed balanced through saturation
```

The shape is the point. Up to eight clients, throughput rises and latency barely moves. At
sixteen, the `waited%` reported by the pool jumps from nothing to 51% — that is the moment
requests start queueing for one of ten pooled connections rather than for the database. From
there, adding clients buys nothing: 1,024 clients produce less throughput than 16 while paying
48× the p50.

The `poolwait/req` column turns the observation into a diagnosis. At 1,024 clients the mean
request takes 115.1ms and spends 97.1ms of it waiting to borrow a connection. **The database is
not the bottleneck; the pool in front of it is.** PostgreSQL was configured for 100 connections
and pgx defaulted to 10, one per core, so nine tenths of the configured capacity sat unused
while requests queued.

Read the 512-client row against the 1,024-client row. At 512 nothing is shed and the p99 is
469.9ms — the worst in the whole ramp. At 1,024 admission control engages, 44% of offered load
is refused in 32.6ms, and the p99 for everyone admitted falls back to 188.7ms. The queue stops
growing because it is no longer allowed to.

Run-to-run the peak lands anywhere between roughly 4,800 and 7,300 ops/s on this laptop, so
treat the rate as an order of magnitude. The concurrency at which the queue forms is stable
across runs, and that is the number worth quoting.

> **A correction.** The table published here previously was measured against the wrong process.
> A server left running on port 8099 from an earlier debugging session meant `stress-test.sh`
> silently failed to bind, and the ramp measured that stale binary instead of the one it had
> just built. The script now refuses to start if anything is already listening, and aborts if
> the API does not report `listening` in its own log. The numbers above come from a run that
> was verified to be measuring the build under test.

### Load shedding, and when it is worth having

At the saturation point the API has a choice: queue the excess or refuse it. `internal/admission`
bounds concurrency, bounds the queue behind it, and bounds how long anything may sit in that
queue. Past all three it answers `503 service_overloaded` with a `Retry-After` header. The
defaults are 512 in flight, 128 queued, 25ms of patience, all settable by environment variable.

Whether that helps turned out to depend entirely on how the caller behaves. At 1,024 clients:

| Admission control | Caller | ops/s | p50 | p99 | shed |
|---|---|---|---|---|---|
| off | retries immediately | 5,394 | 162.9ms | 370.8ms | 0% |
| on | retries immediately | 3,121 | 177.7ms | 299.4ms | 97% |
| off | waits 100ms first | 6,533 | 139.4ms | 307.7ms | 0% |
| on | waits 100ms first | 5,452 | **93.9ms** | **207ms** | 43% |

Against a caller that honours the `Retry-After`, shedding cuts the p99 by a third and the p50
by a third, for 17% less throughput. Against a caller that hammers, it is a **bad trade**: 42%
of the throughput disappears into the cost of refusing requests, and the tail barely improves.
A refusal is cheap, but it is not free, and at 100,000 refusals per second the server spends
more of itself saying no than doing work.

That is why the 503 carries `Retry-After`, and why the load generator has a `-backoff` flag —
a load test that models only the hostile client would have concluded this feature was harmful.
The first four measurements did exactly that.

The defaults sit deliberately above the plateau rather than inside it. An earlier default of 64
in flight was measured and rejected: at 384 clients it cost 55% of throughput to gain 11% on
the p99, because limiting concurrency below the level the system handles comfortably only
starves it. The limiter is a backstop against unbounded queueing, not a throttle on normal
traffic.

`/healthz` and `/internal/v1/saturation` are never shed. Diagnostics have to answer precisely
when everything else is refusing to.

### The comparison: an append-only ledger against a balance column

```sh
./scripts/compare-ledger.sh
```

`docs/database.md` claims that a balance is derived, never stored. This benchmark is the
attempt to falsify that claim. It runs the same payment — debit the payer, credit the
merchant, credit the platform fee — through two implementations against the same PostgreSQL
instance, with 32 concurrent writers and 4,000 operations per scenario:

- **append** is the real `internal/ledger` code: three immutable rows and a transaction
  header, with the deferred balance trigger firing on every commit
- **column** is the naive alternative: three `UPDATE accounts SET balance = balance ± x`
  statements in one transaction

```
==> 4000 operations, 32 concurrent writers

scenario                        ops/s        p50        p95        p99   errors
---------------------------------------------------------------------------------
append, one hot merchant        20613      1.5ms      1.8ms        4ms        0
append, spread merchants        22404      1.4ms      1.6ms      1.7ms        0
column, one hot merchant         8419      3.5ms      5.6ms      7.2ms        0
column, spread merchants        12700      2.3ms      3.7ms      4.8ms        0
column, sharded fee row         26159      1.2ms      1.3ms      1.4ms        0

Reading one account balance:
                              reads/s        p50        p99
------------------------------------------------------------
append balance read             17770      344µs      1.6ms
column balance read            127420       58µs      158µs

Correctness after the hot-account runs:
  expected credit on the hot account : 12710400000
  append  result                     : 12710400000
  column  result                     : 12710400000
  ledger global sum                  : 0
```

Both implementations are correct. Neither loses an update — PostgreSQL's row locks see to
that. The difference is throughput, and the interesting part is *where* it comes from.

The first version of this benchmark reported 8,482/s for the hot column scenario and 8,471/s
for the spread one. That near-identity was the result worth chasing: if spreading writes
across 200 merchant accounts changes nothing, then the merchant row was never the bottleneck.
It wasn't. Every payment also credits **one** platform fee row, so every payment in the entire
system serialised behind that single lock. Spreading the merchant accounts only moved the
queue; it didn't shorten it.

The last row proves the diagnosis. Shard the fee account 16 ways and the column model jumps to
26,159/s — faster than the append-only ledger. So the honest conclusion is not that appending
is faster:

> An append-only ledger is contention-free *by construction*. A balance column can be made
> faster, but only if you already know which rows are hot and shard each one.

In a payment system you don't get that knowledge in advance. A merchant goes viral, a
promotion lands, a biller settles — and yesterday's cold row is today's serialisation point.
The append-only model never needs the prediction.

It is not free. Reading one balance costs 344µs by `SUM()` against 58µs from a column, and
that gap widens with every row the account accumulates. That number is the entire reason
`internal/wallet` keeps a Redis projection in front of the ledger, and the reason the
projection is treated as disposable: it is a cache over an expensive read, not a second copy
of the truth.

## Repository layout

```
ARCHITECTURE.md      service map, consistency models, failure modes
docs/
  stack.md           what actually runs, and the path a request takes through it
  database.md        schema design and rationale
  api.md             HTTP contract, idempotency, webhooks
cmd/marspay/         API server entrypoint
cmd/crashdriver/     load and verify phases for the crash test
cmd/demoseed/        one consumer, one operator, one merchant, an opening balance
cmd/uicheck/         replays what the UI calls and checks the fields it reads
cmd/ledgerbench/     append-only ledger against a balance column
cmd/stressdriver/    concurrency ramp that finds the saturation point
internal/
  admission/         bounded in-flight, bounded queue, bounded wait
  auth/              consumer sessions, PIN hashing, devices, token rotation
  console/           read-only projections behind the merchant and ops dashboards
  state/             every status change, with who changed it and why
  money/             minor units, fee rounding
  ledger/            double-entry postings, Postgres repository
  wallet/            balance reservation (Redis Lua, and an in-memory twin)
  idempotency/       key store and middleware
  payment/           payment service and handler
  risk/              merchant scoring and the holdback formula
  rail/              bank rail interface and the fault-injecting simulator
  payout/            instant payout, float guard, rail selection
  reconcile/         internal books vs provider statement
  txn/               transfers, top ups, withdrawals, bills, refunds
  compliance/        audit log, account blocks, KYC queue, disputes
  merchant/          API keys, outlets, staff
  loyalty/           points, offers, money requests, bill splits
  velocity/          hot-path rule engine
  webhook/           signing, backoff schedule, dispatcher with dead letter queue
  outbox/            transactional outbox, ordered relay, Kafka publisher
  api/               router and wiring
  httpx/             error envelope, request ids, strict JSON decoding
  id/                prefixed ULIDs
  testdb/            per-package throwaway database for tests
migrations/          numbered SQL, up and down
scripts/             test infrastructure, ledger invariant check
web/                 Next.js interface, its own build and its own process
  src/lib/api.ts     typed client, refresh-on-401, error envelope
  src/app/           one route per role
mockup/              the static prototype the interface was ported from
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

Unit tests need nothing. Integration tests need PostgreSQL 15+, Redis and a Kafka broker, and
each group skips itself when its environment variable is unset rather than failing.

```sh
go test ./...                      # unit tests only, integration tests skip

./scripts/test-db.sh up            # PostgreSQL, Redis and Redpanda; prints the exports
eval "$(./scripts/test-db.sh env)"
go test ./...                      # now everything runs
./scripts/test-db.sh down          # stop and delete all three
```

PostgreSQL and Redis run natively on non-default ports. Redpanda runs in Docker as a
Kafka-compatible broker; if Docker is not running the script says so and the Kafka tests skip.

The integration tests apply every migration from scratch on each run, so they also serve as
the migration test. Three of them are the ones worth reading:

| Test | What it proves |
|---|---|
| `TestPostWritesBalancedPayment` | a payment lands as three entries summing to zero |
| `TestDatabaseRejectsUnbalancedPostingBypassingGoValidation` | the database refuses an unbalanced commit even when the Go check is skipped |
| `TestConcurrentPostingsKeepGlobalSumZero` | 400 concurrent postings leave `SUM(amount_minor) = 0` |

## Trying it

```sh
./scripts/demo.sh
```

Brings up PostgreSQL and Redis, migrates, seeds one consumer with a balance and one merchant
with an API key, starts the API on `127.0.0.1:8080`, and starts the web interface on
<http://127.0.0.1:3000>. It prints the credentials and a sequence of `curl` calls that signs in
and spends money. Ctrl-C stops both.

**Two processes, deliberately.** The API is a Go binary. The interface is a Next.js app with its
own `server.js`, built to `output: "standalone"` so it ships with its own dependencies and runs
under plain `node` without the repository. They share nothing but HTTP, which is why the API
carries a CORS allowlist and the interface holds its tokens in `sessionStorage` rather than in
a session the API knows about. Deploying one does not deploy the other.

Click **Sample data** in the top right to open the connection bar, then sign in. The consumer
screens switch from fictional data to the real ledger: the balance is a `SUM()` over
`ledger_entries`, the history is the real activity feed, and Pay, Transfer, Top Up and Withdraw
move real money through the real velocity rules. Sign out and the sample data comes back.

Sign in as the operator, and paste the merchant API key, to bring the other three roles up the
same way. What is live in each:

| Role | Live | Still sample |
|---|---|---|
| Consumer | balance, history, profile, devices, pay, transfer, top up, withdraw, bills, offers, points, money requests, split bill, inbox |
| Merchant | payments, payouts, holdback, settlements, API keys, outlets, staff, webhook deliveries, hourly volume, refunds, payment links |
| Admin / Ops | search, float, payout engine, reconciliation, audit, queues and scheduled work, the ledger behind a payment, any account |
| Risk | alerts, rules, disputes and their resolution, blocks and unblocking, KYC queue and review, merchant score |

Every screen that shows a number now gets it from the API. The two places the mockup once
showed Kafka consumer lag report the queues this system actually has — the outbox relay, the
webhook dispatcher, and money in flight at a bank — because a consumer group that does not
exist cannot have a lag. Nothing shown as live is invented, and nothing invented is shown as
live.

```sh
./scripts/check-ui.sh
```

Starts a throwaway stack and replays all 33 calls the UI makes across the four roles, checking
the status, the fields the UI reads, the CORS preflight, and that a consumer token is refused
by the operator endpoints. It exists because the first version of the wiring asked for
`full_name` where the API returns `name`, and sent `bca` where the API wants `BCA`; both
failures were silent in the browser and obvious here.

In demo mode the one-time code is returned in the `POST /v1/auth/otp` response, because there
is no SMS provider. `MARSPAY_REVEAL_OTP` controls that and defaults to off everywhere else.

The interface on its own, pointed at an API you started yourself:

```sh
cd web
npm install
npm run dev          # or: npm run build && npm start
```

Switch between the four roles — Consumer, Merchant, Admin/Ops, Risk/Compliance — using the tabs
in the header. `mockup/` is the original static prototype the interface was ported from; it is
kept as the design reference and is no longer what `demo.sh` serves.

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
