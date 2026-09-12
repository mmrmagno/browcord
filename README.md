<p align="center">
  <img src="assets/icon.svg" alt="browcord" width="140"/>
</p>

<h1 align="center">browcord</h1>

<p align="center">
  A shared web browser that lives inside a Discord voice channel.
  <br/>
  One real Chromium runs on your server, everyone sees the same screen, and everyone can click.
</p>

<p align="center">
  <a href="https://github.com/mmrmagno/browcord/actions/workflows/ci.yml">
    <img src="https://github.com/mmrmagno/browcord/actions/workflows/ci.yml/badge.svg" alt="CI"/>
  </a>
  <img src="https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white" alt="Go"/>
  <img src="https://img.shields.io/badge/TypeScript-strict-3178C6?logo=typescript&logoColor=white" alt="TypeScript"/>
  <img src="https://img.shields.io/badge/Discord-Activity-5865F2?logo=discord&logoColor=white" alt="Discord Activity"/>
  <img src="https://img.shields.io/badge/Docker-compose-2496ED?logo=docker&logoColor=white" alt="Docker"/>
  <img src="https://img.shields.io/badge/video-VP8%20%2B%20Opus-6d79f7" alt="Codecs"/>
  <img src="https://img.shields.io/badge/license-AGPL--3.0-blue" alt="License"/>
</p>

---

Watch something together, read docs together, argue about a pull request together. The
browser runs on the server, so nobody uploads their screen, nobody needs a good connection
to share, and there is no "can you scroll down a bit" because everyone already has the mouse.

Any site that works in Chrome works in the room.

---

## Features

- **Real Chromium, not a proxy.** Pages render on the server exactly as Chrome renders them. No rewriting, no broken JavaScript, no blocked iframes.
- **Everyone can click.** Mouse, scroll, keyboard, back, forward and a URL bar, all shared. A typing token stops two people fighting over the same text box.
- **Ghost cursors and presence.** You can see where everyone else is pointing, and who is in the room.
- **Runs as a Discord Activity.** Launches from the voice channel. Discord OAuth, with a guild allow list so only your servers can open it.
- **Adaptive A/V sync.** Audio is scheduled against a video presentation clock, and both jitter buffers size themselves from measured network jitter.
- **Ad blocking built in.** uBlock Origin Lite is force installed by enterprise policy, and popup tabs are closed automatically.
- **Hardened by default.** Read only container, all capabilities dropped, Chromium keeps its own sandbox, and a packet filter stops the room reaching your private network.

---

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

The room captures the X display with GStreamer, encodes to VP8 and Opus, and pushes
length prefixed chunks to the gateway. The gateway fans them out to every viewer over a
websocket and replays a cached keyframe to late joiners, so a new arrival sees a picture
immediately. Input travels the other way and is applied through the Chrome DevTools
Protocol.

**VP8 is deliberate.** Discord's client cannot decode H.264 through WebCodecs:
`isConfigSupported()` returns true and `configure()` then fails for every profile. Do not
switch the encoder back to H.264 expecting hardware acceleration to help.

---

## Requirements

| Thing | Why |
|---|---|
| A Linux host with Docker and Compose | Runs the gateway and the room |
| A reverse proxy with TLS | Discord will only load an Activity over HTTPS |
| A Discord application with Activities enabled | Provides OAuth and the iframe |
| About 2 CPU cores and 3 GB RAM for the room | Chromium plus a 720p encoder |

The room image must be **Debian trixie or newer**. `go-glib` needs GLib 2.76 or later, and
bookworm ships 2.74, which fails to build the bindings.

---

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
| `ROOM_START_URL` | Page the room opens on |
| `ROOM_WIDTH`, `ROOM_HEIGHT` | Capture resolution, defaults to 1280x720 |

Generate the two secrets with something like `openssl rand -hex 32`.

Then bring it up and apply the egress rules:

```sh
docker compose up -d --build
sudo ROOM_SUBNET=10.89.0.0/24 ../deploy/netguard/apply.sh
../deploy/verify-egress.sh
```

