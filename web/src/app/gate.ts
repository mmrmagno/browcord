export class RevealGate {
  private tapped = false;
  private streaming = false;
  private fired = false;
  private onReveal: () => void;

  constructor(onReveal: () => void) {
    this.onReveal = onReveal;
  }

  get joined(): boolean {
    return this.tapped;
  }

  join(): void {
    this.tapped = true;
    this.settle();
  }

  stream(): void {
    this.streaming = true;
    this.settle();
  }

  private settle(): void {
    if (this.fired || !this.tapped || !this.streaming) return;
    this.fired = true;
    this.onReveal();
  }
}
