import { test } from "node:test";
import assert from "node:assert/strict";
import { RevealGate } from "./gate.ts";

test("video arriving alone never reveals the room", () => {
  let revealed = 0;
  const gate = new RevealGate(() => revealed++);

  gate.stream();

  assert.equal(revealed, 0);
  assert.equal(gate.joined, false);
});

test("tapping join alone never reveals the room", () => {
  let revealed = 0;
  const gate = new RevealGate(() => revealed++);

  gate.join();

  assert.equal(revealed, 0);
  assert.equal(gate.joined, true);
});

test("video then join reveals exactly once", () => {
  let revealed = 0;
  const gate = new RevealGate(() => revealed++);

  gate.stream();
  gate.join();

  assert.equal(revealed, 1);
});

test("join then video reveals exactly once", () => {
  let revealed = 0;
  const gate = new RevealGate(() => revealed++);

  gate.join();
  gate.stream();

  assert.equal(revealed, 1);
});

test("repeated signals reveal only once", () => {
  let revealed = 0;
  const gate = new RevealGate(() => revealed++);

  gate.stream();
  gate.stream();
  gate.join();
  gate.join();
  gate.stream();

  assert.equal(revealed, 1);
});
