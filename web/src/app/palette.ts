export const PALETTE: readonly string[] = [
  "#ff4d4d",
  "#ff9f1a",
  "#ffd93d",
  "#4ade80",
  "#38bdf8",
  "#a78bfa",
  "#f472b6",
  "#ffffff",
];

export function colorAt(index: number): string {
  if (!Number.isInteger(index) || index < 0 || index >= PALETTE.length) return PALETTE[0];
  return PALETTE[index];
}
