#!/usr/bin/env bash
set -euo pipefail

WIDTH="${ROOM_WIDTH:-1920}"
HEIGHT="${ROOM_HEIGHT:-1080}"
FPS="${ROOM_FPS:-30}"
BITRATE="${ROOM_BITRATE_KBPS:-4000}"
START_URL="${ROOM_START_URL:-about:blank}"

cleanup() {
  jobs -p | xargs -r kill 2>/dev/null || true
}
trap cleanup EXIT

mkdir -p /tmp/pulse /tmp/runtime
chmod 0700 /tmp/runtime

Xvfb "${DISPLAY}" -screen 0 "${WIDTH}x${HEIGHT}x24" -nolisten tcp -noreset +extension RANDR &

for _ in $(seq 1 50); do
  if xdpyinfo -display "${DISPLAY}" >/dev/null 2>&1; then break; fi
  sleep 0.1
done
xdpyinfo -display "${DISPLAY}" >/dev/null 2>&1 || { echo "Xvfb failed to start" >&2; exit 1; }

env -u PULSE_SERVER pulseaudio \
  --start \
  --exit-idle-time=-1 \
  --disallow-exit \
  --disallow-module-loading=false \
  --load="module-native-protocol-unix socket=/tmp/pulse/native auth-anonymous=1" \
  --load="module-null-sink sink_name=browcord sink_properties=device.description=browcord" \
  --log-target=stderr

pactl set-default-sink browcord

chromium \
  --user-data-dir=/profile \
  --window-position=0,0 \
  --window-size="${WIDTH},${HEIGHT}" \
  --kiosk \
  --block-new-web-contents \
  --no-first-run \
  --no-default-browser-check \
  --disable-sync \
  --disable-features=Translate,TranslateUI,AutofillServerCommunication,PasswordManagerOnboarding \
  --password-store=basic \
  --use-fake-ui-for-media-stream \
  --autoplay-policy=no-user-gesture-required \
  --remote-debugging-address=127.0.0.1 \
  --remote-debugging-port=9222 \
  --remote-allow-origins=http://127.0.0.1:9222 \
  "${START_URL}" &

sleep 3

browcord-agent &

status=0
wait -n || status=$?

echo "room: a supervised process exited with status ${status}, stopping the container so it restarts" >&2
exit 1
