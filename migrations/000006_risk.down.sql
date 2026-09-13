ALTER TABLE holdbacks DROP CONSTRAINT IF EXISTS holdbacks_consumed_by_fkey;
DROP TABLE IF EXISTS audit_log, disputes, account_blocks, risk_alerts, velocity_rules CASCADE;
