\set ON_ERROR_STOP off

INSERT INTO accounts (id, owner_type, owner_id, account_type) VALUES
  ('acc_wallet_1',  'user',     'usr_44182',  'user_wallet'),
  ('acc_payable_1', 'merchant', 'merch_8812', 'merchant_payable'),
  ('acc_fee',       'platform', NULL,         'platform_fee_revenue');

BEGIN;
INSERT INTO ledger_transactions (id, kind, description)
  VALUES ('txn_balanced', 'payment', 'Rp 32.000 QRIS payment with 0.7 percent fee');
INSERT INTO ledger_entries (id, transaction_id, account_id, amount_minor) VALUES
  ('led_b1', 'txn_balanced', 'acc_wallet_1',  -3200000),
  ('led_b2', 'txn_balanced', 'acc_payable_1',  3177600),
  ('led_b3', 'txn_balanced', 'acc_fee',          22400);
COMMIT;

BEGIN;
INSERT INTO ledger_transactions (id, kind, description)
  VALUES ('txn_unbalanced', 'payment', 'this must be rejected at commit');
INSERT INTO ledger_entries (id, transaction_id, account_id, amount_minor) VALUES
  ('led_u1', 'txn_unbalanced', 'acc_wallet_1', -3200000),
  ('led_u2', 'txn_unbalanced', 'acc_payable_1', 3100000);
COMMIT;

SELECT
  (SELECT count(*) FROM ledger_entries WHERE transaction_id = 'txn_balanced')   AS balanced_rows_kept,
  (SELECT count(*) FROM ledger_entries WHERE transaction_id = 'txn_unbalanced') AS unbalanced_rows_kept,
  (SELECT COALESCE(SUM(amount_minor), 0) FROM ledger_entries)                   AS global_sum;
