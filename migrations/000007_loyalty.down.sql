ALTER TABLE money_requests DROP CONSTRAINT IF EXISTS money_requests_split_fkey;
DROP TABLE IF EXISTS bill_splits, money_requests, promo_redemptions, promos CASCADE;
DROP VIEW IF EXISTS point_balances;
DROP TABLE IF EXISTS point_entries, point_accounts CASCADE;
