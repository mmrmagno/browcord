#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/../deploy"

if [[ ! -f .env ]]; then
  echo "deploy: .env missing, copy .env.example and fill it in" >&2
  exit 1
fi

layers_of_image() {
  docker image inspect --format '{{json .RootFS.Layers}}' "$1" 2>/dev/null || true
}

layers_of_container() {
  local image
  image="$(docker inspect --format '{{.Image}}' "$1" 2>/dev/null || true)"
  if [[ -z "$image" ]]; then
    return 0
  fi
  layers_of_image "$image"
}

needs_recreate() {
  local service="$1" container="$2"
  local image running pulled want have state

  if [[ "$(docker inspect --format '{{.State.Running}}' "$container" 2>/dev/null || echo false)" != "true" ]]; then
    return 0
  fi

  image="$(docker compose config --images "$service" | head -1)"
  running="$(layers_of_container "$container")"
  pulled="$(layers_of_image "$image")"
  if [[ -z "$running" || -z "$pulled" || "$running" != "$pulled" ]]; then
    return 0
  fi

  want="$(docker compose config --hash "$service" | awk '{print $2}')"
  have="$(docker inspect --format '{{index .Config.Labels "com.docker.compose.config-hash"}}' "$container" 2>/dev/null || true)"
  if [[ -z "$want" || "$want" != "$have" ]]; then
    return 0
  fi

  return 1
}

echo "-> docker compose pull"
docker compose pull gateway room

services=(netguard)

for pair in "gateway:browcord-gateway" "room:browcord-room"; do
  service="${pair%%:*}"
  container="${pair##*:}"
  if needs_recreate "$service" "$container"; then
    echo "-> $service changed, it will be recreated"
    services+=("$service")
  else
    echo "-> $service is byte identical and its config is unchanged, leaving it running"
  fi
done

echo "-> docker compose up -d ${services[*]}"
docker compose up -d "${services[@]}"

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
