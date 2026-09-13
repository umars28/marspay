CREATE TABLE idempotency_keys (
  scope         TEXT NOT NULL,
  key           TEXT NOT NULL,
  request_hash  TEXT NOT NULL,
  status        TEXT NOT NULL CHECK (status IN ('in_progress','completed')),
  response_code INT,
  response_body JSONB,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at    TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (scope, key)
);
CREATE INDEX idempotency_keys_sweep_idx ON idempotency_keys (expires_at);

CREATE TABLE payments (
  id                     TEXT PRIMARY KEY,
  user_id                TEXT NOT NULL REFERENCES users(id),
  merchant_id            TEXT NOT NULL REFERENCES merchants(id),
  outlet_id              TEXT REFERENCES outlets(id),
  method                 TEXT NOT NULL CHECK (method IN ('qris','payment_link','virtual_account')),
  amount_minor           BIGINT NOT NULL CHECK (amount_minor > 0),
  fee_minor              BIGINT NOT NULL DEFAULT 0 CHECK (fee_minor >= 0),
  status                 TEXT NOT NULL DEFAULT 'created' CHECK (status IN (
                           'created','risk_held','pending','succeeded','failed','reversed')),
  failure_reason         TEXT,
  idempotency_key        TEXT,
  ledger_transaction_id  TEXT REFERENCES ledger_transactions(id),
  expires_at             TIMESTAMPTZ,
  created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX payments_user_idx ON payments (user_id, created_at DESC);
CREATE INDEX payments_merchant_idx ON payments (merchant_id, created_at DESC);
CREATE INDEX payments_expiry_idx ON payments (expires_at) WHERE status IN ('created','pending');

CREATE TABLE transfers (
  id                    TEXT PRIMARY KEY,
  payer_user_id         TEXT NOT NULL REFERENCES users(id),
  payee_user_id         TEXT NOT NULL REFERENCES users(id),
  amount_minor          BIGINT NOT NULL CHECK (amount_minor > 0),
  note                  TEXT,
  status                TEXT NOT NULL DEFAULT 'created',
  idempotency_key       TEXT,
  ledger_transaction_id TEXT REFERENCES ledger_transactions(id),
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (payer_user_id <> payee_user_id)
);
CREATE INDEX transfers_payer_idx ON transfers (payer_user_id, created_at DESC);
CREATE INDEX transfers_payee_idx ON transfers (payee_user_id, created_at DESC);

CREATE TABLE topups (
  id                    TEXT PRIMARY KEY,
  user_id               TEXT NOT NULL REFERENCES users(id),
  source                TEXT NOT NULL CHECK (source IN ('bank_va','retail','card')),
  provider_code         TEXT NOT NULL,
  provider_ref          TEXT,
  va_number             TEXT,
  amount_minor          BIGINT NOT NULL CHECK (amount_minor > 0),
  admin_fee_minor       BIGINT NOT NULL DEFAULT 0,
  status                TEXT NOT NULL DEFAULT 'created',
  idempotency_key       TEXT,
  ledger_transaction_id TEXT REFERENCES ledger_transactions(id),
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX topups_user_idx ON topups (user_id, created_at DESC);
CREATE UNIQUE INDEX topups_provider_ref_idx ON topups (provider_code, provider_ref)
  WHERE provider_ref IS NOT NULL;

CREATE TABLE withdrawals (
  id                    TEXT PRIMARY KEY,
  user_id               TEXT NOT NULL REFERENCES users(id),
  bank_code             TEXT NOT NULL,
  account_number        TEXT NOT NULL,
  account_name          TEXT NOT NULL,
  amount_minor          BIGINT NOT NULL CHECK (amount_minor > 0),
  admin_fee_minor       BIGINT NOT NULL DEFAULT 0,
  status                TEXT NOT NULL DEFAULT 'created',
  failure_reason        TEXT,
  bank_reference        TEXT,
  idempotency_key       TEXT,
  ledger_transaction_id TEXT REFERENCES ledger_transactions(id),
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX withdrawals_user_idx ON withdrawals (user_id, created_at DESC);

CREATE TABLE bill_payments (
  id                    TEXT PRIMARY KEY,
  user_id               TEXT NOT NULL REFERENCES users(id),
  biller_code           TEXT NOT NULL,
  customer_ref          TEXT NOT NULL,
  customer_name         TEXT,
  period                TEXT,
  amount_minor          BIGINT NOT NULL CHECK (amount_minor > 0),
  admin_fee_minor       BIGINT NOT NULL DEFAULT 0,
  status                TEXT NOT NULL DEFAULT 'created',
  provider_ref          TEXT,
  failure_reason        TEXT,
  idempotency_key       TEXT,
  ledger_transaction_id TEXT REFERENCES ledger_transactions(id),
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX bill_payments_user_idx ON bill_payments (user_id, created_at DESC);
CREATE INDEX bill_payments_unsettled_idx ON bill_payments (created_at)
  WHERE status = 'pending';

CREATE TABLE refunds (
  id                    TEXT PRIMARY KEY,
  payment_id            TEXT NOT NULL REFERENCES payments(id),
  merchant_id           TEXT NOT NULL REFERENCES merchants(id),
  amount_minor          BIGINT NOT NULL CHECK (amount_minor > 0),
  reason                TEXT NOT NULL,
  status                TEXT NOT NULL DEFAULT 'created',
  idempotency_key       TEXT,
  ledger_transaction_id TEXT REFERENCES ledger_transactions(id),
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX refunds_payment_idx ON refunds (payment_id);
CREATE INDEX refunds_merchant_idx ON refunds (merchant_id, created_at DESC);

CREATE TABLE operation_state_transitions (
  id             BIGSERIAL PRIMARY KEY,
  operation_kind TEXT NOT NULL,
  operation_id   TEXT NOT NULL,
  from_status    TEXT,
  to_status      TEXT NOT NULL,
  actor          TEXT NOT NULL,
  reason         TEXT,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX operation_state_transitions_idx
  ON operation_state_transitions (operation_kind, operation_id, created_at);
