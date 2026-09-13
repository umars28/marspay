#!/usr/bin/env sh
set -eu

PORT="${MARSPAY_PGPORT:-55432}"
DATA_DIR="${MARSPAY_PGDATA:-${TMPDIR:-/tmp}/marspay-pgdata}"
SOCKET_DIR="${MARSPAY_PGSOCKET:-/tmp/marspay-pg}"
DB_NAME=marspay
DB_USER=marspay

usage() {
  echo "usage: $0 {up|down|dsn}"
  echo
  echo "  up    start a throwaway PostgreSQL cluster and print the test DSN"
  echo "  down  stop it and delete the data directory"
  echo "  dsn   print the DSN only"
  exit 64
}

dsn() {
  echo "postgres://${DB_USER}@127.0.0.1:${PORT}/${DB_NAME}?sslmode=disable"
}

up() {
  mkdir -p "$SOCKET_DIR"

  if [ ! -d "$DATA_DIR" ]; then
    initdb -D "$DATA_DIR" -U "$DB_USER" --auth=trust -E UTF8 --locale=C >/dev/null
  fi

  if ! pg_ctl -D "$DATA_DIR" status >/dev/null 2>&1; then
    pg_ctl -D "$DATA_DIR" \
      -o "-p ${PORT} -k ${SOCKET_DIR} -h 127.0.0.1" \
      -l "${DATA_DIR}/server.log" -w start >/dev/null
  fi

  psql -h 127.0.0.1 -p "$PORT" -U "$DB_USER" -d postgres -qtc \
    "SELECT 1 FROM pg_database WHERE datname = '${DB_NAME}'" | grep -q 1 ||
    psql -h 127.0.0.1 -p "$PORT" -U "$DB_USER" -d postgres -qc "CREATE DATABASE ${DB_NAME}"

  echo "cluster ready at $DATA_DIR"
  echo
  echo "  export MARSPAY_TEST_DATABASE_URL=\"$(dsn)\""
  echo "  go test ./..."
}

down() {
  pg_ctl -D "$DATA_DIR" stop -m fast >/dev/null 2>&1 || true
  rm -rf "$DATA_DIR" "$SOCKET_DIR"
  echo "cluster removed"
}

case "${1:-}" in
  up) up ;;
  down) down ;;
  dsn) dsn ;;
  *) usage ;;
esac
