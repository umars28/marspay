# Database design

PostgreSQL 15 or newer, one logical database. Every design choice below exists to protect one
invariant:

```sql
SELECT SUM(amount_minor) FROM ledger_entries;  -- always exactly 0
```

If that query ever returns a non-zero value, the system has lost or invented money and
everything else in this document is irrelevant.

## 1. Conventions

| Decision | Choice | Rationale |
|---|---|---|
| Money type | `BIGINT`, minor units (sen, 1/100 IDR) | never floating point; fee maths produces fractions, integers do not drift |
| Identifiers | prefixed ULID as `TEXT` (`txn_01J7QF3K2M`) | time-ordered like UUIDv7, but readable in logs and support tools |
| Timestamps | `TIMESTAMPTZ`, stored UTC | the product is single-timezone today, the data should not assume it |
| Deletes | none, anywhere | corrections are new opposite rows; the audit trail is append-only |
| Enums | `TEXT` + `CHECK` | adding a value is a migration, not a table rewrite |

### Rounding

Fees are a percentage and produce fractions. The rule:

1. Compute the fee in sen, round half-up to a whole sen.
2. The rounding remainder is absorbed by the platform fee account.

This keeps every transaction summing to zero without pushing sub-sen amounts into user
balances, where they would be invisible and eventually wrong.

## 2. The ledger

Three tables carry all money. Everything else in the database references them.

### `accounts` — the chart of accounts

Not user accounts. These are ledger accounts, and users own some of them.

```sql
CREATE TABLE accounts (
  id           TEXT PRIMARY KEY,
  owner_type   TEXT NOT NULL CHECK (owner_type IN ('user','merchant','platform','provider')),
  owner_id     TEXT,
  account_type TEXT NOT NULL,
  currency     TEXT NOT NULL DEFAULT 'IDR' CHECK (currency = 'IDR'),
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (owner_type, owner_id, account_type)
);
```

| `account_type` | Owner | Meaning |
|---|---|---|
| `user_wallet` | user | spendable balance |
| `user_hold` | user | reserved, not spendable, not yet gone |
| `merchant_payable` | merchant | owed to the merchant, not yet paid out |
| `merchant_holdback` | merchant | withheld against dispute risk, released on a timer |
| `platform_fee_revenue` | platform | what we earn |
| `platform_float` | platform | our own money currently lent out by instant payout |
| `provider_clearing` | provider | funds in flight at a bank or switch |

The `provider_clearing` accounts are what make reconciliation possible: money that has left us
but not yet arrived has somewhere real to sit.

### `ledger_transactions` — the grouping

```sql
CREATE TABLE ledger_transactions (
  id            TEXT PRIMARY KEY,
  kind          TEXT NOT NULL CHECK (kind IN (
                  'topup','payment','transfer','withdrawal','bill_payment',
                  'refund','reversal','payout','holdback_release','fee','adjustment')),
  reference_id  TEXT,
  description   TEXT,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  metadata      JSONB NOT NULL DEFAULT '{}'
);
```

### `ledger_entries` — the only place money exists

```sql
CREATE TABLE ledger_entries (
  id             TEXT NOT NULL,
  transaction_id TEXT NOT NULL REFERENCES ledger_transactions(id),
  account_id     TEXT NOT NULL REFERENCES accounts(id),
  amount_minor   BIGINT NOT NULL CHECK (amount_minor <> 0),
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
```

Positive is credit, negative is debit. A payment of Rp 32.000 with a 0.7% fee:

| Account | `amount_minor` | |
|---|---:|---|
| `usr_44182` `user_wallet` | −3 200 000 | payer loses |
| `merch_8812` `merchant_payable` | +3 177 600 | merchant gains |
| `platform` `fee_revenue` | +22 400 | we gain |
| | **0** | must be |

Monthly range partitions. `ledger_entries` is the largest table in the system and is
append-only, so old partitions are read-only and can be moved to cheaper storage or detached
without touching live writes. The partition key must be in the primary key, which is why the
key is `(id, created_at)` rather than `id` alone.

### Enforcing the invariant

A `CHECK` cannot see other rows, so balance is enforced by a deferred constraint trigger that
fires once at commit:

