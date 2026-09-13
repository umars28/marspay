CREATE TABLE users (
  id          TEXT PRIMARY KEY,
  phone       TEXT NOT NULL UNIQUE,
  email       TEXT UNIQUE,
  full_name   TEXT NOT NULL,
  pin_hash    TEXT NOT NULL,
  kyc_tier    TEXT NOT NULL DEFAULT 'unverified' CHECK (kyc_tier IN ('unverified','verified')),
  status      TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','frozen','blocked','closed')),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE user_limits (
  user_id            TEXT PRIMARY KEY REFERENCES users(id),
  max_balance_minor  BIGINT NOT NULL,
  max_per_txn_minor  BIGINT NOT NULL,
  max_monthly_minor  BIGINT NOT NULL,
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE devices (
  id           TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL REFERENCES users(id),
  platform     TEXT NOT NULL CHECK (platform IN ('ios','android','web')),
  model        TEXT,
  trusted      BOOLEAN NOT NULL DEFAULT false,
  last_seen_at TIMESTAMPTZ,
  last_ip      INET,
  revoked_at   TIMESTAMPTZ,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX devices_user_idx ON devices (user_id, last_seen_at DESC);

CREATE TABLE kyc_submissions (
  id            TEXT PRIMARY KEY,
  user_id       TEXT NOT NULL REFERENCES users(id),
  target_tier   TEXT NOT NULL,
  id_number_hash TEXT NOT NULL,
  match_score   NUMERIC(4,3),
  status        TEXT NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending','approved','rejected','resubmit','escalated')),
  reviewed_by   TEXT,
  review_reason TEXT,
  submitted_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  reviewed_at   TIMESTAMPTZ
);
CREATE INDEX kyc_queue_idx ON kyc_submissions (submitted_at) WHERE status = 'pending';

CREATE TABLE merchants (
  id              TEXT PRIMARY KEY,
  legal_name      TEXT NOT NULL,
  display_name    TEXT NOT NULL,
  category        TEXT NOT NULL,
  status          TEXT NOT NULL DEFAULT 'onboarding'
                  CHECK (status IN ('onboarding','active','suspended','terminated')),
  payout_mode     TEXT NOT NULL DEFAULT 'batch' CHECK (payout_mode IN ('instant','batch','held')),
  payout_bank     TEXT,
  payout_account  TEXT,
  payout_name     TEXT,
  fee_bps         INT NOT NULL DEFAULT 70,
  onboarded_at    TIMESTAMPTZ,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE outlets (
  id           TEXT PRIMARY KEY,
  merchant_id  TEXT NOT NULL REFERENCES merchants(id),
  name         TEXT NOT NULL,
  nmid         TEXT UNIQUE,
  address      TEXT,
  status       TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','pending_bank','disabled')),
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX outlets_merchant_idx ON outlets (merchant_id);

CREATE TABLE merchant_users (
  id           TEXT PRIMARY KEY,
  merchant_id  TEXT NOT NULL REFERENCES merchants(id),
  outlet_id    TEXT REFERENCES outlets(id),
  full_name    TEXT NOT NULL,
  email        TEXT NOT NULL,
  role         TEXT NOT NULL CHECK (role IN ('owner','supervisor','cashier')),
  revoked_at   TIMESTAMPTZ,
  last_seen_at TIMESTAMPTZ,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (merchant_id, email)
);

CREATE TABLE api_keys (
  id          TEXT PRIMARY KEY,
  merchant_id TEXT NOT NULL REFERENCES merchants(id),
  name        TEXT NOT NULL,
  prefix      TEXT NOT NULL UNIQUE,
  key_hash    TEXT NOT NULL,
  mode        TEXT NOT NULL CHECK (mode IN ('live','test')),
  scopes      TEXT[] NOT NULL DEFAULT '{}',
  last_used_at TIMESTAMPTZ,
  revoked_at  TIMESTAMPTZ,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX api_keys_merchant_idx ON api_keys (merchant_id) WHERE revoked_at IS NULL;
