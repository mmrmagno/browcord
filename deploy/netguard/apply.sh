#!/usr/bin/env bash
set -euo pipefail

ROOM_SUBNET="${ROOM_SUBNET:?ROOM_SUBNET required}"
GATEWAY_IP="${GATEWAY_IP:-}"
AGENT_PORT="${AGENT_PORT:-7000}"
CHAIN="${CHAIN:-BROWCORD-EGRESS}"
INPUT_CHAIN="${INPUT_CHAIN:-BROWCORD-INPUT}"
REAPPLY_SECONDS="${REAPPLY_SECONDS:-300}"

BLOCKED=(
  10.0.0.0/8
  172.16.0.0/12
  192.168.0.0/16
  127.0.0.0/8
  169.254.0.0/16
  100.64.0.0/10
  192.0.0.0/24
  198.18.0.0/15
  224.0.0.0/4
  240.0.0.0/4
)

apply() {
  iptables -N "${CHAIN}" 2>/dev/null || iptables -F "${CHAIN}"

  iptables -A "${CHAIN}" -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
  if [ -n "${GATEWAY_IP}" ]; then
    iptables -A "${CHAIN}" -s "${ROOM_SUBNET}" -d "${GATEWAY_IP}" -p tcp --dport "${AGENT_PORT}" -j RETURN
  fi
  for net in "${BLOCKED[@]}"; do
    iptables -A "${CHAIN}" -s "${ROOM_SUBNET}" -d "${net}" -j DROP
  done
  iptables -A "${CHAIN}" -s "${ROOM_SUBNET}" -d "${ROOM_SUBNET}" -j DROP

  iptables -C DOCKER-USER -j "${CHAIN}" 2>/dev/null || iptables -I DOCKER-USER 1 -j "${CHAIN}"

  iptables -N "${INPUT_CHAIN}" 2>/dev/null || iptables -F "${INPUT_CHAIN}"
  iptables -A "${INPUT_CHAIN}" -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
  iptables -A "${INPUT_CHAIN}" -s "${ROOM_SUBNET}" -j DROP
  iptables -C INPUT -j "${INPUT_CHAIN}" 2>/dev/null || iptables -I INPUT 1 -j "${INPUT_CHAIN}"
}

cleanup() {
  iptables -D DOCKER-USER -j "${CHAIN}" 2>/dev/null || true
  iptables -F "${CHAIN}" 2>/dev/null || true
  iptables -X "${CHAIN}" 2>/dev/null || true
  iptables -D INPUT -j "${INPUT_CHAIN}" 2>/dev/null || true
  iptables -F "${INPUT_CHAIN}" 2>/dev/null || true
  iptables -X "${INPUT_CHAIN}" 2>/dev/null || true
  echo "netguard: rules removed"
  exit 0
}

trap cleanup TERM INT

apply
echo "netguard: egress policy active for ${ROOM_SUBNET}"

while true; do
  sleep "${REAPPLY_SECONDS}" &
  wait $!
  apply
done
