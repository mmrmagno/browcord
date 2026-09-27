import { test } from "node:test";
import assert from "node:assert/strict";
import { parseRoomState, statusNote } from "./status.ts";

test("no change produces no note", () => {
  assert.equal(statusNote("live", "live"), null);
  assert.equal(statusNote("offline", "offline"), null);
});

test("offline and recovering notes are sticky and do not resync", () => {
  for (const next of ["offline", "recovering"] as const) {
    const n = statusNote("live", next);
    assert.ok(n);
    assert.equal(n.holdMs, 0);
    assert.equal(n.tone, "warn");
    assert.equal(n.resync, false);
  }
});

test("returning to live resyncs the player", () => {
  const n = statusNote("recovering", "live");
  assert.ok(n);
  assert.equal(n.resync, true);
  assert.equal(n.tone, "live");
});

test("unknown states are refused", () => {
  assert.equal(parseRoomState("live"), "live");
  assert.equal(parseRoomState("ending"), "ending");
  assert.equal(parseRoomState("full"), "full");
  assert.equal(parseRoomState("exec"), null);
  assert.equal(parseRoomState(3), null);
});
