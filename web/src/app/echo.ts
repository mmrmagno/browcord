export const RELEASE_MS = 250;
export const LONG_MS = 450;
export const PRESS_MAX_MS = 5000;

export type EchoKind = "down" | "up" | "tap" | "long";

export interface EchoEvent {
  kind: EchoKind;
  x: number;
  y: number;
}

export interface Area {
  left: number;
  top: number;
  width: number;
  height: number;
}

export type RingState = "pressed" | "released" | "long";

export interface Ring {
  id: number;
  left: number;
  top: number;
  state: RingState;
  until: number;
}

export function placeRing(x: number, y: number, box: Area): { left: number; top: number } {
  const cx = Math.max(0, Math.min(1, x));
  const cy = Math.max(0, Math.min(1, y));
  return { left: box.left + cx * box.width, top: box.top + cy * box.height };
}

export class EchoTracker {
  private rings: Ring[] = [];
  private nextId = 1;
  private pressed: Ring | null = null;

  apply(event: EchoEvent, box: Area, now: number): Ring[] {
    const changed: Ring[] = [];

    if (event.kind === "down") {
      if (this.pressed) changed.push(this.release(this.pressed, now));
      const ring = this.add(event, box, "pressed", now + PRESS_MAX_MS);
      this.pressed = ring;
      changed.push(ring);
      return changed;
    }

    if (event.kind === "up") {
      if (this.pressed) {
        changed.push(this.release(this.pressed, now));
        return changed;
      }
      changed.push(this.add(event, box, "released", now + RELEASE_MS));
      return changed;
    }

    if (event.kind === "tap") {
      changed.push(this.add(event, box, "released", now + RELEASE_MS));
      return changed;
    }

    changed.push(this.add(event, box, "long", now + LONG_MS));
    return changed;
  }

  expire(now: number): Ring[] {
    const gone = this.rings.filter((r) => r.until <= now);
    if (gone.length === 0) return gone;
    this.rings = this.rings.filter((r) => r.until > now);
    if (this.pressed && this.pressed.until <= now) this.pressed = null;
    return gone;
  }

  nextExpiry(): number | null {
    if (this.rings.length === 0) return null;
    return Math.min(...this.rings.map((r) => r.until));
  }

  all(): Ring[] {
    return this.rings.slice();
  }

  private add(event: EchoEvent, box: Area, state: RingState, until: number): Ring {
    const { left, top } = placeRing(event.x, event.y, box);
    const ring: Ring = { id: this.nextId++, left, top, state, until };
    this.rings.push(ring);
    return ring;
  }

  private release(ring: Ring, now: number): Ring {
    ring.state = "released";
    ring.until = now + RELEASE_MS;
    if (this.pressed === ring) this.pressed = null;
    return ring;
  }
}
