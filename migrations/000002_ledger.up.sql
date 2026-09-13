CREATE TABLE accounts (
  id           TEXT PRIMARY KEY,
  owner_type   TEXT NOT NULL CHECK (owner_type IN ('user','merchant','platform','provider')),
  owner_id     TEXT,
  account_type TEXT NOT NULL CHECK (account_type IN (
                 'user_wallet','user_hold','merchant_payable','merchant_holdback',
                 'platform_fee_revenue','platform_float','provider_clearing')),
  currency     TEXT NOT NULL DEFAULT 'IDR' CHECK (currency = 'IDR'),
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (owner_type, owner_id, account_type)
);

CREATE TABLE ledger_transactions (
  id           TEXT PRIMARY KEY,
  kind         TEXT NOT NULL CHECK (kind IN (
                 'topup','payment','transfer','withdrawal','bill_payment',
                 'refund','reversal','payout','holdback_release','fee','adjustment')),
  reference_id TEXT,
  description  TEXT,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  metadata     JSONB NOT NULL DEFAULT '{}'
);
CREATE INDEX ledger_transactions_ref_idx ON ledger_transactions (reference_id);

CREATE TABLE ledger_entries (
  id             TEXT NOT NULL,
  transaction_id TEXT NOT NULL REFERENCES ledger_transactions(id),
  account_id     TEXT NOT NULL REFERENCES accounts(id),
  amount_minor   BIGINT NOT NULL CHECK (amount_minor <> 0),
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE TABLE ledger_entries_2026_09 PARTITION OF ledger_entries
  FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');
CREATE TABLE ledger_entries_2026_10 PARTITION OF ledger_entries
  FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');
CREATE TABLE ledger_entries_2026_11 PARTITION OF ledger_entries
  FOR VALUES FROM ('2026-11-01') TO ('2026-12-01');
CREATE TABLE ledger_entries_2026_12 PARTITION OF ledger_entries
  FOR VALUES FROM ('2026-12-01') TO ('2027-01-01');
CREATE TABLE ledger_entries_default PARTITION OF ledger_entries DEFAULT;

CREATE INDEX ledger_entries_txn_idx ON ledger_entries (transaction_id);
CREATE INDEX ledger_entries_account_idx ON ledger_entries (account_id, created_at);

CREATE FUNCTION assert_transaction_balanced() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  total BIGINT;
BEGIN
  SELECT COALESCE(SUM(amount_minor), 0) INTO total
  FROM ledger_entries
  WHERE transaction_id = NEW.transaction_id;

  IF total <> 0 THEN
    RAISE EXCEPTION 'ledger transaction % is unbalanced: sum = %', NEW.transaction_id, total
      USING ERRCODE = 'check_violation';
  END IF;

  RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER ledger_entries_balanced
  AFTER INSERT ON ledger_entries
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION assert_transaction_balanced();

CREATE VIEW account_balances AS
SELECT account_id, SUM(amount_minor) AS balance_minor
FROM ledger_entries
GROUP BY account_id;

CREATE MATERIALIZED VIEW account_balances_snapshot AS
SELECT account_id, SUM(amount_minor) AS balance_minor, now() AS refreshed_at
FROM ledger_entries
GROUP BY account_id;
CREATE UNIQUE INDEX account_balances_snapshot_pk ON account_balances_snapshot (account_id);
