DROP MATERIALIZED VIEW IF EXISTS account_balances_snapshot;
DROP VIEW IF EXISTS account_balances;
DROP TRIGGER IF EXISTS ledger_entries_balanced ON ledger_entries;
DROP FUNCTION IF EXISTS assert_transaction_balanced();
DROP TABLE IF EXISTS ledger_entries CASCADE;
DROP TABLE IF EXISTS ledger_transactions CASCADE;
DROP TABLE IF EXISTS accounts CASCADE;
