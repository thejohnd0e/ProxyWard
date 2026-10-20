#!/usr/bin/env bash
# Starts ProxyWard. If no port is chosen yet, picks the first free one from 8080
# and remembers it in .env, so re-running keeps the same port.
set -euo pipefail
cd "$(dirname "$0")"

port_in_use() {
  if command -v ss >/dev/null 2>&1; then
    ss -ltn 2>/dev/null | awk '{print $4}' | grep -Eq "[:.]$1$"
  else
    (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null
  fi
}

touch .env
if ! grep -Eq '^PORT=[0-9]+' .env; then
  port=""
  for p in $(seq 8080 8199); do
    if ! port_in_use "$p"; then port=$p; break; fi
  done
  if [ -z "$port" ]; then
    echo "No free port found in 8080-8199. Set PORT in .env yourself." >&2
    exit 1
  fi
  sed -i '/^#\? \?PORT=/d' .env
  printf 'PORT=%s\n' "$port" >> .env
  echo "Using port $port (saved in .env)."
fi

port=$(grep -E '^PORT=' .env | tail -n1 | cut -d= -f2)
if [ "${DRY_RUN:-}" = 1 ]; then echo "dry run: port=$port"; exit 0; fi

docker compose up -d --build

host=$(hostname -I 2>/dev/null | awk '{print $1}')
echo
echo "ProxyWard is starting. Open http://${host:-<address of this machine>}:${port}/"
echo "First time? Set the administrator password:"
echo "  docker compose run --rm proxyward admin set-password"
