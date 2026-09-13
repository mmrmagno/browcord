import { test } from "node:test";
import assert from "node:assert/strict";
import { describeActivity, siteOf, PARTY_MAX, PresenceReporter } from "./presence.ts";

test("siteOf reduces a url to a bare host", () => {
  assert.equal(siteOf("https://www.youtube.com/watch?v=abc"), "youtube.com");
  assert.equal(siteOf("https://en.wikipedia.org/wiki/Opus"), "en.wikipedia.org");
  assert.equal(siteOf("not a url"), "");
  assert.equal(siteOf(""), "");
});

test("alone in the room reads as solo, not as zero others", () => {
  const a = describeActivity("youtube.com", 1, "main", 1000);

  assert.equal(a.state, "Browsing solo");
  assert.equal(a.assets.small_text, "Solo");
  assert.equal(a.details, "Browsing youtube.com");
});

test("one other viewer is singular", () => {
  const a = describeActivity("github.com", 2, "main", 1000);

  assert.equal(a.state, "Sharing control with 1 other");
  assert.equal(a.assets.small_text, "Shared control");
});

test("several viewers are plural and exclude yourself", () => {
  const a = describeActivity("twitch.tv", 4, "main", 1000);

  assert.equal(a.state, "Sharing control with 3 others");
});

test("a room with no page yet does not claim to browse nothing", () => {
  const a = describeActivity("", 1, "main", 1000);

  assert.equal(a.details, "Opening a room");
});

test("party carries the room id and never reports a size below one", () => {
  const a = describeActivity("example.com", 0, "room-7", 1000);

  assert.equal(a.party.id, "room-7");
  assert.deepEqual(a.party.size, [1, PARTY_MAX]);
});

test("timestamps carry only a start so discord counts elapsed, not a countdown", () => {
  const a = describeActivity("example.com", 2, "main", 1789224993000);

  assert.deepEqual(a.timestamps, { start: 1789224993000 });
  assert.ok(!("end" in a.timestamps));
});

test("a refused setActivity is reported, an unsupported one is not", async () => {
  const errors: string[] = [];

  const refused = new PresenceReporter(
    () => Promise.reject({ code: 4006, message: "no permission" }),
    "main",
    (m) => errors.push(m),
  );
  refused.setSite("https://example.com");
  await new Promise((r) => setTimeout(r, 30));

  assert.equal(errors.length, 1);
  assert.match(errors[0], /code=4006/);

  const unsupported = new PresenceReporter(
    () => Promise.reject({ code: 5012, message: "Command not available for this application" }),
    "main",
    (m) => errors.push(m),
  );
  unsupported.setSite("https://example.com");
  await new Promise((r) => setTimeout(r, 30));

  assert.equal(errors.length, 1, "5012 must not be reported as an error");
  assert.equal(unsupported.unsupported, true);
});
