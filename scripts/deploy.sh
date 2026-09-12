#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/../deploy"

if [[ ! -f .env ]]; then
  echo "deploy: .env missing, copy .env.example and fill it in" >&2
  exit 1
fi

echo "-> docker compose pull gateway"
docker compose pull gateway

echo "-> docker compose up -d gateway"
docker compose up -d --no-deps gateway

echo "-> pruning dangling images"
docker image prune -f

echo "-> status"
docker compose ps

echo "-> health"
for i in $(seq 1 15); do
  if docker exec browcord-gateway wget -qO- http://127.0.0.1:8080/api/health 2>/dev/null; then
    echo
    echo "deploy: gateway healthy"
    exit 0
  fi
  sleep 2
done

echo "deploy: gateway did not report healthy within 30s" >&2
exit 1
