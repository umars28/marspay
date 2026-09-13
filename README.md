# Marspay

A digital wallet built to be explained, not just demoed. Consumers hold a balance, pay
merchants, split bills and settle utilities. Merchants accept payments and — this is the part
that differs — **get paid within seconds instead of the next business day**.

The money is simulated. The system is not.

> **Status: design phase.** The UI mockup and the design documents are complete. The backend
> is not written yet. This README describes what is being built and marks what exists today.

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

| Claim | How it is proven |
|---|---|
| The ledger balances | 100k concurrent transfers, then `SUM(amount_minor) = 0` |
| Money survives a crash | `kill -9` the Postgres primary mid-payment; nothing lost or duplicated |
| Redis is disposable | flush the cache; balances rebuild from the ledger |
| Reconciliation works | inject lost callbacks via `provider-sim`; they appear as discrepancies |
| Throughput is real | k6 run with p50/p95/p99, published alongside the numbers |

None of these require a single real rupiah.

## Repository layout

```
ARCHITECTURE.md      service map, consistency models, failure modes
docs/
  database.md        schema design and rationale
  api.md             HTTP contract, idempotency, webhooks
mockup/              clickable UI prototype, 38 screens, no backend
```

Backend directories (`cmd/`, `internal/`, `migrations/`, `deploy/`) arrive with the
implementation.

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