```sql
CREATE CONSTRAINT TRIGGER ledger_entries_balanced
  AFTER INSERT ON ledger_entries
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION assert_transaction_balanced();
```

`assert_transaction_balanced()` raises if `SUM(amount_minor)` for the row's `transaction_id`
is not zero. Deferring it is essential: entries are inserted one at a time, so the sum is
briefly non-zero mid-transaction and that is fine. What must never happen is *committing* an
unbalanced transaction.

This makes the invariant impossible to violate through application bugs. It is the single
most important line in the schema.

### Balances are derived, never stored

```sql
CREATE VIEW account_balances AS
SELECT account_id, SUM(amount_minor) AS balance_minor
FROM ledger_entries GROUP BY account_id;
```

This view is correct and too slow for the hot path, so Redis caches it. The cache is a
projection: when Redis and this view disagree, **this view wins**, and the disagreement
surfaces on the Admin reconciliation screen. A materialised view refreshed by the balance
projector sits between the two for admin queries.

`scripts/compare-ledger.sh` measures both halves of that trade. Appending sustains 20,613
payments/s against one hot merchant where a `balance` column manages 8,419, because the column
version serialises every payment in the system behind the single platform fee row. Reading one
balance goes the other way: 344µs through `SUM()` against 58µs from a column. The write side is
why balances are derived; the read side is why Redis exists. Full results and the caveat — a
column shards its way back to 26,159/s if you know in advance which rows are hot — are in the
README.

### Sessions and one-time codes

```sql
CREATE TABLE sessions (
  id                 TEXT PRIMARY KEY,
  user_id            TEXT NOT NULL REFERENCES users(id),
  device_id          TEXT NOT NULL REFERENCES devices(id),
  access_hash        TEXT NOT NULL UNIQUE,
  refresh_hash       TEXT NOT NULL UNIQUE,
  rotated_to         TEXT REFERENCES sessions(id),
  revoked_at         TIMESTAMPTZ,
  revoked_reason     TEXT,
  ...
);
```

`rotated_to` is the whole design. A session is never updated in place on refresh; a new row is
inserted and the old one points at it. That turns a session into a linked list, which is what
makes replay detection possible: a refresh token whose row already has a `rotated_to` was used
twice, and the recursive walk down that chain revokes every descendant in one statement.
Overwriting the tokens in place would have thrown away exactly the evidence needed to notice.

Both hashes are `UNIQUE`, so the database refuses to hold the same token twice regardless of
what the application believes. Neither column can be reversed into a credential — they are
SHA-256 digests, and the tokens they came from carry 256 bits from `crypto/rand`.

`otp_challenges` keeps `attempts` and `max_attempts` as columns rather than in Redis, because a
code that survives a cache flush with its attempt counter reset is a code that can be brute
forced. The counter and the secret belong in the same place.

### Payment links

```sql
CREATE TABLE charges (
  ...
  status      TEXT NOT NULL CHECK (status IN ('open','paid','expired','cancelled')),
  payment_id  TEXT REFERENCES payments(id),
  expires_at  TIMESTAMPTZ NOT NULL,
  UNIQUE (merchant_id, reference),
  CHECK ((status = 'paid') = (payment_id IS NOT NULL))
);
```

The last `CHECK` is the one worth reading. A link is paid if and only if it points at a
payment: the database will not accept a link marked paid with nothing behind it, nor a link
carrying a payment id while still claiming to be open. Application code cannot drift from that
because it is not application code.

Expiry is **derived on read**, never written by a reader. A `GET` on an expired link reports
`expired: true` while the row still says `open`, because a read that rewrites rows turns every
dashboard refresh into a write and makes the history depend on who looked at it. The status
column only changes when somebody acts.

### Every status change is recorded

