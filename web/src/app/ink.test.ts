import { test } from "node:test";
import assert from "node:assert/strict";
import { InkStore, MAX_BATCH_PAIRS, StrokeBatcher } from "./ink.ts";
import { PALETTE, colorAt } from "./palette.ts";

test("the palette matches the size the server validates against", () => {
  assert.equal(PALETTE.length, 8);
  for (const c of PALETTE) assert.match(c, /^#[0-9a-f]{6}$/);
});

test("an out of range colour index falls back instead of throwing", () => {
  assert.equal(colorAt(0), PALETTE[0]);
  assert.equal(colorAt(7), PALETTE[7]);
  assert.equal(colorAt(8), PALETTE[0]);
  assert.equal(colorAt(-1), PALETTE[0]);
  assert.equal(colorAt(1.5), PALETTE[0]);
});

test("a pathological burst is chunked, never dropped, and no chunk exceeds the cap", () => {
  const b = new StrokeBatcher();
  b.begin(0);

  const batches: number[][] = [];
  for (let i = 0; i < 500; i++) {
    const out = b.push(i % 2 === 0 ? 0.1 : 0.9, 0.5, 1);
    if (out) batches.push(out);
  }
  const tail = b.flush(1);
  if (tail) batches.push(tail);

  assert.ok(batches.length > 0);
  for (const batch of batches) {
    assert.ok(batch.length / 2 <= MAX_BATCH_PAIRS, `a batch carried ${batch.length / 2} pairs`);
  }

  const sent = batches.reduce((n, b) => n + b.length / 2, 0);
  assert.equal(sent, 500, "every point that moved should reach the wire");
});

test("a realistic pointer rate produces one batch per flush window", () => {
  const b = new StrokeBatcher();
  b.begin(0);

  const batches: number[][] = [];
  for (let i = 0; i <= 10; i++) {
    const out = b.push(i / 100, 0.5, i * 8);
    if (out) batches.push(out);
  }

  assert.equal(batches.length, 1, "120Hz input over one 80ms window should cost a single message");
  assert.equal(batches[0].length / 2, 11);
});

test("a full batch stays well inside the 8KB control frame cap", () => {
  const b = new StrokeBatcher();
  b.begin(0);

  let batch: number[] | null = null;
  for (let i = 0; i < 1000 && !batch; i++) batch = b.push(i / 1000, 0.123456789, 1);

  assert.ok(batch);
  const frame = JSON.stringify({ type: "stroke", seq: 1, points: batch });
  assert.ok(frame.length < 8192, `frame was ${frame.length} bytes`);
});

test("points that barely move are dropped", () => {
  const b = new StrokeBatcher();
  b.begin(0);

  assert.deepEqual(b.push(0.5, 0.5, 0), null);
  assert.equal(b.push(0.5001, 0.5001, 0), null);

  const out = b.flush(0);
  assert.equal(out?.length, 2, "only the first point should have been kept");
});

test("the time cadence releases a batch even when few points moved", () => {
  const b = new StrokeBatcher();
  b.begin(0);

  assert.equal(b.push(0.1, 0.1, 0), null);
  const out = b.push(0.9, 0.9, 80);

  assert.equal(out?.length, 4);
});

test("ending a stroke discards anything still pending", () => {
  const b = new StrokeBatcher();
  b.begin(0);
  b.push(0.1, 0.1, 0);
  b.end();

  assert.equal(b.flush(0), null);
  assert.equal(b.push(0.5, 0.5, 0), null);
});

test("a delta appends to a stroke already held and inserts an unknown one", () => {
  const s = new InkStore();
  s.reset([{ id: 1, userId: "a", color: 0, points: [0, 0] }]);

  s.apply([{ id: 1, userId: "a", color: 0, points: [0.5, 0.5] }]);
  assert.deepEqual(s.all()[0].points, [0, 0, 0.5, 0.5]);

  s.apply([{ id: 2, userId: "b", color: 3, points: [1, 1] }]);
  assert.equal(s.all().length, 2);
  assert.equal(s.all()[1].userId, "b");
});

test("reset replaces the board and does not alias the caller's arrays", () => {
  const s = new InkStore();
  const incoming = [{ id: 1, userId: "a", color: 0, points: [0, 0] }];
  s.reset(incoming);

  incoming[0].points.push(9, 9);
  assert.deepEqual(s.all()[0].points, [0, 0]);

  s.reset([]);
  assert.equal(s.all().length, 0);
});

test("clearing one author leaves everyone else's ink in paint order", () => {
  const s = new InkStore();
  s.reset([
    { id: 1, userId: "a", color: 0, points: [0, 0] },
    { id: 2, userId: "b", color: 1, points: [1, 1] },
    { id: 3, userId: "a", color: 0, points: [2, 2] },
    { id: 4, userId: "c", color: 2, points: [3, 3] },
  ]);

  s.clearUser("a");

  assert.deepEqual(
    s.all().map((x) => x.id),
    [2, 4],
  );
});
