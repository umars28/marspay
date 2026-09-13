#!/usr/bin/env sh
set -eu

PORT="${MARSPAY_CRASH_PORT:-55433}"
DATA_DIR="${MARSPAY_CRASH_PGDATA:-${TMPDIR:-/tmp}/marspay-crash-pgdata}"
SOCKET_DIR="${MARSPAY_CRASH_SOCKET:-/tmp/marspay-crash-pg}"
JOURNAL="${DATA_DIR}/acknowledged.journal"
DURATION="${MARSPAY_CRASH_DURATION:-12s}"
KILL_AFTER="${MARSPAY_CRASH_KILL_AFTER:-5}"
WORKERS="${MARSPAY_CRASH_WORKERS:-16}"
DB=marspay_crash
USER_NAME=marspay

ROOT=$(cd "$(dirname "$0")/.." && pwd)
DSN="postgres://${USER_NAME}@127.0.0.1:${PORT}/${DB}?sslmode=disable"

cleanup() {
  pg_ctl -D "$DATA_DIR" stop -m fast >/dev/null 2>&1 || true
  rm -rf "$DATA_DIR" "$SOCKET_DIR"
}

start_cluster() {
  pg_ctl -D "$DATA_DIR" \
    -o "-p ${PORT} -k ${SOCKET_DIR} -h 127.0.0.1" \
    -l "${DATA_DIR}/server.log" -w start >/dev/null
}

echo "==> preparing a dedicated cluster at $DATA_DIR"
cleanup
mkdir -p "$SOCKET_DIR"
initdb -D "$DATA_DIR" -U "$USER_NAME" --auth=trust -E UTF8 --locale=C >/dev/null

start_cluster
psql -h 127.0.0.1 -p "$PORT" -U "$USER_NAME" -d postgres -qc "CREATE DATABASE ${DB}"

echo "==> synchronous_commit is $(psql "$DSN" -tAc 'SHOW synchronous_commit')"

for f in "$ROOT"/migrations/*.up.sql; do
  psql "$DSN" -q -v ON_ERROR_STOP=1 -f "$f" >/dev/null
done

go run "$ROOT/cmd/crashdriver" -phase=seed -dsn="$DSN" -journal="$JOURNAL" >/dev/null

echo "==> writing for ${DURATION} with ${WORKERS} concurrent writers"
go run "$ROOT/cmd/crashdriver" \
  -phase=load -dsn="$DSN" -journal="$JOURNAL" \
  -workers="$WORKERS" -duration="$DURATION" &
DRIVER_PID=$!

sleep "$KILL_AFTER"

POSTMASTER_PID=$(head -1 "${DATA_DIR}/postmaster.pid")
echo "==> kill -9 ${POSTMASTER_PID} (the postmaster) mid-flight"
kill -9 "$POSTMASTER_PID"

wait "$DRIVER_PID" 2>/dev/null || true
echo "==> driver stopped"

sleep 1
echo "==> restarting; PostgreSQL will replay its write-ahead log"
start_cluster
grep -c "database system was not properly shut down" "${DATA_DIR}/server.log" >/dev/null &&
  echo "==> confirmed: the server performed crash recovery"

echo
echo "==> verifying"
if go run "$ROOT/cmd/crashdriver" -phase=verify -dsn="$DSN" -journal="$JOURNAL"; then
  STATUS=0
else
  STATUS=1
fi

echo
echo "==> tearing the cluster down"
cleanup
exit "$STATUS"
