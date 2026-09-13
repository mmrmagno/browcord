export class Renewal {
  private run: () => Promise<boolean>;
  private cooldownMs: number;
  private now: () => number;
  private inflight: Promise<boolean> | null = null;
  private last = Number.NEGATIVE_INFINITY;

  constructor(run: () => Promise<boolean>, cooldownMs: number, now: () => number = () => Date.now()) {
    this.run = run;
    this.cooldownMs = cooldownMs;
    this.now = now;
  }

  request(): Promise<boolean> {
    if (this.inflight) return this.inflight;

    const at = this.now();
    if (at - this.last < this.cooldownMs) return Promise.resolve(false);
    this.last = at;

    this.inflight = this.run()
      .catch(() => false)
      .finally(() => {
        this.inflight = null;
      });

    return this.inflight;
  }
}
