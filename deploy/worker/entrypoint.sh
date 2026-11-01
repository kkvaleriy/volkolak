#!/bin/sh
# gRPC-клиент воркера собран без CGO. Резолв имени temporal через DNS Docker
# не укладывается в таймаут, и воркер пишет
# "failed reaching server: context deadline exceeded".
# getent пользуется libc и сразу отдаёт IPv4, его резолвер не спрашивает.
set -eu

addr=${TEMPORAL_ADDRESS:-temporal:7233}
host=${addr%:*}
port=${addr##*:}
if [ "$host" = "$addr" ]; then
  port=7233
fi

ip=""
if command -v getent >/dev/null 2>&1; then
  ip=$(getent hosts "$host" 2>/dev/null | awk '$1 ~ /^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$/ { print $1; exit }')
fi
if [ -n "$ip" ]; then
  export TEMPORAL_ADDRESS="${ip}:${port}"
  echo "temporal address ${TEMPORAL_ADDRESS} (${host})"
else
  echo "temporal address ${addr}"
fi

exec /usr/local/bin/worker
