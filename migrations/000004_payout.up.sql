CREATE TABLE payouts (
  id                    TEXT PRIMARY KEY,
  merchant_id           TEXT NOT NULL REFERENCES merchants(id),
  payment_id            TEXT REFERENCES payments(id),
  batch_id              TEXT,
  mode                  TEXT NOT NULL CHECK (mode IN ('instant','batch')),
  gross_minor           BIGINT NOT NULL CHECK (gross_minor > 0),
  holdback_minor        BIGINT NOT NULL DEFAULT 0 CHECK (holdback_minor >= 0),
  net_minor             BIGINT NOT NULL CHECK (net_minor >= 0),
  rail                  TEXT CHECK (rail IN ('bifast','bank_internal','sknbi')),
  status                TEXT NOT NULL DEFAULT 'queued' CHECK (status IN (
                          'queued','sending','settled','failed','degraded_to_batch')),
  attempts              INT NOT NULL DEFAULT 0,
  bank_reference        TEXT,
  failure_reason        TEXT,
  ledger_transaction_id TEXT REFERENCES ledger_transactions(id),
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  settled_at            TIMESTAMPTZ,
  latency_ms            INT,
  CHECK (net_minor = gross_minor - holdback_minor)
);
CREATE INDEX payouts_merchant_idx ON payouts (merchant_id, created_at DESC);
CREATE INDEX payouts_retry_idx ON payouts (created_at) WHERE status IN ('queued','failed');
CREATE INDEX payouts_batch_idx ON payouts (batch_id) WHERE batch_id IS NOT NULL;

CREATE TABLE holdbacks (
  id           TEXT PRIMARY KEY,
  merchant_id  TEXT NOT NULL REFERENCES merchants(id),
  payout_id    TEXT NOT NULL REFERENCES payouts(id),
  amount_minor BIGINT NOT NULL CHECK (amount_minor > 0),
  release_at   TIMESTAMPTZ NOT NULL,
  released_at  TIMESTAMPTZ,
  consumed_by  TEXT,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX holdbacks_release_idx ON holdbacks (release_at) WHERE released_at IS NULL;
CREATE INDEX holdbacks_merchant_idx ON holdbacks (merchant_id, created_at DESC);

CREATE TABLE merchant_risk_scores (
  merchant_id  TEXT NOT NULL REFERENCES merchants(id),
  score        INT NOT NULL CHECK (score BETWEEN 0 AND 100),
  holdback_bps INT NOT NULL CHECK (holdback_bps BETWEEN 150 AND 4500),
  components   JSONB NOT NULL,
  computed_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (merchant_id, computed_at)
);
CREATE INDEX merchant_risk_scores_latest_idx ON merchant_risk_scores (merchant_id, computed_at DESC);

CREATE TABLE merchant_exposure (
  merchant_id       TEXT PRIMARY KEY REFERENCES merchants(id),
  outstanding_minor BIGINT NOT NULL DEFAULT 0,
  limit_minor       BIGINT NOT NULL,
  oldest_payout_at  TIMESTAMPTZ,
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE float_positions (
  id                BIGSERIAL PRIMARY KEY,
  captured_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  outstanding_minor BIGINT NOT NULL,
  limit_minor       BIGINT NOT NULL,
  utilisation_bps   INT NOT NULL,
  instant_enabled   BOOLEAN NOT NULL
);
CREATE INDEX float_positions_time_idx ON float_positions (captured_at DESC);

CREATE TABLE settlement_batches (
  id            TEXT PRIMARY KEY,
  merchant_id   TEXT NOT NULL REFERENCES merchants(id),
  period_start  TIMESTAMPTZ NOT NULL,
  period_end    TIMESTAMPTZ NOT NULL,
  payout_count  INT NOT NULL DEFAULT 0,
  gross_minor   BIGINT NOT NULL DEFAULT 0,
  fee_minor     BIGINT NOT NULL DEFAULT 0,
  net_minor     BIGINT NOT NULL DEFAULT 0,
  status        TEXT NOT NULL DEFAULT 'open',
  settled_at    TIMESTAMPTZ,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (merchant_id, period_start)
);
