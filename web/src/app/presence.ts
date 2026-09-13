export const LARGE_IMAGE = "browcord-cover";
export const SMALL_IMAGE = "browcord-cursor";
export const LARGE_TEXT = "browcord: one browser, everyone clicks";
export const PARTY_MAX = 16;

const MIN_INTERVAL_MS = 5000;

export interface Activity {
  type: number;
  details: string;
  state: string;
  timestamps: { start: number };
  assets: {
    large_image: string;
    large_text: string;
    small_image: string;
    small_text: string;
  };
  party: { id: string; size: [number, number] };
  instance: boolean;
}

export function siteOf(url: string): string {
  try {
    const host = new URL(url).hostname;
    return host.startsWith("www.") ? host.slice(4) : host;
  } catch {
    return "";
  }
}

export function describeActivity(
  site: string,
  viewers: number,
  roomId: string,
  startedAt: number,
): Activity {
  const others = Math.max(0, viewers - 1);

  let state: string;
  if (others === 0) {
    state = "Browsing solo";
  } else if (others === 1) {
    state = "Sharing control with 1 other";
  } else {
    state = `Sharing control with ${others} others`;
  }

  return {
    type: 0,
    details: site ? `Browsing ${site}` : "Opening a room",
    state,
    timestamps: { start: startedAt },
    assets: {
      large_image: LARGE_IMAGE,
      large_text: LARGE_TEXT,
      small_image: SMALL_IMAGE,
      small_text: others > 0 ? "Shared control" : "Solo",
    },
    party: { id: roomId, size: [Math.max(1, viewers), PARTY_MAX] },
    instance: true,
  };
}

function detail(err: unknown): string {
  if (err instanceof Error) return `${err.name}: ${err.message}`;
  if (typeof err === "string") return err;
  if (err && typeof err === "object") {
    const o = err as Record<string, unknown>;
    const parts = ["code", "message", "error", "name"]
      .map((k) => (o[k] === undefined ? "" : `${k}=${String(o[k])}`))
      .filter(Boolean);
    if (parts.length) return parts.join(" ");
    try {
      return JSON.stringify(err).slice(0, 300);
    } catch {
      return "unserialisable error object";
    }
  }
  return String(err);
}

export type SendActivity = (activity: Activity) => Promise<unknown>;

export class PresenceReporter {
  private send: SendActivity;
  private roomId: string;
  private onError: (message: string) => void;
  private startedAt: number;

  private site = "";
  private viewers = 1;
  private lastSent = "";
  private lastSentAt = 0;
  private timer: ReturnType<typeof setTimeout> | null = null;
  private stopped = false;

  updates = 0;

  constructor(
    send: SendActivity,
    roomId: string,
    onError: (message: string) => void = () => {},
    startedAt: number = Date.now(),
  ) {
    this.send = send;
    this.roomId = roomId;
    this.onError = onError;
    this.startedAt = startedAt;
  }

  setSite(url: string): void {
    const site = siteOf(url);
    if (site === this.site) return;
    this.site = site;
    this.schedule();
  }

  setViewers(count: number): void {
    if (count === this.viewers) return;
    this.viewers = count;
    this.schedule();
  }

  stop(): void {
    this.stopped = true;
    if (this.timer !== null) clearTimeout(this.timer);
    this.timer = null;
  }

  private schedule(): void {
    if (this.stopped || this.timer !== null) return;

    const since = Date.now() - this.lastSentAt;
    const wait = this.lastSentAt === 0 ? 0 : Math.max(0, MIN_INTERVAL_MS - since);

    this.timer = setTimeout(() => {
      this.timer = null;
      void this.flush();
    }, wait);
  }

  private async flush(): Promise<void> {
    if (this.stopped) return;

    const activity = describeActivity(this.site, this.viewers, this.roomId, this.startedAt);
    const encoded = JSON.stringify(activity);
    if (encoded === this.lastSent) return;

    this.lastSent = encoded;
    this.lastSentAt = Date.now();

    try {
      await this.send(activity);
      this.updates++;
    } catch (err) {
      this.stopped = true;
      this.onError(`setActivity: ${detail(err)}`);
    }
  }
}
