#!/usr/bin/env sh
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
API_ADDR="${MARSPAY_DEMO_ADDR:-127.0.0.1:8080}"
UI_PORT="${MARSPAY_DEMO_UI_PORT:-8932}"
BIN="${TMPDIR:-/tmp}/marspay-demo-server"
LOG="${TMPDIR:-/tmp}/marspay-demo-server.log"

for port in "${API_ADDR##*:}" "$UI_PORT"; do
  if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "FAIL: something is already listening on port ${port}." >&2
    lsof -nP -iTCP:"$port" -sTCP:LISTEN >&2
    exit 1
  fi
done

echo "==> starting PostgreSQL and Redis"
sh "$ROOT/scripts/test-db.sh" up >/dev/null
DSN=$(sh "$ROOT/scripts/test-db.sh" dsn)
REDIS=$(sh "$ROOT/scripts/test-db.sh" redis-addr)

cleanup() {
  [ -n "${SERVER_PID:-}" ] && kill "$SERVER_PID" 2>/dev/null || true
  [ -n "${UI_PID:-}" ] && kill "$UI_PID" 2>/dev/null || true
  echo
  echo "==> stopped. PostgreSQL and Redis are still up; ./scripts/test-db.sh down clears them."
}
trap cleanup EXIT INT TERM

echo "==> applying migrations"
for f in "$ROOT"/migrations/*.up.sql; do
  psql "$DSN" -q -f "$f" >/dev/null 2>&1 || true
done

echo "==> seeding demo data"
go run "$ROOT/cmd/demoseed" -dsn="$DSN" -redis="$REDIS"

go build -o "$BIN" "$ROOT/cmd/marspay"
MARSPAY_DATABASE_URL="$DSN" MARSPAY_REDIS_ADDR="$REDIS" MARSPAY_ADDR="$API_ADDR" \
MARSPAY_REVEAL_OTP=true \
MARSPAY_CORS_ORIGINS="http://127.0.0.1:${UI_PORT},http://localhost:${UI_PORT}" \
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

(cd "$ROOT/mockup" && python3 -m http.server "$UI_PORT" >/dev/null 2>&1) &
UI_PID=$!
sleep 1

cat <<EOF

  Mockup    http://127.0.0.1:${UI_PORT}      clickable, fictional data, not wired to the API
  API       http://${API_ADDR}         real ledger, real sessions, simulated bank rails
  Logs      ${LOG}

  Sign in, then spend:

    CHALLENGE=\$(curl -s -X POST http://${API_ADDR}/v1/auth/otp \\
      -H 'Content-Type: application/json' \\
      -d '{"phone":"081200000001"}')
    echo "\$CHALLENGE"

    curl -s -X POST http://${API_ADDR}/v1/auth/token \\
      -H 'Content-Type: application/json' \\
      -d '{"challenge_id":"<id>","code":"<code>","pin":"294715","platform":"android"}'

    curl -s http://${API_ADDR}/v1/balance -H 'Authorization: Bearer <access_token>'

    curl -s -X POST http://${API_ADDR}/v1/payments \\
      -H 'Authorization: Bearer <access_token>' \\
      -H 'Content-Type: application/json' \\
      -H 'Idempotency-Key: demo-1' \\
      -d '{"merchant_id":"merch_demo","method":"qris","amount":3200000,"currency":"IDR"}'

  Ctrl-C to stop.

EOF

wait "$SERVER_PID"
