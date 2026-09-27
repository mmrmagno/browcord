import { test } from "node:test";
import assert from "node:assert/strict";
import { PtsWatch } from "./discontinuity.ts";

test("steady and slightly reordered pts is not a discontinuity", () => {
  const w = new PtsWatch();
  for (const pts of [0, 33_000, 20_000, 66_000, 100_000, 90_000]) {
    assert.equal(w.observe(pts), false);
  }
});

test("a pipeline restart that returns pts to zero is detected once", () => {
  const w = new PtsWatch();
  assert.equal(w.observe(0), false);
  assert.equal(w.observe(600_000_000), false);
  assert.equal(w.observe(0), true);
  assert.equal(w.observe(33_000), false);
});

test("a forward gap from a reconnect is not a discontinuity", () => {
  const w = new PtsWatch();
  w.observe(10_000_000);
  assert.equal(w.observe(40_000_000), false);
});

test("reset forgets the previous origin", () => {
  const w = new PtsWatch();
  w.observe(600_000_000);
  w.reset();
  assert.equal(w.observe(0), false);
});
