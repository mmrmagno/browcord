export const PTS_BACKSTEP_US = 1_000_000;

export class PtsWatch {
  private last = -1;

  observe(pts: number): boolean {
    const jumped = this.last >= 0 && pts < this.last - PTS_BACKSTEP_US;
    this.last = pts;
    return jumped;
  }

  reset(): void {
    this.last = -1;
  }
}
