export const HEADER_SIZE = 13;

const CTL_RETRY_BASE_MS = 500;
const CTL_RETRY_MAX_MS = 10000;
const STALE_AFTER_FAILURES = 3;

export const enum ChunkType {
  VideoKey = 1,
  VideoDelta = 2,
  Audio = 3,
  VideoConfig = 4,
  AudioConfig = 5,
}

export interface Chunk {
  type: ChunkType;
  pts: number;
  payload: Uint8Array;
}

export function unmarshal(buffer: ArrayBuffer): Chunk {
  if (buffer.byteLength < HEADER_SIZE) throw new Error("frame shorter than header");
  const view = new DataView(buffer);
  const length = view.getUint32(9);
  if (buffer.byteLength - HEADER_SIZE !== length) throw new Error("declared length mismatch");
  return {
    type: view.getUint8(0) as ChunkType,
    pts: Number(view.getBigUint64(1)),
    payload: new Uint8Array(buffer, HEADER_SIZE),
  };
}

export function codecFromParameterSets(payload: Uint8Array): string | null {
  for (let i = 0; i + 7 < payload.length; i++) {
    if (payload[i] === 0 && payload[i + 1] === 0 && payload[i + 2] === 0 && payload[i + 3] === 1) {
      if ((payload[i + 4] & 0x1f) === 7) {
        const hex = (n: number) => n.toString(16).padStart(2, "0");
        return `avc1.${hex(payload[i + 5])}${hex(payload[i + 6])}${hex(payload[i + 7])}`;
      }
    }
  }
  return null;
}

export interface Identity {
  token: string;
  userId: string;
  name: string;
  instanceId: string;
}

const params = new URLSearchParams(location.search);
export const inDiscord = params.has("frame_id");
export const base = inDiscord ? "/.proxy" : "";

interface ActivitySdk {
  commands: { setActivity: (args: { activity: unknown }) => Promise<unknown> };
}

let activitySdk: ActivitySdk | null = null;

export function activityClient(): ActivitySdk | null {
  return activitySdk;
}

type DiscordSdk = InstanceType<(typeof import("@discord/embedded-app-sdk"))["DiscordSDK"]>;

let sdkInstance: DiscordSdk | null = null;

async function discordSdk(clientId: string): Promise<DiscordSdk> {
  if (sdkInstance) return sdkInstance;

  const { DiscordSDK } = await import("@discord/embedded-app-sdk");
  const sdk = new DiscordSDK(clientId);
  await sdk.ready();
  sdkInstance = sdk;

  return sdk;
}

export function wsURL(path: string, query: Record<string, string>): string {
  const scheme = location.protocol === "https:" ? "wss:" : "ws:";
  const search = new URLSearchParams(query).toString();
  return `${scheme}//${location.host}${base}${path}?${search}`;
}

export async function clientID(): Promise<string> {
  const res = await fetch(`${base}/api/config`);
  if (!res.ok) throw new Error(`config: ${res.status} ${await res.text()}`);

  const body = (await res.json()) as { clientId?: string };
  if (!body.clientId) throw new Error("the gateway reported no Discord client id");

  return body.clientId;
}

export async function authenticate(clientId: string): Promise<Identity> {
  let code = "";
  let instanceId = params.get("instance_id") ?? "dev-room";
  let guildId = params.get("guild_id") ?? "";

  if (inDiscord) {
    const sdk = await discordSdk(clientId);

    try {
      const granted = await sdk.commands.authorize({
        client_id: clientId,
        response_type: "code",
        state: "",
        prompt: "none",
        scope: ["identify", "rpc.activities.write"],
      });
      code = granted.code;
      activitySdk = sdk as unknown as ActivitySdk;
    } catch {
      const granted = await sdk.commands.authorize({
        client_id: clientId,
        response_type: "code",
        state: "",
        prompt: "none",
        scope: ["identify"],
      });
      code = granted.code;
    }

    instanceId = sdk.instanceId;
    guildId = sdk.guildId ?? "";
  }

  const res = await fetch(`${base}/api/token`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ code, instanceId, guildId }),
  });

  if (!res.ok) {
    throw new Error(`authentication failed: ${res.status} ${await res.text()}`);
  }

  const body = (await res.json()) as { token: string; userId: string; name: string; roomId?: string };
  return { ...body, instanceId: body.roomId ?? instanceId };
}

export async function reportClient(
  identity: Identity,
  event: string,
  detail: string,
  codec = "",
  decoded = 0,
): Promise<void> {
  try {
    await fetch(`${base}/api/clientlog`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        token: identity.token,
        event,
        detail: detail.slice(0, 1500),
        codec,
        decoded,
        userAgent: navigator.userAgent.slice(0, 200),
      }),
    });
  } catch {
    // diagnostics must never break playback
  }
}

