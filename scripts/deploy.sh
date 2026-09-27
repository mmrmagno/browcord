#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/../deploy"

if [[ ! -f .env ]]; then
  echo "deploy: .env missing, copy .env.example and fill it in" >&2
  exit 1
fi

if [[ -z "${COMPOSE_PROFILES:-}" ]] && ! grep -q '^COMPOSE_PROFILES=' .env; then
  export COMPOSE_PROFILES=single
fi
profiles="${COMPOSE_PROFILES:-$(grep '^COMPOSE_PROFILES=' .env | tail -1 | cut -d= -f2- | tr -d "\"' ")}"

multi=false
case ",$profiles," in
  *,multi,*) multi=true ;;
esac

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
  local ref running pulled want have

  if [[ "$(docker inspect --format '{{.State.Running}}' "$container" 2>/dev/null || echo false)" != "true" ]]; then
    return 0
  fi

  ref="$(docker inspect --format '{{.Config.Image}}' "$container" 2>/dev/null || true)"
  running="$(layers_of_container "$container")"
  pulled="$(layers_of_image "$ref")"
  if [[ -z "$ref" || -z "$running" || -z "$pulled" || "$running" != "$pulled" ]]; then
    return 0
  fi

  want="$(docker compose config --hash "$service" | awk -v s="$service" '$1 == s { print $2 }')"
  have="$(docker inspect --format '{{index .Config.Labels "com.docker.compose.config-hash"}}' "$container" 2>/dev/null || true)"
  if [[ -z "$want" || "$want" != "$have" ]]; then
    return 0
  fi

  return 1
}

echo "-> docker compose pull (profiles: $profiles)"
docker compose --profile single pull gateway room

services=(netguard)
pairs=("gateway:browcord-gateway")
if [[ "$multi" == true ]]; then
  services+=(docker-proxy)
  pairs+=("dockerguard:browcord-dockerguard")
else
  pairs+=("room:browcord-room")
fi

for pair in "${pairs[@]}"; do
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

if [[ "$multi" == true ]] && docker inspect browcord-room >/dev/null 2>&1; then
  echo "-> multi room is on, removing the old fixed room container"
  docker compose --profile single rm -sf room
fi

echo "-> pruning dangling images"
docker image prune -f

echo "-> status"
docker compose ps

want='"agents":1'
if [[ "$multi" == true ]]; then
  want='"ok":true'
fi

echo "-> waiting for the gateway ($want)"
for i in $(seq 1 30); do
  health="$(docker exec browcord-gateway wget -qO- http://127.0.0.1:8080/api/health 2>/dev/null || true)"
  case "$health" in
    *"$want"*)
      echo "$health"
      echo "deploy: gateway healthy"
      exit 0
      ;;
  esac
  sleep 3
done

echo "deploy: gateway did not report $want within 90s" >&2
docker compose ps >&2
exit 1
