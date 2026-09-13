#!/usr/bin/env sh
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
ADDR="${MARSPAY_STRESS_ADDR:-127.0.0.1:8099}"
RUN_ID="stress$(date +%s)"
LEVELS="${MARSPAY_STRESS_LEVELS:-1,2,4,8,16,32,64,128,256,384}"
DWELL="${MARSPAY_STRESS_DWELL:-6s}"
BIN="${TMPDIR:-/tmp}/marspay-stress-server"
LOG="${TMPDIR:-/tmp}/marspay-stress-server.log"

USER_PREFIX=usr_load
USERS="${MARSPAY_STRESS_USERS:-500}"
MERCHANT_ID=merch_load

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
echo "==> postgres max_connections is $(psql "$DSN" -tAc 'SHOW max_connections')"

for f in "$ROOT"/migrations/*.down.sql; do psql "$DSN" -q -f "$f" >/dev/null 2>&1 || true; done
for f in "$ROOT"/migrations/*.up.sql; do psql "$DSN" -q -v ON_ERROR_STOP=1 -f "$f" >/dev/null; done

echo "==> seeding ${USERS} consumers, one merchant"
psql "$DSN" -q <<SQL >/dev/null
INSERT INTO users (id, phone, full_name, pin_hash, kyc_tier, status)
SELECT '${USER_PREFIX}_' || i, '0812' || lpad(i::text, 8, '0'),
       'Stress ' || i, 'x', 'verified', 'active'
FROM generate_series(0, ${USERS} - 1) AS i;

INSERT INTO merchants (id, legal_name, display_name, category, status, fee_bps)
  VALUES ('${MERCHANT_ID}', 'PT Stress', 'Stress Merchant', 'food', 'active', 70);

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
  echo "      Refusing to run: the ramp would measure that process, not this build." >&2
  lsof -nP -iTCP:"${ADDR##*:}" -sTCP:LISTEN >&2
  exit 1
fi

go build -o "$BIN" "$ROOT/cmd/marspay"
MARSPAY_DATABASE_URL="$DSN" MARSPAY_REDIS_ADDR="$REDIS" MARSPAY_ADDR="$ADDR" \
MARSPAY_MAX_IN_FLIGHT="${MARSPAY_MAX_IN_FLIGHT:-}" \
MARSPAY_ADMISSION_QUEUE="${MARSPAY_ADMISSION_QUEUE:-}" \
MARSPAY_ADMISSION_MAX_WAIT="${MARSPAY_ADMISSION_MAX_WAIT:-}" \
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

echo "==> $(grep -o '"msg":"admission control"[^}]*' "$LOG" || echo 'admission control is not configured')"

echo
go run "$ROOT/cmd/stressdriver" \
  -base="http://${ADDR}" -user="$USER_PREFIX" -users="$USERS" -merchant="$MERCHANT_ID" \
  -run="$RUN_ID" -levels="$LEVELS" -dwell="$DWELL" -backoff="${MARSPAY_STRESS_BACKOFF:-0s}"

echo
echo "==> the books after the ramp"
psql "$DSN" -c "SELECT
  (SELECT count(*) FROM payments)                            AS payments,
  (SELECT count(*) FROM ledger_transactions)                 AS ledger_transactions,
  (SELECT count(*) FROM ledger_entries)                      AS ledger_entries,
  (SELECT COALESCE(SUM(amount_minor),0) FROM ledger_entries) AS global_sum;"

GLOBAL_SUM=$(psql "$DSN" -tAc "SELECT COALESCE(SUM(amount_minor),0) FROM ledger_entries")
PAYMENTS=$(psql "$DSN" -tAc "SELECT count(*) FROM payments")
TXNS=$(psql "$DSN" -tAc "SELECT count(*) FROM ledger_transactions")

STATUS=0
[ "$GLOBAL_SUM" = "0" ] || { echo "FAIL: global sum is $GLOBAL_SUM, want 0" >&2; STATUS=1; }
[ "$PAYMENTS" = "$TXNS" ] || { echo "FAIL: $PAYMENTS payments but $TXNS ledger transactions" >&2; STATUS=1; }

[ "$STATUS" = "0" ] && echo "PASS: the books stayed balanced through saturation"
exit "$STATUS"
