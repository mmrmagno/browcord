import { ControlSocket } from "./transport";
import { contentBox } from "./viewport";

const POINTER_HZ = 25;

const MODIFIER_ALT = 1;
const MODIFIER_CTRL = 2;
const MODIFIER_META = 4;
const MODIFIER_SHIFT = 8;

function modifiersOf(e: KeyboardEvent | MouseEvent): number {
  return (
    (e.altKey ? MODIFIER_ALT : 0) |
    (e.ctrlKey ? MODIFIER_CTRL : 0) |
    (e.metaKey ? MODIFIER_META : 0) |
    (e.shiftKey ? MODIFIER_SHIFT : 0)
  );
}

export interface View {
  scale: number;
  offsetX: number;
  offsetY: number;
}

export class InputBridge {
  private lastPointerAt = 0;
  private longPressTimer = 0;
  private touchStart: { x: number; y: number; at: number } | null = null;
  private pinchDistance = 0;

  view: View = { scale: 1, offsetX: 0, offsetY: 0 };

  constructor(
    private surface: HTMLElement,
    private media: HTMLCanvasElement,
    private textInput: HTMLInputElement,
    private ctl: ControlSocket,
    private onViewChange: () => void,
  ) {
    this.attachPointer();
    this.attachKeyboard();
    this.attachTouch();
  }

  private normalize(clientX: number, clientY: number): { x: number; y: number } {
    const rect = this.surface.getBoundingClientRect();
    const box = contentBox(rect.width, rect.height, this.media.width, this.media.height);
    const localX = clientX - rect.left - box.left;
    const localY = clientY - rect.top - box.top;
    return {
      x: Math.max(0, Math.min(1, localX / box.width)),
      y: Math.max(0, Math.min(1, localY / box.height)),
    };
  }

  private attachPointer(): void {
    this.surface.addEventListener("pointermove", (e) => {
      if (e.pointerType === "touch") return;
      const now = performance.now();
      if (now - this.lastPointerAt < 1000 / POINTER_HZ) return;
      this.lastPointerAt = now;

      const { x, y } = this.normalize(e.clientX, e.clientY);
      this.ctl.send({ type: "pointer", x, y });
    });

    this.surface.addEventListener("pointerdown", (e) => {
      if (e.pointerType === "touch") return;
      e.preventDefault();
      this.textInput.focus({ preventScroll: true });
      const { x, y } = this.normalize(e.clientX, e.clientY);
      this.ctl.send({ type: "click", x, y, button: e.button, down: true });
    });

    this.surface.addEventListener("pointerup", (e) => {
      if (e.pointerType === "touch") return;
      const { x, y } = this.normalize(e.clientX, e.clientY);
      this.ctl.send({ type: "click", x, y, button: e.button, down: false });
    });

    this.surface.addEventListener("contextmenu", (e) => e.preventDefault());

    this.surface.addEventListener(
      "wheel",
      (e) => {
        if (e.ctrlKey) {
          e.preventDefault();
          this.zoomBy(e.deltaY < 0 ? 1.1 : 1 / 1.1, e.clientX, e.clientY);
          return;
        }
        e.preventDefault();
        const { x, y } = this.normalize(e.clientX, e.clientY);
        this.ctl.send({ type: "scroll", x, y, dx: -e.deltaX, dy: -e.deltaY });
      },
      { passive: false },
    );
  }

  private attachKeyboard(): void {
    this.textInput.addEventListener("keydown", (e) => {
      if (e.key === "Unidentified") return;
      if (e.ctrlKey && (e.key === "v" || e.key === "c")) return;

      e.preventDefault();
      this.ctl.send({
        type: "key",
        key: e.key,
        code: e.code,
        down: true,
        modifiers: modifiersOf(e),
      });
    });

    this.textInput.addEventListener("keyup", (e) => {
      if (e.key === "Unidentified") return;
      e.preventDefault();
      this.ctl.send({
        type: "key",
        key: e.key,
        code: e.code,
        down: false,
        modifiers: modifiersOf(e),
      });
    });

    this.textInput.addEventListener("input", () => {
      const text = this.textInput.value;
      if (!text) return;
      this.textInput.value = "";
      this.ctl.send({ type: "text", text });
    });
  }

