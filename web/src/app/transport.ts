export const HEADER_SIZE = 13;

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
    const { DiscordSDK } = await import("@discord/embedded-app-sdk");
    const sdk = new DiscordSDK(clientId);
    await sdk.ready();

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
  private ws: WebSocket;

  constructor(identity: Identity, onChunk: (c: Chunk) => void, onClose: () => void) {
    this.ws = new WebSocket(wsURL("/ws/media", { room: identity.instanceId, token: identity.token }));
    this.ws.binaryType = "arraybuffer";
    this.ws.onmessage = (ev) => {
      try {
        onChunk(unmarshal(ev.data as ArrayBuffer));
      } catch {
        // a malformed frame is dropped; the next keyframe resynchronises playback
      }
    };
    this.ws.onclose = onClose;
  }

  close(): void {
    this.ws.close();
  }
}

export class ControlSocket {
  private ws: WebSocket;
  private queue: string[] = [];

  constructor(identity: Identity, private onMessage: (msg: Record<string, unknown>) => void) {
    this.ws = new WebSocket(wsURL("/ws/ctl", { room: identity.instanceId, token: identity.token }));
    this.ws.onopen = () => {
      for (const msg of this.queue) this.ws.send(msg);
      this.queue = [];
    };
    this.ws.onmessage = (ev) => {
      try {
        this.onMessage(JSON.parse(ev.data as string));
      } catch {
        // ignore anything that is not a control message
      }
    };
  }

  send(msg: Record<string, unknown>): void {
    const encoded = JSON.stringify(msg);
    if (this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(encoded);
    } else if (this.queue.length < 32) {
      this.queue.push(encoded);
    }
  }

  close(): void {
    this.ws.close();
  }
}
