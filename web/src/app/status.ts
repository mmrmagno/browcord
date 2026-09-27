export type RoomState = "live" | "offline" | "recovering" | "full" | "ending";

export interface StatusNote {
  message: string;
  tone: "warn" | "live";
  holdMs: number;
  resync: boolean;
}

export function statusNote(prev: RoomState, next: RoomState): StatusNote | null {
  if (prev === next) return null;

  if (next === "offline") {
    return { message: "Room browser is offline, waiting for it", tone: "warn", holdMs: 0, resync: false };
  }
  if (next === "full") {
    return { message: "Every room is in use, this one cannot restart yet", tone: "warn", holdMs: 0, resync: false };
  }
  if (next === "ending") {
    return { message: "This room reached its time limit, starting a fresh one", tone: "warn", holdMs: 0, resync: false };
  }
  if (next === "recovering") {
    return { message: "Room froze, restarting the browser", tone: "warn", holdMs: 0, resync: false };
  }
  return { message: "Room is back", tone: "live", holdMs: 2500, resync: true };
}

export function parseRoomState(value: unknown): RoomState | null {
  return value === "live" || value === "offline" || value === "recovering" || value === "full" || value === "ending"
    ? value
    : null;
}
