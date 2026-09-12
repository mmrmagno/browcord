#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

DURATION="${DURATION:-10}"
SIZE="${SIZE:-1920x1080}"
FPS="${FPS:-30}"
BITRATE="${BITRATE:-4M}"

ffmpeg -hide_banner -loglevel error -y \
  -f lavfi -i "testsrc2=size=${SIZE}:rate=${FPS}" \
  -t "${DURATION}" \
  -c:v libx264 -preset veryfast -tune zerolatency \
  -profile:v baseline -level 4.0 \
  -b:v "${BITRATE}" -maxrate "${BITRATE}" -bufsize 1M \
  -g $((FPS * 2)) -bf 0 \
  -x264-params "annexb=1:repeat-headers=1:scenecut=0" \
  -f h264 assets/testpattern.h264

go run ./cmd/spikegen -in assets/testpattern.h264 -out assets/testpattern.bcs -fps "${FPS}"
