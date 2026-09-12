#!/usr/bin/env bash
set -uo pipefail

IMAGE="${IMAGE:-browcord-room:dev}"
NETWORK="${NETWORK:-browcord-rooms}"
SECCOMP="${SECCOMP:-deploy/seccomp/chromium.json}"

TARGETS_BLOCKED=(
  "169.254.169.254:80"
  "192.168.1.1:80"
  "10.0.0.1:80"
  "172.17.0.1:80"
  "127.0.0.1:81"
  "172.17.0.1:81"
  "172.17.0.1:22"
)
TARGETS_ALLOWED=(
  "1.1.1.1:443"
)

probe() {
  docker run --rm --network "${NETWORK}" \
    --entrypoint bash \
    --cap-drop ALL --security-opt no-new-privileges:true \
    --security-opt "seccomp=${SECCOMP}" \
    "${IMAGE}" -c "timeout 3 bash -c '</dev/tcp/${1%:*}/${1#*:}' 2>/dev/null && echo REACHABLE || echo unreachable"
}

fail=0

echo "must be unreachable from a room:"
for t in "${TARGETS_BLOCKED[@]}"; do
  result="$(probe "$t")"
  printf '  %-22s %s\n' "$t" "$result"
  [ "$result" = "REACHABLE" ] && fail=1
done

echo "must stay reachable (rooms need the internet):"
for t in "${TARGETS_ALLOWED[@]}"; do
  result="$(probe "$t")"
  printf '  %-22s %s\n' "$t" "$result"
  [ "$result" = "REACHABLE" ] || fail=1
done

if [ "$fail" -ne 0 ]; then
  echo
  echo "FAIL: egress policy is not what it should be. Check that browcord-netguard is running."
  exit 1
fi

echo
echo "PASS: rooms reach the internet and nothing private."