  private attachTouch(): void {
    this.surface.addEventListener(
      "touchstart",
      (e) => {
        if (e.touches.length === 2) {
          this.pinchDistance = distance(e.touches[0], e.touches[1]);
          this.clearLongPress();
          return;
        }

        const t = e.touches[0];
        if (!t) return;
        e.preventDefault();

        this.touchStart = { x: t.clientX, y: t.clientY, at: performance.now() };
        const { x, y } = this.normalize(t.clientX, t.clientY);

        this.longPressTimer = window.setTimeout(() => {
          this.ctl.send({ type: "click", x, y, button: 2, down: true });
          this.ctl.send({ type: "click", x, y, button: 2, down: false });
          this.touchStart = null;
        }, 500);
      },
      { passive: false },
    );

    this.surface.addEventListener(
      "touchmove",
      (e) => {
        if (e.touches.length === 2) {
          e.preventDefault();
          const next = distance(e.touches[0], e.touches[1]);
          if (this.pinchDistance > 0) {
            const midX = (e.touches[0].clientX + e.touches[1].clientX) / 2;
            const midY = (e.touches[0].clientY + e.touches[1].clientY) / 2;
            this.zoomBy(next / this.pinchDistance, midX, midY);
          }
          this.pinchDistance = next;
          return;
        }

        const t = e.touches[0];
        if (!t || !this.touchStart) return;

        const movedX = t.clientX - this.touchStart.x;
        const movedY = t.clientY - this.touchStart.y;
        if (Math.abs(movedX) + Math.abs(movedY) > 10) this.clearLongPress();

        e.preventDefault();
        const { x, y } = this.normalize(this.touchStart.x, this.touchStart.y);
        this.ctl.send({ type: "scroll", x, y, dx: movedX, dy: movedY });
        this.touchStart = { x: t.clientX, y: t.clientY, at: this.touchStart.at };
      },
      { passive: false },
    );

    this.surface.addEventListener("touchend", (e) => {
      this.pinchDistance = 0;
      if (!this.touchStart) return;
      this.clearLongPress();

      const heldFor = performance.now() - this.touchStart.at;
      if (heldFor < 500) {
        const { x, y } = this.normalize(this.touchStart.x, this.touchStart.y);
        this.ctl.send({ type: "click", x, y, button: 0, down: true });
        this.ctl.send({ type: "click", x, y, button: 0, down: false });
        this.textInput.focus({ preventScroll: true });
      }
      this.touchStart = null;
      e.preventDefault();
    });
  }

  private clearLongPress(): void {
    if (this.longPressTimer) {
      clearTimeout(this.longPressTimer);
      this.longPressTimer = 0;
    }
  }

  private zoomBy(factor: number, originX: number, originY: number): void {
    const rect = this.surface.getBoundingClientRect();
    const next = Math.max(1, Math.min(4, this.view.scale * factor));
    if (next === this.view.scale) return;

    const px = originX - rect.left;
    const py = originY - rect.top;

    this.view.offsetX = px - ((px - this.view.offsetX) / this.view.scale) * next;
    this.view.offsetY = py - ((py - this.view.offsetY) / this.view.scale) * next;
    this.view.scale = next;

    const maxX = rect.width * (next - 1);
    const maxY = rect.height * (next - 1);
    this.view.offsetX = Math.max(-maxX, Math.min(0, this.view.offsetX));
    this.view.offsetY = Math.max(-maxY, Math.min(0, this.view.offsetY));

    this.onViewChange();
  }

  resetZoom(): void {
    this.view = { scale: 1, offsetX: 0, offsetY: 0 };
    this.onViewChange();
  }
}

function distance(a: Touch, b: Touch): number {
  return Math.hypot(a.clientX - b.clientX, a.clientY - b.clientY);
}
