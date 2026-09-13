#!/usr/bin/env sh
set -eu

PG_PORT="${MARSPAY_PGPORT:-55432}"
PG_DATA="${MARSPAY_PGDATA:-${TMPDIR:-/tmp}/marspay-pgdata}"
PG_SOCKET="${MARSPAY_PGSOCKET:-/tmp/marspay-pg}"
DB_NAME=marspay
DB_USER=marspay

REDIS_PORT="${MARSPAY_REDISPORT:-6399}"
REDIS_DIR="${MARSPAY_REDISDIR:-${TMPDIR:-/tmp}/marspay-redis}"

usage() {
  echo "usage: $0 {up|down|env|dsn|redis-addr}"
  echo
  echo "  up          start throwaway PostgreSQL and Redis, print the exports"
  echo "  down        stop both and delete their data directories"
  echo "  env         print the export lines only"
  echo "  dsn         print the PostgreSQL DSN only"
  echo "  redis-addr  print the Redis address only"
  exit 64
}

dsn() {
  echo "postgres://${DB_USER}@127.0.0.1:${PG_PORT}/${DB_NAME}?sslmode=disable"
}

redis_addr() {
  echo "127.0.0.1:${REDIS_PORT}"
}

print_env() {
  echo "export MARSPAY_TEST_DATABASE_URL=\"$(dsn)\""
  echo "export MARSPAY_TEST_REDIS_ADDR=\"$(redis_addr)\""
}

postgres_up() {
  mkdir -p "$PG_SOCKET"

  if [ ! -d "$PG_DATA" ]; then
    initdb -D "$PG_DATA" -U "$DB_USER" --auth=trust -E UTF8 --locale=C >/dev/null
  fi

  if ! pg_ctl -D "$PG_DATA" status >/dev/null 2>&1; then
    pg_ctl -D "$PG_DATA" \
      -o "-p ${PG_PORT} -k ${PG_SOCKET} -h 127.0.0.1" \
      -l "${PG_DATA}/server.log" -w start >/dev/null
  fi

  psql -h 127.0.0.1 -p "$PG_PORT" -U "$DB_USER" -d postgres -qtc \
    "SELECT 1 FROM pg_database WHERE datname = '${DB_NAME}'" | grep -q 1 ||
    psql -h 127.0.0.1 -p "$PG_PORT" -U "$DB_USER" -d postgres -qc "CREATE DATABASE ${DB_NAME}"
}

redis_up() {
  mkdir -p "$REDIS_DIR"

  if redis-cli -p "$REDIS_PORT" ping >/dev/null 2>&1; then
    return
  fi

  redis-server \
    --port "$REDIS_PORT" \
    --bind 127.0.0.1 \
    --dir "$REDIS_DIR" \
    --save '' \
    --appendonly no \
    --daemonize yes \
    --pidfile "${REDIS_DIR}/redis.pid" \
    --logfile "${REDIS_DIR}/redis.log"

  for _ in 1 2 3 4 5 6 7 8 9 10; do
    redis-cli -p "$REDIS_PORT" ping >/dev/null 2>&1 && return
    sleep 0.3
  done

  echo "redis did not start; see ${REDIS_DIR}/redis.log" >&2
  exit 1
}

up() {
  postgres_up
  redis_up
  echo "postgres ready on 127.0.0.1:${PG_PORT}"
  echo "redis    ready on 127.0.0.1:${REDIS_PORT}"
  echo
  print_env
  echo "  go test ./..."
}

down() {
  pg_ctl -D "$PG_DATA" stop -m fast >/dev/null 2>&1 || true
  redis-cli -p "$REDIS_PORT" shutdown nosave >/dev/null 2>&1 || true
  rm -rf "$PG_DATA" "$PG_SOCKET" "$REDIS_DIR"
  echo "postgres and redis removed"
}

case "${1:-}" in
  up) up ;;
  down) down ;;
  env) print_env ;;
  dsn) dsn ;;
  redis-addr) redis_addr ;;
  *) usage ;;
esac
