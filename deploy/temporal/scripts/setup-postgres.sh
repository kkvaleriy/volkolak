#!/bin/sh
set -eu

: "${POSTGRES_SEEDS:?ERROR: POSTGRES_SEEDS is required}"
: "${POSTGRES_USER:?ERROR: POSTGRES_USER is required}"
: "${SQL_PASSWORD:?ERROR: SQL_PASSWORD is required}"

DB_PORT="${DB_PORT:-5432}"

echo "Waiting for PostgreSQL ${POSTGRES_SEEDS}:${DB_PORT}..."
attempt=1
while ! nc -z -w 10 "$POSTGRES_SEEDS" "$DB_PORT"; do
  if [ "$attempt" -ge 60 ]; then
    echo "PostgreSQL port did not open"
    exit 1
  fi
  attempt=$((attempt + 1))
  sleep 1
done

sql() {
  temporal-sql-tool \
    --plugin postgres12 \
    --ep "$POSTGRES_SEEDS" \
    -u "$POSTGRES_USER" \
    -p "$DB_PORT" \
    "$@"
}

setup_db() {
  db="$1"
  schema_dir="$2"
  # create и setup-schema падают, если база уже есть. update-schema идемпотентен.
  sql --db "$db" create || true
  sql --db "$db" setup-schema -v 0.0 || true
  sql --db "$db" update-schema -d "$schema_dir"
}

setup_db temporal /etc/temporal/schema/postgresql/v12/temporal/versioned
setup_db temporal_visibility /etc/temporal/schema/postgresql/v12/visibility/versioned

sql --db volkolak create || true

echo "PostgreSQL schema setup complete"