export class MediaSocket {
  private identity: Identity;
  private onChunk: (c: Chunk) => void;
  private onState: (online: boolean) => void;

  private ws: WebSocket | null = null;
  private retry = 0;
  private timer: ReturnType<typeof setTimeout> | null = null;
  private closed = false;
  private onStale: () => void;

  constructor(
    identity: Identity,
    onChunk: (c: Chunk) => void,
    onState: (online: boolean) => void = () => {},
    onStale: () => void = () => {},
  ) {
    this.identity = identity;
    this.onChunk = onChunk;
    this.onState = onState;
    this.onStale = onStale;
    this.connect();
  }

  private connect(): void {
    if (this.closed) return;

    const ws = new WebSocket(
      wsURL("/ws/media", { room: this.identity.instanceId, token: this.identity.token }),
    );
    ws.binaryType = "arraybuffer";
    this.ws = ws;

    ws.onopen = () => {
      this.retry = 0;
      this.onState(true);
    };

    ws.onmessage = (ev) => {
      try {
        this.onChunk(unmarshal(ev.data as ArrayBuffer));
      } catch {
        // a malformed frame is dropped; the next keyframe resynchronises playback
      }
    };

    ws.onclose = () => {
      if (this.ws === ws) this.ws = null;
      this.onState(false);
      this.scheduleReconnect();
    };

    ws.onerror = () => ws.close();
  }

  private scheduleReconnect(): void {
    if (this.closed || this.timer !== null) return;

    const wait = Math.min(CTL_RETRY_MAX_MS, CTL_RETRY_BASE_MS * 2 ** this.retry);
    this.retry = Math.min(this.retry + 1, 6);
    if (this.retry >= STALE_AFTER_FAILURES) this.onStale();

    this.timer = setTimeout(() => {
      this.timer = null;
      this.connect();
    }, wait);
  }

  close(): void {
    this.closed = true;
    if (this.timer !== null) clearTimeout(this.timer);
    this.timer = null;
    this.ws?.close();
  }
}

export class ControlSocket {
  private identity: Identity;
  private onMessage: (msg: Record<string, unknown>) => void;
  private onState: (online: boolean) => void;

  private ws: WebSocket | null = null;
  private queue: string[] = [];
  private retry = 0;
  private timer: ReturnType<typeof setTimeout> | null = null;
  private closed = false;
  private onStale: () => void;

  constructor(
    identity: Identity,
    onMessage: (msg: Record<string, unknown>) => void,
    onState: (online: boolean) => void = () => {},
    onStale: () => void = () => {},
  ) {
    this.identity = identity;
    this.onMessage = onMessage;
    this.onState = onState;
    this.onStale = onStale;
    this.connect();
  }

  private connect(): void {
    if (this.closed) return;

    const ws = new WebSocket(
      wsURL("/ws/ctl", { room: this.identity.instanceId, token: this.identity.token }),
    );
    this.ws = ws;

    ws.onopen = () => {
      this.retry = 0;
      for (const msg of this.queue) ws.send(msg);
      this.queue = [];
      this.onState(true);
    };

    ws.onmessage = (ev) => {
      try {
        this.onMessage(JSON.parse(ev.data as string));
      } catch {
        // ignore anything that is not a control message
      }
    };

    ws.onclose = () => {
      if (this.ws === ws) this.ws = null;
      this.onState(false);
      this.scheduleReconnect();
    };

    ws.onerror = () => ws.close();
  }

  private scheduleReconnect(): void {
    if (this.closed || this.timer !== null) return;

    const wait = Math.min(CTL_RETRY_MAX_MS, CTL_RETRY_BASE_MS * 2 ** this.retry);
    this.retry = Math.min(this.retry + 1, 6);
    if (this.retry >= STALE_AFTER_FAILURES) this.onStale();

    this.timer = setTimeout(() => {
      this.timer = null;
      this.connect();
    }, wait);
  }

  get online(): boolean {
    return this.ws !== null && this.ws.readyState === WebSocket.OPEN;
  }

  send(msg: Record<string, unknown>): void {
    const encoded = JSON.stringify(msg);
    if (this.ws !== null && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(encoded);
    } else if (this.queue.length < 32) {
      this.queue.push(encoded);
    }
  }

  close(): void {
    this.closed = true;
    if (this.timer !== null) clearTimeout(this.timer);
    this.timer = null;
    this.ws?.close();
  }
}
