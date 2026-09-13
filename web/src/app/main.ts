import { RevealGate } from "./gate";
import { InputBridge } from "./input";
import { Activity, PresenceReporter } from "./presence";
import { Renewal } from "./renewal";
import { contentBox } from "./viewport";
import { decodeSupported, Player } from "./player";
import {
  activityClient,
  authenticate,
  clientID,
  ControlSocket,
  Identity,
  inDiscord,
  MediaSocket,
  reportClient,
} from "./transport";

interface Cursor {
  userId: string;
  name: string;
  x: number;
  y: number;
}

interface Participant {
  userId: string;
  name: string;
}

const el = {
  surface: document.getElementById("surface") as HTMLDivElement,
  canvas: document.getElementById("screen") as HTMLCanvasElement,
  cursors: document.getElementById("cursors") as HTMLDivElement,
  url: document.getElementById("url") as HTMLInputElement,
  back: document.getElementById("back") as HTMLButtonElement,
  forward: document.getElementById("forward") as HTMLButtonElement,
  reload: document.getElementById("reload") as HTMLButtonElement,
  zoom: document.getElementById("zoom") as HTMLButtonElement,
  note: document.getElementById("note") as HTMLDivElement,
  presence: document.getElementById("presence") as HTMLDivElement,
  progress: document.getElementById("progress") as HTMLDivElement,
  typing: document.getElementById("typing") as HTMLDivElement,
  overlay: document.getElementById("overlay") as HTMLDivElement,
  overlayText: document.getElementById("overlay-text") as HTMLDivElement,
  join: document.getElementById("join") as HTMLButtonElement,
  keyboard: document.getElementById("keyboard") as HTMLInputElement,
};

function colorFor(userId: string): string {
  let hash = 0;
  for (let i = 0; i < userId.length; i++) hash = (hash * 31 + userId.charCodeAt(i)) | 0;
  return `hsl(${Math.abs(hash) % 360} 85% 64%)`;
}

function describe(err: unknown): string {
  if (err instanceof Error) return err.message;
  if (typeof err === "string") return err;
  if (err && typeof err === "object") {
    const o = err as Record<string, unknown>;
    const parts = ["message", "error", "code", "description"]
      .map((k) => (o[k] === undefined ? "" : String(o[k])))
      .filter(Boolean);
    if (parts.length) return parts.join(" ");
    try {
      return JSON.stringify(err);
    } catch {
      return String(err);
    }
  }
  return String(err);
}

let noteTimer = 0;

function note(message: string, tone: "bad" | "warn" | "live", holdMs = 6000): void {
  el.note.textContent = message;
  el.note.className = tone;
  el.note.hidden = false;

  clearTimeout(noteTimer);
  if (holdMs > 0) {
    noteTimer = window.setTimeout(() => {
      el.note.hidden = true;
    }, holdMs);
  }
}

let progressTimer = 0;

function setLoading(active: boolean): void {
  clearTimeout(progressTimer);

  if (active) {
    el.progress.classList.add("active");
    el.progress.style.width = "12%";
    requestAnimationFrame(() => {
      el.progress.style.width = "72%";
    });
    return;
  }

  el.progress.style.width = "100%";
  progressTimer = window.setTimeout(() => {
    el.progress.classList.remove("active");
    el.progress.style.width = "0";
  }, 240);
}

function showOverlay(message: string, showJoin: boolean): void {
  el.overlayText.textContent = message;
  el.join.hidden = !showJoin;
  el.overlay.hidden = false;
}

function renderPresence(people: Participant[], selfId: string): void {
  el.presence.replaceChildren(
    ...people
      .filter((p) => p.userId !== selfId)
      .map((p) => {
      const pip = document.createElement("div");
      pip.className = "pip";
      pip.style.setProperty("--pip", colorFor(p.userId));
      pip.textContent = (p.name || "?").slice(0, 1);
      pip.title = p.name;
      return pip;
    }),
  );
}

