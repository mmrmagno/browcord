import { test } from "node:test";
import assert from "node:assert/strict";
import { contentBox } from "./viewport.ts";

test("a box at the media ratio has no letterboxing", () => {
  const b = contentBox(1600, 900, 1280, 720);

  assert.deepEqual(b, { left: 0, top: 0, width: 1600, height: 900 });
});

test("a box wider than the media gets pillarboxed, not stretched", () => {
  const b = contentBox(1600, 854, 1280, 720);

  assert.equal(Math.round(b.width), 1518);
  assert.equal(b.height, 854);
  assert.equal(Math.round(b.left), 41);
  assert.equal(b.top, 0);
});

test("a box taller than the media gets letterboxed", () => {
  const b = contentBox(1200, 900, 1280, 720);

  assert.equal(b.width, 1200);
  assert.equal(Math.round(b.height), 675);
  assert.equal(b.left, 0);
  assert.equal(Math.round(b.top), 113);
});

test("the content box preserves the media aspect ratio in every case", () => {
  for (const [w, h] of [[1600, 900], [1600, 1200], [1200, 900], [900, 1400], [640, 360]]) {
    const b = contentBox(w, h, 1280, 720);
    assert.ok(
      Math.abs(b.width / b.height - 1280 / 720) < 0.001,
      `ratio wrong for ${w}x${h}: ${b.width / b.height}`,
    );
    assert.ok(b.width <= w + 0.001 && b.height <= h + 0.001, `overflows ${w}x${h}`);
  }
});

test("a click at the centre of a pillarboxed box maps to the centre of the media", () => {
  const b = contentBox(1600, 854, 1280, 720);
  const x = (800 - b.left) / b.width;

  assert.ok(Math.abs(x - 0.5) < 0.001, `expected 0.5, got ${x}`);
});

test("degenerate sizes do not produce NaN or divide by zero", () => {
  const b = contentBox(0, 0, 1280, 720);

  assert.ok(Number.isFinite(b.width) && b.width > 0);
  assert.ok(Number.isFinite(b.height) && b.height > 0);
});
