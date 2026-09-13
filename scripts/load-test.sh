#!/usr/bin/env sh
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
ADDR="${MARSPAY_LOAD_ADDR:-127.0.0.1:8099}"
RUN_ID="load$(date +%s)"
BIN="${TMPDIR:-/tmp}/marspay-load-server"
LOG="${TMPDIR:-/tmp}/marspay-load-server.log"

USER_PREFIX=usr_load
USERS="${MARSPAY_LOAD_USERS:-500}"
MERCHANT_ID=merch_load

command -v k6 >/dev/null 2>&1 || { echo "k6 is not installed" >&2; exit 1; }

echo "==> starting PostgreSQL and Redis"
sh "$ROOT/scripts/test-db.sh" up >/dev/null
DSN=$(sh "$ROOT/scripts/test-db.sh" dsn)
REDIS=$(sh "$ROOT/scripts/test-db.sh" redis-addr)
REDIS_PORT=${REDIS##*:}

cleanup() {
  [ -n "${SERVER_PID:-}" ] && kill "$SERVER_PID" 2>/dev/null || true
  sh "$ROOT/scripts/test-db.sh" down >/dev/null 2>&1 || true
  rm -f "$BIN"
}
trap cleanup EXIT

echo "==> synchronous_commit is $(psql "$DSN" -tAc 'SHOW synchronous_commit')"

echo "==> applying migrations"
for f in "$ROOT"/migrations/*.down.sql; do psql "$DSN" -q -f "$f" >/dev/null 2>&1 || true; done
for f in "$ROOT"/migrations/*.up.sql; do psql "$DSN" -q -v ON_ERROR_STOP=1 -f "$f" >/dev/null; done

echo "==> seeding ${USERS} consumers, one merchant"
psql "$DSN" -q <<SQL >/dev/null
INSERT INTO users (id, phone, full_name, pin_hash, kyc_tier, status)
SELECT '${USER_PREFIX}_' || i, '0812' || lpad(i::text, 8, '0'),
       'Load ' || i, 'x', 'verified', 'active'
FROM generate_series(0, ${USERS} - 1) AS i;

INSERT INTO merchants (id, legal_name, display_name, category, status, fee_bps)
  VALUES ('${MERCHANT_ID}', 'PT Load', 'Load Merchant', 'food', 'active', 70);

INSERT INTO accounts (id, owner_type, owner_id, account_type)
SELECT 'acc_${USER_PREFIX}_' || i || '_user_wallet', 'user', '${USER_PREFIX}_' || i, 'user_wallet'
FROM generate_series(0, ${USERS} - 1) AS i;

INSERT INTO accounts (id, owner_type, owner_id, account_type) VALUES
  ('acc_${MERCHANT_ID}_merchant_payable', 'merchant', '${MERCHANT_ID}', 'merchant_payable'),
  ('acc_platform_platform_fee_revenue', 'platform', NULL, 'platform_fee_revenue');
SQL

psql "$DSN" -tAc "SELECT 'SET marspay:balance:acc_${USER_PREFIX}_' || i || '_user_wallet 100000000000000'
                  FROM generate_series(0, ${USERS} - 1) AS i" \
  | redis-cli -p "$REDIS_PORT" --pipe >/dev/null 2>&1

if lsof -nP -iTCP:"${ADDR##*:}" -sTCP:LISTEN >/dev/null 2>&1; then
  echo "FAIL: something is already listening on ${ADDR}." >&2
  echo "      Refusing to run: k6 would measure that process, not this build." >&2
  lsof -nP -iTCP:"${ADDR##*:}" -sTCP:LISTEN >&2
  exit 1
fi

echo "==> seeding ${USERS} consumer sessions"
psql "$DSN" -q <<SQL >/dev/null
INSERT INTO devices (id, user_id, platform, model, last_seen_at)
SELECT 'dev_${USER_PREFIX}_' || i, '${USER_PREFIX}_' || i, 'android', 'load-rig', now()
FROM generate_series(0, ${USERS} - 1) AS i;

INSERT INTO sessions (id, user_id, device_id, access_hash, refresh_hash,
                      access_expires_at, refresh_expires_at, last_seen_at)
SELECT 'sess_${USER_PREFIX}_' || i,
       '${USER_PREFIX}_' || i,
       'dev_${USER_PREFIX}_' || i,
       encode(sha256(('mp_at_${USER_PREFIX}_' || i)::bytea), 'hex'),
       encode(sha256(('mp_rt_${USER_PREFIX}_' || i)::bytea), 'hex'),
       now() + interval '2 hours', now() + interval '2 hours', now()
FROM generate_series(0, ${USERS} - 1) AS i;
SQL

echo "==> building and starting the API"
go build -o "$BIN" "$ROOT/cmd/marspay"
MARSPAY_DATABASE_URL="$DSN" MARSPAY_REDIS_ADDR="$REDIS" MARSPAY_ADDR="$ADDR" \
  "$BIN" >"$LOG" 2>&1 &
SERVER_PID=$!

STARTED=""
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    echo "FAIL: the API exited during start-up:" >&2
    cat "$LOG" >&2
    exit 1
  fi
  if grep -q '"msg":"listening"' "$LOG" 2>/dev/null; then
    STARTED=yes
    break
  fi
  sleep 0.4
done

[ -n "$STARTED" ] || { echo "FAIL: the API never reported listening:" >&2; cat "$LOG" >&2; exit 1; }

echo "==> running k6"
echo
MARSPAY_BASE_URL="http://${ADDR}" \
MARSPAY_USER="mp_at_${USER_PREFIX}" \
MARSPAY_USERS="$USERS" \
MARSPAY_MERCHANT="$MERCHANT_ID" \
MARSPAY_RUN_ID="$RUN_ID" \
  k6 run "$ROOT/scripts/load/payments.js"
K6_STATUS=$?

echo
echo "==> checking the books after the run"
psql "$DSN" -c "SELECT
  (SELECT count(*) FROM payments)                           AS payments,
  (SELECT count(*) FROM ledger_transactions)                AS ledger_transactions,
  (SELECT count(*) FROM ledger_entries)                     AS ledger_entries,
  (SELECT count(*) FROM idempotency_keys)                   AS idempotency_keys,
  (SELECT COALESCE(SUM(amount_minor),0) FROM ledger_entries) AS global_sum;"

GLOBAL_SUM=$(psql "$DSN" -tAc "SELECT COALESCE(SUM(amount_minor),0) FROM ledger_entries")
PAYMENTS=$(psql "$DSN" -tAc "SELECT count(*) FROM payments")
TXNS=$(psql "$DSN" -tAc "SELECT count(*) FROM ledger_transactions")

STATUS=0
[ "$GLOBAL_SUM" = "0" ] || { echo "FAIL: global sum is $GLOBAL_SUM, want 0" >&2; STATUS=1; }
[ "$PAYMENTS" = "$TXNS" ] || { echo "FAIL: $PAYMENTS payments but $TXNS ledger transactions" >&2; STATUS=1; }
[ "$PAYMENTS" -gt 0 ] || { echo "FAIL: no payments were recorded" >&2; STATUS=1; }
[ "$K6_STATUS" = "0" ] || { echo "FAIL: k6 thresholds were not met" >&2; STATUS=1; }

[ "$STATUS" = "0" ] && echo "PASS: every request that returned 201 left exactly one balanced ledger transaction"
exit "$STATUS"
