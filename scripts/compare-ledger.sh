#!/usr/bin/env sh
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
OPS="${MARSPAY_BENCH_OPS:-4000}"
CONCURRENCY="${MARSPAY_BENCH_CONCURRENCY:-32}"
SPREAD="${MARSPAY_BENCH_SPREAD:-200}"

echo "==> starting PostgreSQL"
sh "$ROOT/scripts/test-db.sh" up >/dev/null
DSN=$(sh "$ROOT/scripts/test-db.sh" dsn)

cleanup() {
  sh "$ROOT/scripts/test-db.sh" down >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "==> synchronous_commit is $(psql "$DSN" -tAc 'SHOW synchronous_commit')"

for f in "$ROOT"/migrations/*.down.sql; do psql "$DSN" -q -f "$f" >/dev/null 2>&1 || true; done
for f in "$ROOT"/migrations/*.up.sql; do psql "$DSN" -q -v ON_ERROR_STOP=1 -f "$f" >/dev/null; done

echo
go run "$ROOT/cmd/ledgerbench" \
  -dsn="$DSN" -ops="$OPS" -concurrency="$CONCURRENCY" -spread="$SPREAD"
