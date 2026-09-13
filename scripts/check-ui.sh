#!/usr/bin/env sh
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
ADDR="${MARSPAY_UICHECK_ADDR:-127.0.0.1:8081}"
ORIGIN="${MARSPAY_UICHECK_ORIGIN:-http://127.0.0.1:8932}"
BIN="${TMPDIR:-/tmp}/marspay-uicheck-server"
LOG="${TMPDIR:-/tmp}/marspay-uicheck-server.log"

if lsof -nP -iTCP:"${ADDR##*:}" -sTCP:LISTEN >/dev/null 2>&1; then
  echo "FAIL: something is already listening on ${ADDR}." >&2
  exit 1
fi

echo "==> starting PostgreSQL and Redis"
sh "$ROOT/scripts/test-db.sh" up >/dev/null
DSN=$(sh "$ROOT/scripts/test-db.sh" dsn)
REDIS=$(sh "$ROOT/scripts/test-db.sh" redis-addr)

cleanup() {
  [ -n "${SERVER_PID:-}" ] && kill "$SERVER_PID" 2>/dev/null || true
  rm -f "$BIN"
}
trap cleanup EXIT

for f in "$ROOT"/migrations/*.down.sql; do psql "$DSN" -q -f "$f" >/dev/null 2>&1 || true; done
for f in "$ROOT"/migrations/*.up.sql; do psql "$DSN" -q -v ON_ERROR_STOP=1 -f "$f" >/dev/null; done

go run "$ROOT/cmd/demoseed" -dsn="$DSN" -redis="$REDIS" >/dev/null

go build -o "$BIN" "$ROOT/cmd/marspay"
MARSPAY_DATABASE_URL="$DSN" MARSPAY_REDIS_ADDR="$REDIS" MARSPAY_ADDR="$ADDR" \
MARSPAY_REVEAL_OTP=true MARSPAY_CORS_ORIGINS="$ORIGIN" \
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

echo
go run "$ROOT/cmd/uicheck" -base="http://${ADDR}" -origin="$ORIGIN"