function renderCursors(cursors: Cursor[], selfId: string): void {
  const box = contentBox(
    el.surface.clientWidth,
    el.surface.clientHeight,
    el.canvas.width,
    el.canvas.height,
  );

  el.cursors.replaceChildren(
    ...cursors
      .filter((c) => c.userId !== selfId)
      .map((c) => {
        const node = document.createElement("div");
        node.className = "cursor";
        node.style.left = `${box.left + c.x * box.width}px`;
        node.style.top = `${box.top + c.y * box.height}px`;
        node.style.setProperty("--cursor", colorFor(c.userId));
        node.textContent = c.name;
        return node;
      }),
  );
}

const SESSION_RENEW_COOLDOWN_MS = 30000;

let typingTimer = 0;

async function main(): Promise<void> {
  showOverlay("Connecting to the room", false);

  if (!inDiscord) {
    showOverlay(
      "Open browcord from the activity picker in a Discord voice channel.\nIt cannot sign you in from a normal browser tab.",
      false,
    );
    return;
  }

  if (!(await decodeSupported())) {
    showOverlay("This client cannot decode the video stream.\nTry the Discord desktop app.", false);
    return;
  }

  let identity: Identity;
  let clientId: string;
  try {
    clientId = await clientID();
    identity = await authenticate(clientId);
  } catch (err) {
    console.error("browcord: join failed", err);
    showOverlay(`Could not join the room.\n${describe(err)}`, false);
    return;
  }

  const renewal = new Renewal(async () => {
    const fresh = await authenticate(clientId);
    identity.token = fresh.token;
    void reportClient(identity, "session-renewed", "reconnects kept failing, minted a new session");
    return true;
  }, SESSION_RENEW_COOLDOWN_MS);

  const renew = () => void renewal.request();

  const player = new Player(
    el.canvas,
    (message) => {
      console.error("browcord:", message);
      note("Video stopped", "bad", 0);
      showOverlay(`Video problem.\n${message}`, false);
      void reportClient(identity, "video-error", message, player.codec, player.decoded);
    },
    (message) => {
      console.error("browcord audio:", message);
      note("Audio stopped", "warn");
      void reportClient(identity, "audio-error", message, player.codec, player.audioPlayed);
    },
    (message) => {
      note("Video hiccup, resyncing", "warn", 2500);
      void reportClient(identity, "video-restart", message, player.codec, player.decoded);
    },
  );

  const sdk = activityClient();
  const presence = sdk
    ? new PresenceReporter(
        (activity: Activity) => sdk.commands.setActivity({ activity }),
        identity.instanceId,
        (message) => void reportClient(identity, "presence-error", message),
      )
    : null;

  let ctlWasOnline = true;

  const ctl = new ControlSocket(
    identity,
    (msg) => {
      switch (msg.type) {
        case "cursors":
          renderCursors((msg.cursors as Cursor[]) ?? [], identity.userId);
          break;

        case "presence": {
          const people = (msg.presence as Participant[]) ?? [];
          renderPresence(people, identity.userId);
          presence?.setViewers(people.length);
          break;
        }

        case "nav": {
          const nav = msg.nav as { url?: string; loading?: boolean } | undefined;
          if (nav?.url && document.activeElement !== el.url) el.url.value = nav.url;
          if (nav?.url) presence?.setSite(nav.url);
          if (nav?.loading !== undefined) setLoading(nav.loading);
          break;
        }

        case "typing": {
          if (msg.userId === identity.userId) break;
          el.typing.textContent = `${msg.name} is typing`;
          el.typing.hidden = false;
          clearTimeout(typingTimer);
          typingTimer = window.setTimeout(() => {
            el.typing.hidden = true;
          }, 1600);
          break;
        }

        case "error":
          note(String(msg.message ?? "Something went wrong"), "warn");
          setLoading(false);
          break;
      }
    },
    (online) => {
      if (online && !ctlWasOnline) {
        note("Input reconnected", "live", 2500);
      } else if (!online && ctlWasOnline) {
        note("Input disconnected, reconnecting", "warn", 0);
        void reportClient(identity, "ctl-offline", "control socket closed");
      }
      ctlWasOnline = online;
    },
    renew,
  );

  const input = new InputBridge(el.surface, el.canvas, el.keyboard, ctl, () => {
    const { scale, offsetX, offsetY } = input.view;
    el.surface.style.transform = `translate(${offsetX}px, ${offsetY}px) scale(${scale})`;
    el.zoom.hidden = scale === 1;
  });

  el.zoom.addEventListener("click", () => input.resetZoom());
  el.back.addEventListener("click", () => ctl.send({ type: "back" }));
  el.forward.addEventListener("click", () => ctl.send({ type: "forward" }));
  el.reload.addEventListener("click", () => {
    setLoading(true);
    ctl.send({ type: "reload" });
  });

  el.url.addEventListener("focus", () => el.url.select());
  el.url.addEventListener("keydown", (e) => {
    if (e.key !== "Enter") return;
    e.preventDefault();

    const url = el.url.value.trim();
    if (!url) return;

    setLoading(true);
    ctl.send({ type: "navigate", url });
    el.url.blur();
    el.keyboard.focus({ preventScroll: true });
  });

  let announced = false;
  let mediaWasOnline = true;
  let chunks = 0;

  const gate = new RevealGate(() => {
    el.overlay.hidden = true;
    el.note.hidden = true;
    el.keyboard.focus({ preventScroll: true });
  });

  window.setTimeout(() => {
    if (player.decoded > 0) return;
    const detail = `no frames decoded after 12s: ${chunks} chunks received, codec=${player.codec || "unconfigured"}`;
    note("No video yet", "warn", 0);
    void reportClient(identity, "no-video", detail, player.codec, player.decoded);
  }, 12000);

  new MediaSocket(
    identity,
    (chunk) => {
      player.push(chunk);
      chunks++;
      if (!announced && player.decoded > 0) {
        announced = true;
        void reportClient(identity, "first-frame", `after ${chunks} chunks`, player.codec, player.decoded);
        gate.stream();
      }
    },
    (online) => {
      if (online && !mediaWasOnline) {
        note("Stream reconnected", "live", 2500);
      } else if (!online && mediaWasOnline) {
        note("Stream dropped, reconnecting", "warn", 0);
        void reportClient(identity, "media-offline", "media socket closed", player.codec, player.decoded);
      }
      mediaWasOnline = online;
    },
    renew,
  );

  showOverlay("Tap to join.\nEveryone here shares one browser, and everyone can click.", true);

  el.join.addEventListener("click", () => {
    gate.join();
    el.join.hidden = true;
    if (player.decoded === 0) el.overlayText.textContent = "Connecting to the room";
    el.keyboard.focus({ preventScroll: true });

    void player.unlockAudio().then((state) => {
      void reportClient(identity, "audio-unlock", `state=${state}`, player.codec, player.decoded);
      window.setTimeout(() => {
        const detail =
          `state=${player.audioState} played=${player.audioPlayed} dropped=${player.audioDropped} ` +
          `skew=${Math.round(player.syncSkewMs)}ms resyncs=${player.audioResyncs} underruns=${player.audioUnderruns} ` +
          `audioOffset=${Math.round(player.audioOffsetMs)}ms videoOffset=${Math.round(player.videoOffsetMs)}ms ` +
          `netJitter=${Math.round(player.videoJitterMs)}ms srcJitter=${Math.round(player.sourceJitterMs)}ms ` +
          `audioJitter=${Math.round(player.audioJitterMs)}ms audioBuffer=${Math.round(player.audioBufferMs)}ms ` +
          `lead=${Math.round(player.videoLeadMs)}ms lateFrames=${player.videoDropped}`;
        const event = player.audioPlayed > 0 ? "audio-playing" : "audio-silent";
        void reportClient(identity, event, detail, player.codec, player.audioPlayed);
      }, 8000);
    });
  });
}

void main();
