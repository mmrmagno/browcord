<p align="center">
  <img src="assets/icon.svg" alt="browcord" width="140"/>
</p>

<h1 align="center">browcord</h1>

<p align="center">
  A shared web browser that lives inside a Discord voice channel.
  <br/>
  One real Chromium runs on your server, everyone sees the same screen, and anyone can take the controls.
</p>

<p align="center">
  <a href="https://github.com/mmrmagno/browcord/actions/workflows/ci.yml">
    <img src="https://github.com/mmrmagno/browcord/actions/workflows/ci.yml/badge.svg" alt="CI"/>
  </a>
  <img src="https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white" alt="Go"/>
  <img src="https://img.shields.io/badge/Discord-Activity-5865F2?logo=discord&logoColor=white" alt="Discord Activity"/>
  <img src="https://img.shields.io/badge/video-VP8%20%2B%20Opus-6d79f7" alt="Codecs"/>
  <img src="https://img.shields.io/badge/license-AGPL--3.0-blue" alt="License"/>
</p>

Watch something together, go through docs together, or gang up on the same pull
request. The browser runs on the server, so nobody has to share their screen and
nobody needs a good connection to be the one hosting. There is also no "can you
scroll down a bit", because whoever wants the mouse already has it.

## What it does

It launches from a voice channel as a Discord Activity. Login is Discord OAuth
with a guild allow list, so only your servers can open it.

Inside, it is a whole browser. Mouse, scroll, keyboard, back, forward and a URL
bar, all shared, with a typing token so two people cannot fight over the same
text box. You can see where everyone else is pointing and who else is in the
room. Any site that works in Chrome works, because it is Chrome.

uBlock Origin Lite is installed through enterprise policy, and the agent keeps
exactly one tab open, so popups get closed instead of quietly stealing the input.

## How it works

```
Discord client
  └── iframe  {clientId}.discordsays.com
        └── web client (TypeScript, WebCodecs)
              ├── /ws/media   binary, server to client   VP8 + Opus chunks
              └── /ws/ctl     JSON, both ways            input, cursors, presence, nav
                    │
              ───── reverse proxy terminates TLS ─────
                    │
        browcord-gateway    auth, rooms, websocket fan out
                    │       the only container the proxy can see
                    │       (the room dials out to it, never the reverse)
        browcord-room       no published ports, isolated network
              Xvfb + PulseAudio + Chromium + GStreamer + agent
```

The room captures the X display with GStreamer, encodes to VP8 and Opus, and
pushes length prefixed chunks to the gateway. The gateway fans them out to every
viewer and replays a cached keyframe to late joiners, so someone arriving halfway
through sees a picture instead of a black screen. Input travels the other way and
is applied through the Chrome DevTools Protocol.

Playback is less obvious than drawing frames as they arrive. Audio is scheduled
against a video presentation clock, both jitter buffers size themselves from
measured network jitter, and frames go through a pace queue that presents the one
nearest the upcoming refresh. Drawing on arrival maps every network hiccup
straight onto the screen.

VP8 is deliberate. Discord's client cannot decode H.264 through WebCodecs:
`isConfigSupported()` returns true and `configure()` then fails for every profile.
Do not switch the encoder back to H.264 expecting hardware acceleration to help.

## Requirements

| Thing | Why |
|---|---|
| A Linux host with Docker and Compose | Runs the gateway and the room |
| A reverse proxy with TLS | Discord will only load an Activity over HTTPS |
| A Discord application with Activities enabled | Provides OAuth and the iframe |
| About 2 CPU cores and 3 GB RAM for the room | Chromium plus a 720p encoder |

The room image must be Debian trixie or newer. `go-glib` needs GLib 2.76 or
later, and bookworm ships 2.74, which fails to build the bindings.

## Quick start

```sh
git clone https://github.com/mmrmagno/browcord.git
cd browcord/deploy
cp .env.example .env
```

Fill in `.env`:

| Variable | What it is |
|---|---|
| `DISCORD_CLIENT_ID` | Application ID from the Discord developer portal |
| `DISCORD_CLIENT_SECRET` | OAuth client secret |
| `BROWCORD_ALLOW_GUILDS` | Comma separated guild IDs allowed to open the room |
| `BROWCORD_AGENT_TOKEN` | Shared secret the room uses to reach the gateway |
| `BROWCORD_SECRET` | Key for signing viewer sessions |
| `BROWCORD_SESSION_TTL` | How long a viewer session lasts, defaults to 8h |
| `ROOM_START_URL` | Page the room opens on |
| `ROOM_WIDTH`, `ROOM_HEIGHT` | Capture resolution, defaults to 1280x720 |

Generate the two secrets with something like `openssl rand -hex 32`.

Then bring it up. This builds both images locally and starts the gateway, the
room and the egress filter:

```sh
docker compose up -d --build
```

Check the room really is fenced in before you invite anyone:

```sh
../deploy/verify-egress.sh
```

`netguard` runs as a service rather than a one-shot script, so the rules are
reapplied every five minutes and survive reboots and Docker restarts. Do not run
`netguard/apply.sh` by hand: without `GATEWAY_IP` and `AGENT_PORT` it drops the
room's connection to the gateway and the stream dies.

Point your reverse proxy at the gateway, set the Activity URL mapping in the
Discord developer portal to the same host, and launch it from a voice channel.

### Checking it is alive

```sh
docker exec browcord-gateway wget -qO- http://127.0.0.1:8080/api/health
```