```sql
CREATE TABLE operation_state_transitions (
  operation_kind TEXT NOT NULL,
  operation_id   TEXT NOT NULL,
  from_status    TEXT,
  to_status      TEXT NOT NULL,
  actor          TEXT NOT NULL,
  reason         TEXT,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

A status column tells you where an operation is. It cannot tell you how it got there, who
decided, or why — and those are the three questions support actually asks. Each transition is
written in the **same transaction** as the status change it describes, so a trail can never
exist for a change that rolled back, and a change can never happen without its trail.

`actor` is not nullable on purpose. Every state change in this system has somebody or something
responsible for it: a provider callback, an operator, the payer, or `system`.

## 3. Operations

Each product flow gets its own table rather than one polymorphic `transactions` table,
because the fields genuinely differ — a bill payment needs a biller and a customer number, a
withdrawal needs a bank account, a payment needs a merchant. They converge on
`ledger_transaction_id`, which is where they become money.

| Table | Flow-specific columns |
|---|---|
| `payments` | `merchant_id`, `outlet_id`, `method`, `fee_minor` |
| `transfers` | `payer_user_id`, `payee_user_id`, `note` |
| `topups` | `source`, `provider_ref`, `va_number` |
| `withdrawals` | `bank_code`, `account_number`, `account_name` |
| `bill_payments` | `biller_code`, `customer_ref`, `period`, `admin_fee_minor` |
| `refunds` | `payment_id`, `reason`, `partial` |

All share: `id`, `user_id`, `amount_minor`, `status`, `idempotency_key`,
`ledger_transaction_id`, `created_at`, `updated_at`.

### Status is a state machine, not a column you overwrite

```
created ──▶ risk_held ──▶ pending ──▶ succeeded
   │            │            │
   │            ▼            ▼
   └────────▶ failed ◀── reversed
