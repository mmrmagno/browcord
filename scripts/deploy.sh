#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/../deploy"

if [[ ! -f .env ]]; then
  echo "deploy: .env missing, copy .env.example and fill it in" >&2
  exit 1
fi

echo "-> docker compose pull"
docker compose pull gateway room

echo "-> docker compose up -d"
docker compose up -d gateway room netguard

echo "-> pruning dangling images"
docker image prune -f

echo "-> status"
docker compose ps

echo "-> waiting for the agent to reconnect"
for i in $(seq 1 30); do
  health="$(docker exec browcord-gateway wget -qO- http://127.0.0.1:8080/api/health 2>/dev/null || true)"
  case "$health" in
    *'"agents":1'*)
      echo "$health"
      echo "deploy: gateway healthy, agent connected"
      exit 0
      ;;
  esac
  sleep 3
done

echo "deploy: agent did not reconnect within 90s" >&2
docker compose ps >&2
exit 1
