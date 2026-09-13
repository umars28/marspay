#!/usr/bin/env sh
set -eu

PG_PORT="${MARSPAY_PGPORT:-55432}"
PG_DATA="${MARSPAY_PGDATA:-${TMPDIR:-/tmp}/marspay-pgdata}"
PG_SOCKET="${MARSPAY_PGSOCKET:-/tmp/marspay-pg}"
DB_NAME=marspay
DB_USER=marspay

REDIS_PORT="${MARSPAY_REDISPORT:-6399}"
REDIS_DIR="${MARSPAY_REDISDIR:-${TMPDIR:-/tmp}/marspay-redis}"

KAFKA_PORT="${MARSPAY_KAFKAPORT:-19092}"
KAFKA_CONTAINER="${MARSPAY_KAFKA_CONTAINER:-marspay-redpanda}"
KAFKA_IMAGE="${MARSPAY_KAFKA_IMAGE:-docker.redpanda.com/redpandadata/redpanda:v24.2.7}"

usage() {
  echo "usage: $0 {up|down|env|dsn|redis-addr|kafka-brokers}"
  echo
  echo "  up             start throwaway PostgreSQL, Redis and Redpanda, print the exports"
  echo "  down           stop all three and delete their data"
  echo "  env            print the export lines only"
  echo "  dsn            print the PostgreSQL DSN only"
  echo "  redis-addr     print the Redis address only"
  echo "  kafka-brokers  print the Kafka broker list only"
  echo
  echo "Redpanda needs Docker. If it is not running, PostgreSQL and Redis still come up"
  echo "and the Kafka tests skip themselves."
  exit 64
}

dsn() {
  echo "postgres://${DB_USER}@127.0.0.1:${PG_PORT}/${DB_NAME}?sslmode=disable"
}

redis_addr() {
  echo "127.0.0.1:${REDIS_PORT}"
}

kafka_brokers() {
  echo "127.0.0.1:${KAFKA_PORT}"
}

print_env() {
  echo "export MARSPAY_TEST_DATABASE_URL=\"$(dsn)\""
  echo "export MARSPAY_TEST_REDIS_ADDR=\"$(redis_addr)\""
  if kafka_running; then
    echo "export MARSPAY_TEST_KAFKA_BROKERS=\"$(kafka_brokers)\""
  fi
}

kafka_running() {
  docker ps --filter "name=^/${KAFKA_CONTAINER}$" --filter "status=running" \
    --format '{{.Names}}' 2>/dev/null | grep -q "$KAFKA_CONTAINER"
}

kafka_up() {
  if ! docker info >/dev/null 2>&1; then
    echo "redpanda  skipped, Docker is not running"
    return
  fi

  if kafka_running; then
    return
  fi

  docker rm -f "$KAFKA_CONTAINER" >/dev/null 2>&1 || true
  docker run -d --name "$KAFKA_CONTAINER" \
    -p "${KAFKA_PORT}:${KAFKA_PORT}" \
    "$KAFKA_IMAGE" \
    redpanda start \
      --overprovisioned --smp 1 --memory 512M --reserve-memory 0M \
      --node-id 0 --check=false \
      --kafka-addr "external://0.0.0.0:${KAFKA_PORT}" \
      --advertise-kafka-addr "external://127.0.0.1:${KAFKA_PORT}" >/dev/null

  for _ in $(seq 1 40); do
    if docker exec "$KAFKA_CONTAINER" rpk cluster info \
         --brokers "127.0.0.1:${KAFKA_PORT}" >/dev/null 2>&1; then
      return
    fi
    sleep 0.5
  done

  echo "redpanda did not become ready; see: docker logs $KAFKA_CONTAINER" >&2
}

kafka_down() {
  docker rm -f "$KAFKA_CONTAINER" >/dev/null 2>&1 || true
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
  kafka_up
  echo "postgres ready on 127.0.0.1:${PG_PORT}"
  echo "redis    ready on 127.0.0.1:${REDIS_PORT}"
  if kafka_running; then
    echo "redpanda ready on 127.0.0.1:${KAFKA_PORT}"
  fi
  echo
  print_env
  echo "  go test ./..."
}

down() {
  pg_ctl -D "$PG_DATA" stop -m fast >/dev/null 2>&1 || true
  redis-cli -p "$REDIS_PORT" shutdown nosave >/dev/null 2>&1 || true
  kafka_down
  rm -rf "$PG_DATA" "$PG_SOCKET" "$REDIS_DIR"
  echo "postgres, redis and redpanda removed"
}

case "${1:-}" in
  up) up ;;
  down) down ;;
  env) print_env ;;
  dsn) dsn ;;
  redis-addr) redis_addr ;;
  kafka-brokers) kafka_brokers ;;
  *) usage ;;
esac
