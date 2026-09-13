CREATE TABLE charges (
  id            TEXT PRIMARY KEY,
  merchant_id   TEXT NOT NULL REFERENCES merchants(id),
  outlet_id     TEXT REFERENCES outlets(id),
  reference     TEXT NOT NULL,
  description   TEXT NOT NULL,
  amount_minor  BIGINT NOT NULL CHECK (amount_minor > 0),
  currency      TEXT NOT NULL DEFAULT 'IDR' CHECK (currency = 'IDR'),
  status        TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','paid','expired','cancelled')),
  payment_id    TEXT REFERENCES payments(id),
  paid_by       TEXT REFERENCES users(id),
  expires_at    TIMESTAMPTZ NOT NULL,
  paid_at       TIMESTAMPTZ,
  cancelled_at  TIMESTAMPTZ,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (merchant_id, reference),
  CHECK ((status = 'paid') = (payment_id IS NOT NULL))
);
CREATE INDEX charges_merchant_idx ON charges (merchant_id, created_at DESC);
CREATE INDEX charges_open_idx ON charges (expires_at) WHERE status = 'open';
