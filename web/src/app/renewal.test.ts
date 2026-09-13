import { test } from "node:test";
import assert from "node:assert/strict";
import { Renewal } from "./renewal.ts";

function clock(start = 0) {
  const state = { now: start };
  return { state, read: () => state.now };
}

test("the first request runs the renewal", async () => {
  const c = clock();
  let calls = 0;
  const r = new Renewal(async () => { calls++; return true; }, 30000, c.read);

  assert.equal(await r.request(), true);
  assert.equal(calls, 1);
});

test("concurrent requests share one renewal", async () => {
  const c = clock();
  let calls = 0;
  let release = () => {};
  const gate = new Promise<void>((resolve) => { release = resolve; });
  const r = new Renewal(async () => { calls++; await gate; return true; }, 30000, c.read);

  const a = r.request();
  const b = r.request();
  release();

  assert.deepEqual(await Promise.all([a, b]), [true, true]);
  assert.equal(calls, 1);
});

test("a request inside the cooldown does not renew again", async () => {
  const c = clock();
  let calls = 0;
  const r = new Renewal(async () => { calls++; return true; }, 30000, c.read);

  await r.request();
  c.state.now = 29999;

  assert.equal(await r.request(), false);
  assert.equal(calls, 1);
});

test("a request after the cooldown renews again", async () => {
  const c = clock();
  let calls = 0;
  const r = new Renewal(async () => { calls++; return true; }, 30000, c.read);

  await r.request();
  c.state.now = 30000;
  await r.request();

  assert.equal(calls, 2);
});

test("a failed renewal reports false and does not wedge later attempts", async () => {
  const c = clock();
  let calls = 0;
  const r = new Renewal(async () => { calls++; throw new Error("gateway down"); }, 1000, c.read);

  assert.equal(await r.request(), false);
  c.state.now = 1000;
  assert.equal(await r.request(), false);
  assert.equal(calls, 2);
});
