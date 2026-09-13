CREATE TABLE point_accounts (
  id         TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL UNIQUE REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE point_entries (
  id          TEXT PRIMARY KEY,
  account_id  TEXT NOT NULL REFERENCES point_accounts(id),
  amount      BIGINT NOT NULL CHECK (amount <> 0),
  kind        TEXT NOT NULL CHECK (kind IN ('earn','redeem','expire','adjustment')),
  source_type TEXT,
  source_id   TEXT,
  expires_at  TIMESTAMPTZ,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX point_entries_account_idx ON point_entries (account_id, created_at DESC);
CREATE INDEX point_entries_expiry_idx ON point_entries (expires_at)
  WHERE kind = 'earn' AND expires_at IS NOT NULL;

CREATE VIEW point_balances AS
SELECT account_id, SUM(amount) AS balance
FROM point_entries
GROUP BY account_id;

CREATE TABLE promos (
  id             TEXT PRIMARY KEY,
  code           TEXT NOT NULL UNIQUE,
  name           TEXT NOT NULL,
  kind           TEXT NOT NULL CHECK (kind IN ('cashback','discount','point_multiplier')),
  value_bps      INT,
  value_minor    BIGINT,
  max_benefit_minor BIGINT,
  min_spend_minor   BIGINT NOT NULL DEFAULT 0,
  total_quota    INT,
  per_user_quota INT NOT NULL DEFAULT 1,
  starts_at      TIMESTAMPTZ NOT NULL,
  ends_at        TIMESTAMPTZ NOT NULL,
  status         TEXT NOT NULL DEFAULT 'draft' CHECK (status IN (
                   'draft','active','exhausted','ended')),
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (ends_at > starts_at)
);

CREATE TABLE promo_redemptions (
  id           TEXT PRIMARY KEY,
  promo_id     TEXT NOT NULL REFERENCES promos(id),
  user_id      TEXT NOT NULL REFERENCES users(id),
  payment_id   TEXT REFERENCES payments(id),
  benefit_minor BIGINT NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX promo_redemptions_promo_idx ON promo_redemptions (promo_id, created_at DESC);
CREATE UNIQUE INDEX promo_redemptions_payment_idx ON promo_redemptions (payment_id)
  WHERE payment_id IS NOT NULL;

CREATE TABLE money_requests (
  id           TEXT PRIMARY KEY,
  requester_id TEXT NOT NULL REFERENCES users(id),
  payer_id     TEXT NOT NULL REFERENCES users(id),
  split_id     TEXT,
  amount_minor BIGINT NOT NULL CHECK (amount_minor > 0),
  note         TEXT,
  status       TEXT NOT NULL DEFAULT 'pending' CHECK (status IN (
                 'pending','paid','declined','expired','cancelled')),
  transfer_id  TEXT REFERENCES transfers(id),
  expires_at   TIMESTAMPTZ,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (requester_id <> payer_id)
);
CREATE INDEX money_requests_payer_idx ON money_requests (payer_id, created_at DESC);
CREATE INDEX money_requests_expiry_idx ON money_requests (expires_at) WHERE status = 'pending';

CREATE TABLE bill_splits (
  id           TEXT PRIMARY KEY,
  owner_id     TEXT NOT NULL REFERENCES users(id),
  title        TEXT NOT NULL,
  total_minor  BIGINT NOT NULL CHECK (total_minor > 0),
  participants INT NOT NULL CHECK (participants > 1),
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE money_requests
  ADD CONSTRAINT money_requests_split_fkey
  FOREIGN KEY (split_id) REFERENCES bill_splits(id);
