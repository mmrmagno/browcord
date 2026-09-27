export const FLUSH_MS = 80;
export const MAX_BATCH_PAIRS = 48;
export const MIN_MOVE = 0.002;
export const INK_TTL_MS = 15000;
export const INK_FADE_MS = 3000;

export interface Stroke {
  id: number;
  userId: string;
  color: number;
  points: number[];
  age?: number;
}

function round3(v: number): number {
  return Math.round(v * 1000) / 1000;
}

export class StrokeBatcher {
  private pending: number[] = [];
  private lastX = 0;
  private lastY = 0;
  private hasLast = false;
  private dueAt = 0;
  private drawing = false;

  begin(now: number): void {
    this.pending = [];
    this.hasLast = false;
    this.dueAt = now + FLUSH_MS;
    this.drawing = true;
  }

  push(x: number, y: number, now: number): number[] | null {
    if (!this.drawing) return null;

    if (this.hasLast && Math.abs(x - this.lastX) < MIN_MOVE && Math.abs(y - this.lastY) < MIN_MOVE) {
      return null;
    }

    this.lastX = x;
    this.lastY = y;
    this.hasLast = true;
    this.pending.push(round3(x), round3(y));

    if (this.pending.length / 2 >= MAX_BATCH_PAIRS || now >= this.dueAt) return this.take(now);
    return null;
  }

  flush(now: number): number[] | null {
    if (!this.drawing || this.pending.length === 0) return null;
    return this.take(now);
  }

  end(): void {
    this.drawing = false;
    this.pending = [];
    this.hasLast = false;
  }

  private take(now: number): number[] {
    const batch = this.pending;
    this.pending = [];
    this.dueAt = now + FLUSH_MS;
    return batch;
  }
}

export class InkStore {
  private strokes: Stroke[] = [];
  private lastAt = new Map<number, number>();

  reset(list: Stroke[], now = 0): void {
    this.strokes = list.map((s) => ({ ...s, points: s.points.slice() }));
    this.lastAt = new Map(list.map((s) => [s.id, now - (s.age ?? 0)]));
  }

  apply(list: Stroke[], now = 0): void {
    for (const incoming of list) {
      const existing = this.strokes.find((s) => s.id === incoming.id);
      if (existing) {
        for (const v of incoming.points) existing.points.push(v);
      } else {
        this.strokes.push({ ...incoming, points: incoming.points.slice() });
      }
      this.lastAt.set(incoming.id, now);
    }
  }

  clearUser(userId: string): void {
    this.strokes = this.strokes.filter((s) => s.userId !== userId);
  }

  clear(): void {
    this.strokes = [];
    this.lastAt.clear();
  }

  alpha(id: number, now: number, ttl = INK_TTL_MS, fade = INK_FADE_MS): number {
    const at = this.lastAt.get(id) ?? now;
    const left = ttl - (now - at);
    if (left <= 0) return 0;
    return Math.min(1, left / fade);
  }

  expire(now: number, ttl = INK_TTL_MS): boolean {
    const before = this.strokes.length;
    this.strokes = this.strokes.filter((s) => this.alpha(s.id, now, ttl) > 0);
    for (const id of this.lastAt.keys()) {
      if (!this.strokes.some((s) => s.id === id)) this.lastAt.delete(id);
    }
    return this.strokes.length !== before;
  }

  fading(now: number, ttl = INK_TTL_MS, fade = INK_FADE_MS): boolean {
    return this.strokes.some((s) => this.alpha(s.id, now, ttl, fade) < 1);
  }

  all(): readonly Stroke[] {
    return this.strokes;
  }
}