Point your reverse proxy at the gateway, set the Activity URL mapping in the Discord
developer portal to the same host, and launch it from a voice channel.

### Checking it is alive

```sh
docker exec browcord-gateway wget -qO- http://127.0.0.1:8080/api/health
```

`agents` should be 1, `secondsSinceChunk` 0 and `hasVideo` true. Since there is no browser
console inside the Discord client, the web client reports diagnostics server side:

```sh
docker compose logs gateway | grep client
```

---

## Security

A room executes whatever web content a participant types into it. That shapes the whole
design, and two controls are not optional.

### The egress filter

`deploy/netguard/apply.sh` is the single most important control in the project. Without it,
anyone in the room can type `http://169.254.169.254/` or `http://192.168.1.1/` and turn the
room into a probe pointed at your cloud metadata endpoint and your LAN.

Filtering happens **by IP, in the `DOCKER-USER` chain, not by hostname**. A hostname deny
list is trivially defeated by a domain that resolves to a private address on its second
lookup. A packet filter is not. The check in `internal/cdp` is defence in depth and a
friendly error message, not the control.

Re-run the script after any change to the compose file or the networks, then verify from
inside a live room that the metadata endpoint, the LAN gateway and the host's own ports are
all unreachable.

### The seccomp profile

`deploy/seccomp/chromium.json` is what lets Chromium keep **its own sandbox** while the
container still runs with `cap_drop: ALL` and `no-new-privileges`.

Docker's default profile gates `clone`, `unshare`, `mount` and `umount2` behind
`CAP_SYS_ADMIN` and denies `pivot_root` outright. Drop capabilities without replacing the
profile and Chromium reports "No usable sandbox" and dies with "Zygote process exited
prematurely". At that point the tempting fix is `--no-sandbox`, which converts any renderer
bug into code execution as the container user. That is the worst available outcome for this
workload and it is never the answer here.

The profile is Docker's default plus exactly one rule allowing
`clone, unshare, mount, umount2, pivot_root, chroot`, so the browser can build its own
namespace sandbox unprivileged. `bpf`, `perf_event_open`, `setns` and the rest of the
`CAP_SYS_ADMIN` group stay denied.

This is a deliberate trade: permitting unprivileged user namespaces widens the host syscall
surface slightly, in exchange for keeping the much stronger boundary that is Chromium's own
multi process sandbox. On a host with gVisor, running rooms under `runsc` removes the need
for the trade entirely.

---

## Layout

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

---

## Development

```sh
go vet ./...                  # static checks
go test -race ./...           # Go unit and websocket integration tests
cd web && npm ci              # install client dependencies
cd web && npm run typecheck   # tsc, strict
cd web && npm test            # client tests, uses node --test
cd web && npm run build       # bundle the client
```

All of the above runs in CI on every push and pull request, along with a build of the
gateway image.

`internal/gateway` holds integration tests over real websockets covering late joiners,
input routing, malformed message rejection and unauthorized access. They are the fastest
way to check that a transport change did not break the contract.

The capture source is switchable, so the pipeline runs on a laptop with no X server:

```sh
go run ./cmd/capturedump -source test -seconds 8
```

### A note on dependencies

`go-gst` is pinned to an untagged main snapshot. The tagged releases ship generated bindings
that expose neither `Buffer.PTS()` nor `Buffer.Map()`, so they cannot carry timestamps out
of the pipeline. All use of the binding is confined to `internal/capture`, so an upstream
API break touches one file.

---

## Status

Working and in use: video, audio, input, navigation, Discord OAuth with a guild allow list,
ad blocking, reconnect after a gateway restart, and A/V sync.

Not done yet:

- **Multi room.** Every Discord activity instance currently maps onto one long lived room container. Real per instance rooms need a supervisor that creates and destroys containers through a docker socket proxy.
- **Mobile.** Touch, pinch zoom and the on screen keyboard are written but barely tested on a real device.
- **Gateway self healing.** The gateway can already see that a room has no agent and stale chunks. It should act on that rather than serving a frozen picture.

---

## License

[AGPL-3.0](LICENSE).