```

`pending` is load-bearing. It is the state for "the provider timed out and we do not yet know
whether the money moved". Collapsing it into `failed` causes double charges on retry;
collapsing it into `succeeded` loses money. Every transition is written to
`payment_state_transitions` with the actor and reason, so a support agent can answer
"what happened" without reading application logs.

### `idempotency_keys`

```sql
CREATE TABLE idempotency_keys (
  key            TEXT NOT NULL,
  scope          TEXT NOT NULL,
  request_hash   TEXT NOT NULL,
  status         TEXT NOT NULL CHECK (status IN ('in_progress','completed')),
  response_code  INT,
  response_body  BYTEA,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at     TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (scope, key)
);
```

`scope` is the merchant or user, so two callers cannot collide on the same key. `request_hash`
catches the dangerous case: the same key reused with a *different* body. That is a client bug
and must return `422`, not silently replay the first response.

The completed response body is stored so a replay returns the original answer byte for byte,
including the original `id`. Retention is 24 hours; the row is not the source of truth, the
ledger is.

`response_body` is `BYTEA`, not `JSONB`, and the difference is not cosmetic. `JSONB` is a
parsed representation: it reorders object keys and normalises whitespace, so what comes back
out is semantically equal to what went in but not byte-identical. That silently breaks the
"byte for byte" promise in the API contract, and it breaks HMAC signatures computed over a
response body. If the column exists to replay bytes, it has to store bytes.

## 4. Instant payout — the tables the differentiator needs

### `payouts`

```sql
CREATE TABLE payouts (
  id               TEXT PRIMARY KEY,
  merchant_id      TEXT NOT NULL REFERENCES merchants(id),
  payment_id       TEXT REFERENCES payments(id),
  mode             TEXT NOT NULL CHECK (mode IN ('instant','batch')),
  gross_minor      BIGINT NOT NULL,
  holdback_minor   BIGINT NOT NULL DEFAULT 0,
  net_minor        BIGINT NOT NULL,
  rail             TEXT CHECK (rail IN ('bifast','bank_internal','sknbi')),
  status           TEXT NOT NULL,
  attempts         INT NOT NULL DEFAULT 0,
  bank_reference   TEXT,
  failure_reason   TEXT,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  settled_at       TIMESTAMPTZ,
  latency_ms       INT,
  CHECK (net_minor = gross_minor - holdback_minor)
);
```

`latency_ms` is a stored column, not something computed at query time. The payout SLA is a
product promise, so it needs to be queryable and alertable without a join.

### `holdbacks`

```sql
CREATE TABLE holdbacks (
  id           TEXT PRIMARY KEY,
  merchant_id  TEXT NOT NULL REFERENCES merchants(id),
  payout_id    TEXT NOT NULL REFERENCES payouts(id),
  amount_minor BIGINT NOT NULL CHECK (amount_minor > 0),
  release_at   TIMESTAMPTZ NOT NULL,
  released_at  TIMESTAMPTZ,
  consumed_by  TEXT REFERENCES disputes(id)
);
```

A holdback is money sitting in the merchant's `merchant_holdback` ledger account. Release is a
ledger transaction of kind `holdback_release`, not an `UPDATE`. `consumed_by` records the
dispute that ate it, which is how we measure whether the holdback rate is set correctly.

### `merchant_risk_scores`

```sql
CREATE TABLE merchant_risk_scores (
  merchant_id   TEXT NOT NULL REFERENCES merchants(id),
  score         INT  NOT NULL CHECK (score BETWEEN 0 AND 100),
  holdback_bps  INT  NOT NULL CHECK (holdback_bps BETWEEN 150 AND 4500),
  components    JSONB NOT NULL,
  computed_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (merchant_id, computed_at)
);
```

Append-only, not an updated row. A merchant whose holdback jumped from 4% to 12% is entitled
to see when and why, and `components` holds the per-factor breakdown that produced the score.
A rate nobody can explain is not acceptable, which is also why this is a formula and not a model.

### `float_positions` and `merchant_exposure`

Instant payout means paying before collecting, so platform money is outstanding. The float
tables are the treasury view: a rolling snapshot of total outstanding against a hard limit,
plus per-merchant exposure with its own cap. When utilisation crosses 85%, the payout service
flips merchants to batch mode — the limit is enforced in the service, and these tables are how
it knows.

## 5. Loyalty points have a separate ledger

`point_accounts` and `point_entries` mirror the money ledger's structure but are a different
table with a different invariant. Mixing points into `ledger_entries` would mean
`SUM(amount_minor)` no longer has to be zero, because points are minted from nothing.

Losing that invariant to save one table is a bad trade. Points also expire, which money never
does, so they need `expires_at` and an expiry worker that money would not.

## 6. Indexing

The access patterns that matter, and what serves them:

| Query | Index |
|---|---|
| user transaction history, newest first | `(user_id, created_at DESC)` on each operation table |
| ledger entries for one transaction | `(transaction_id)` on `ledger_entries` |
| account balance rebuild | `(account_id, created_at)` on `ledger_entries` |
| merchant payouts in a window | `(merchant_id, created_at DESC)` on `payouts` |
| webhook retry sweep | partial index on `next_retry_at WHERE status = 'retrying'` |
| unreconciled provider callbacks | partial index `WHERE reconciled_at IS NULL` |
| idempotency lookup | primary key `(scope, key)` |

The two partial indexes matter more than they look. Both back worker sweeps that run
constantly against tables that are overwhelmingly finished rows; without them the workers
table-scan and their cost grows with total history rather than with pending work.

## 7. Verifying it

The migrations in `migrations/` apply and roll back cleanly, and the invariant is tested
rather than asserted. Against a throwaway cluster:

```sh
psql -d marspay -f migrations/000001_identity.up.sql   # ... through 000007
psql -d marspay -f scripts/verify-ledger-invariant.sql
```

`verify-ledger-invariant.sql` commits one balanced transaction and attempts one unbalanced
one. Actual output on PostgreSQL 15.17:

```
INSERT 0 3
COMMIT
...
ERROR:  ledger transaction txn_unbalanced is unbalanced: sum = -100000

 balanced_rows_kept | unbalanced_rows_kept | global_sum
--------------------+----------------------+------------
                  3 |                    0 |          0
```

Three things are proven here. The balanced transaction survives. The unbalanced one is
rejected **at commit**, not at insert — which is what makes the deferred trigger necessary,
since the sum is legitimately non-zero after the first row. And the global sum is still zero,
meaning the rejected rows left nothing behind.

Running the seven down migrations in reverse leaves zero tables in `public`.

## 8. What is deliberately absent

No `users.balance` column. No soft-delete flags. No triggers that write business data — the
only trigger in the schema asserts the invariant and writes nothing. No stored procedures
holding business logic; that belongs in Go where it can be tested and reviewed.