`/api/health` is unauthenticated, because the container healthcheck uses it, so
it reports only `ok`, `rooms` and `agents`. `agents` should be 1. Per room
detail lives behind the agent token, since room ids are Discord activity
instance ids and viewer counts are nobody else's business:

```sh
docker exec browcord-gateway sh -c 'wget -qO- \
  --header="Authorization: Bearer $BROWCORD_AGENT_TOKEN" \
  http://127.0.0.1:8080/api/stats'
```

The quoting matters: the token lives in the container's environment, not your
shell, so expanding it inside `sh -c` keeps it out of your history and out of
`ps` on the host.

There `secondsSinceChunk` should be 0 and `hasVideo` true. There is no browser
console inside the Discord client, so the web client reports its diagnostics
server side instead:

```sh
docker compose logs gateway | grep client
```

## Security

A room runs whatever anyone in it types. Two controls follow from that, and
neither is optional.

### The egress filter

`deploy/netguard/apply.sh` is the most important control in the project. Without
it, anyone in the room can type `http://169.254.169.254/` or
`http://192.168.1.1/` and turn the shared browser into a probe pointed at your
cloud metadata endpoint and your LAN.

Filtering happens by IP, in the `DOCKER-USER` chain, not by hostname. A hostname
deny list is trivially defeated by a domain that resolves to a private address on
its second lookup. A packet filter is not. The check in `internal/cdp` is defence
in depth and a friendly error message, not the control.

Re-run the script after any change to the compose file or the networks, then
check from inside a live room that the metadata endpoint, the LAN gateway and the
host's own ports are all unreachable.

### The seccomp profile

`deploy/seccomp/chromium.json` is what lets Chromium keep its own sandbox while
the container still runs with `cap_drop: ALL` and `no-new-privileges`.

Docker's default profile gates `clone`, `unshare`, `mount` and `umount2` behind
`CAP_SYS_ADMIN` and denies `pivot_root` outright. Drop capabilities without
replacing the profile and Chromium reports "No usable sandbox" and dies with
"Zygote process exited prematurely". The tempting fix at that point is
`--no-sandbox`, which converts any renderer bug into code execution as the
container user. That is the worst outcome available here, so it is not an option.

The profile is Docker's default plus exactly one rule allowing `clone`,
`unshare`, `mount`, `umount2`, `pivot_root` and `chroot`, so the browser can
build its own namespace sandbox unprivileged. `bpf`, `perf_event_open`, `setns`
and the rest of the `CAP_SYS_ADMIN` group stay denied.

This is a deliberate trade. Permitting unprivileged user namespaces widens the
host syscall surface slightly, in exchange for keeping the much stronger boundary
that is Chromium's own multi process sandbox. On a host with gVisor, running
rooms under `runsc` removes the need for the trade entirely.

## Repository layout

```
cmd/browcord-gateway   the public service: auth, rooms, websocket fan out
cmd/browcord-agent     runs inside a room container: Chromium, capture, input
cmd/capturedump        runs the capture pipeline to a file, for verification

internal/wire      chunk codec (type, PTS, length) and control message schema
internal/capture   GStreamer pipeline to wire chunks, via go-gst appsink
internal/cdp       Chromium control: input, navigation, address space deny list
internal/room      fan out, late joiner cache, typing token, cursors, stats
internal/gateway   http and websocket endpoints, auth, input arbitration
internal/agent     in container supervisor, ties capture to the gateway
internal/authz     signed sessions, guild allow list, per user rate limits
internal/discord   OAuth code exchange and identity
internal/h264      Annex B parsing, kept for the alternate encoder path

web/src/app        the client: transport, player, input, main
deploy/            container images, compose stack, seccomp, firewall rules
```

## Development

```sh
go vet ./...                  # static checks
go test -race ./...           # Go unit and websocket integration tests
cd web && npm ci              # install client dependencies
cd web && npm run typecheck   # tsc, strict
cd web && npm test            # client tests, uses node --test
cd web && npm run build       # bundle the client
```

All of that runs in CI on every push and pull request, along with a build of both
images.

`internal/gateway` holds integration tests over real websockets covering late
joiners, input routing, malformed message rejection and unauthorized access.
They are the fastest way to check that a transport change did not break the
contract.

The capture source is switchable, so the pipeline runs on a laptop with no X
server:

```sh
go run ./cmd/capturedump -source test -seconds 8
```

### A note on dependencies

`go-gst` is pinned to an untagged main snapshot. The tagged releases ship
generated bindings that expose neither `Buffer.PTS()` nor `Buffer.Map()`, so they
cannot carry timestamps out of the pipeline. All use of the binding is confined
to `internal/capture`, so an upstream API break touches one file.

## Status

Working and in use: video, audio, A/V sync, input, navigation, Discord OAuth with
a guild allow list, ad blocking, and touch on mobile. Both sockets reconnect with
backoff, the client rebuilds its decoder if it dies mid stream, and an expired
session is renewed rather than retried forever.

Not done yet:

- Multi room. Every Discord activity instance currently maps onto one long lived
  room container. Real per instance rooms need a supervisor that creates and
  destroys containers through a docker socket proxy.
- Gateway self healing. The gateway can already see that a room has no agent and
  stale chunks. It should act on that rather than serving a frozen picture.
- Frozen room detection. If Chromium stops painting while the encoder keeps
  running, every health signal still reads green. Catching that needs a check at
  the capture source, not at the gateway.
- Local input echo. Every click round trips to the server before anything moves.

## License

[AGPL-3.0](LICENSE).
