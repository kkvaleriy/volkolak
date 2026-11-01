#!/bin/sh
set -eu

NAMESPACE=${DEFAULT_NAMESPACE:-default}
RAW_ADDRESS=${TEMPORAL_ADDRESS:-temporal:7233}
MAX_ATTEMPTS=${TEMPORAL_HEALTH_CHECK_MAX_ATTEMPTS:-60}
SLEEP_SECONDS=${TEMPORAL_HEALTH_CHECK_SLEEP_SECONDS:-5}

SERVER_HOST=$(printf '%s\n' "$RAW_ADDRESS" | cut -d: -f1)
SERVER_PORT=$(printf '%s\n' "$RAW_ADDRESS" | cut -d: -f2)

echo "Waiting for Temporal port ${SERVER_HOST}:${SERVER_PORT}..."
attempt=1
while ! nc -z -w 10 "$SERVER_HOST" "$SERVER_PORT"; do
  if [ "$attempt" -ge "$MAX_ATTEMPTS" ]; then
    echo "Temporal port did not open"
    exit 1
  fi
  attempt=$((attempt + 1))
  sleep "$SLEEP_SECONDS"
done

# CLI Temporal ходит в gRPC через резолвер dns:///. На DNS Docker Desktop этот
# запрос не укладывается в 5 секунд GetSystemInfo, и каждая попытка кончается
# "failed reaching server: context deadline exceeded", хотя порт открыт и UI жив.
# Литеральный IPv4 резолвер не спрашивает. passthrough — запасной путь.
resolve_ipv4() {
  if command -v getent >/dev/null 2>&1; then
    getent hosts "$SERVER_HOST" 2>/dev/null | awk '$1 ~ /^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$/ { print $1; exit }'
    return 0
  fi
  if command -v nslookup >/dev/null 2>&1; then
    nslookup "$SERVER_HOST" 2>/dev/null | awk '/^Address[0-9]*: / { print $2 }' | awk '/^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$/ { print; exit }'
  fi
}

cli_address() {
  ip=$(resolve_ipv4 || true)
  if [ -n "$ip" ]; then
    printf '%s\n' "${ip}:${SERVER_PORT}"
    return 0
  fi
  printf '%s\n' "passthrough:///${SERVER_HOST}:${SERVER_PORT}"
}

attempt=1
while :; do
  CLI_ADDRESS=$(cli_address)
  echo "Temporal cluster health via ${CLI_ADDRESS}"
  if temporal operator cluster health --address "$CLI_ADDRESS"; then
    break
  fi
  if [ "$attempt" -ge "$MAX_ATTEMPTS" ]; then
    echo "Temporal cluster did not become healthy"
    exit 1
  fi
  attempt=$((attempt + 1))
  sleep "$SLEEP_SECONDS"
done

attempt=1
while :; do
  CLI_ADDRESS=$(cli_address)
  if temporal operator namespace describe -n "$NAMESPACE" --address "$CLI_ADDRESS" >/dev/null 2>&1; then
    echo "Namespace ${NAMESPACE} already exists"
    exit 0
  fi
  if temporal operator namespace create -n "$NAMESPACE" --address "$CLI_ADDRESS"; then
    echo "Namespace ${NAMESPACE} created"
    exit 0
  fi
  if [ "$attempt" -ge "$MAX_ATTEMPTS" ]; then
    echo "Failed to create namespace ${NAMESPACE}"
    exit 1
  fi
  attempt=$((attempt + 1))
  sleep "$SLEEP_SECONDS"
done
