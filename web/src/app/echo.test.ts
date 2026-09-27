import { test } from "node:test";
import assert from "node:assert/strict";
import { EchoTracker, LONG_MS, PRESS_MAX_MS, RELEASE_MS, placeRing } from "./echo.ts";

const box = { left: 10, top: 20, width: 200, height: 100 };

test("a ring lands at the content point, offset by the letterbox", () => {
  assert.deepEqual(placeRing(0, 0, box), { left: 10, top: 20 });
  assert.deepEqual(placeRing(0.5, 0.5, box), { left: 110, top: 70 });
  assert.deepEqual(placeRing(1, 1, box), { left: 210, top: 120 });
});

test("coordinates outside the content are clamped to its edge", () => {
  assert.deepEqual(placeRing(-0.5, 2, box), { left: 10, top: 120 });
});

test("a press stays pressed until the button comes up", () => {
  const echo = new EchoTracker();

  const [ring] = echo.apply({ kind: "down", x: 0.5, y: 0.5 }, box, 0);
  assert.equal(ring.state, "pressed");
  assert.deepEqual(echo.expire(1000), []);
  assert.equal(echo.all()[0].state, "pressed");

  const [released] = echo.apply({ kind: "up", x: 0.6, y: 0.6 }, box, 1200);
  assert.equal(released.id, ring.id);
  assert.equal(released.state, "released");
  assert.equal(released.left, 110);
  assert.equal(released.until, 1200 + RELEASE_MS);
});

test("a released ring is gone once its animation has run", () => {
  const echo = new EchoTracker();

  echo.apply({ kind: "down", x: 0.5, y: 0.5 }, box, 0);
  echo.apply({ kind: "up", x: 0.5, y: 0.5 }, box, 100);

  assert.deepEqual(echo.expire(100 + RELEASE_MS - 1), []);
  assert.equal(echo.expire(100 + RELEASE_MS).length, 1);
  assert.deepEqual(echo.all(), []);
  assert.equal(echo.nextExpiry(), null);
});

test("a tap is a press and release in one ring", () => {
  const echo = new EchoTracker();

  const changed = echo.apply({ kind: "tap", x: 0.25, y: 0.5 }, box, 50);

  assert.equal(changed.length, 1);
  assert.equal(changed[0].state, "released");
  assert.equal(changed[0].left, 60);
  assert.equal(changed[0].until, 50 + RELEASE_MS);
});

test("a long press gets its own ring variant and lifetime", () => {
  const echo = new EchoTracker();

  const [ring] = echo.apply({ kind: "long", x: 0.5, y: 0.5 }, box, 0);

  assert.equal(ring.state, "long");
  assert.equal(ring.until, LONG_MS);
  assert.equal(echo.nextExpiry(), LONG_MS);
});

test("an up with no matching down still echoes a release", () => {
  const echo = new EchoTracker();

  const [ring] = echo.apply({ kind: "up", x: 0, y: 0 }, box, 10);

  assert.equal(ring.state, "released");
  assert.equal(ring.until, 10 + RELEASE_MS);
});

test("a second down releases the ring that was still held", () => {
  const echo = new EchoTracker();

  const [first] = echo.apply({ kind: "down", x: 0.1, y: 0.1 }, box, 0);
  const changed = echo.apply({ kind: "down", x: 0.9, y: 0.9 }, box, 300);

  assert.equal(changed.length, 2);
  assert.equal(changed[0].id, first.id);
  assert.equal(changed[0].state, "released");
  assert.equal(changed[1].state, "pressed");
  assert.notEqual(changed[1].id, first.id);
});

test("a press whose up never arrives is cleared eventually", () => {
  const echo = new EchoTracker();

  echo.apply({ kind: "down", x: 0.5, y: 0.5 }, box, 0);

  assert.equal(echo.expire(PRESS_MAX_MS).length, 1);
  const [ring] = echo.apply({ kind: "up", x: 0.5, y: 0.5 }, box, PRESS_MAX_MS + 1);
  assert.equal(ring.state, "released");
  assert.equal(echo.all().length, 1);
});
