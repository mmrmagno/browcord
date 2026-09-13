import { test } from "node:test";
import assert from "node:assert/strict";
import { RestartBudget } from "./restart.ts";

test("a fresh budget allows restarts up to the limit", () => {
  const budget = new RestartBudget(3, 60000);

  assert.equal(budget.take(0), true);
  assert.equal(budget.take(1000), true);
  assert.equal(budget.take(2000), true);
});

test("the budget refuses once the limit is spent inside the window", () => {
  const budget = new RestartBudget(2, 60000);

  budget.take(0);
  budget.take(1000);

  assert.equal(budget.take(2000), false);
});

test("restarts older than the window stop counting", () => {
  const budget = new RestartBudget(2, 10000);

  budget.take(0);
  budget.take(1000);
  assert.equal(budget.take(5000), false);

  assert.equal(budget.take(11001), true);
});

test("a spent budget recovers fully once the window clears", () => {
  const budget = new RestartBudget(1, 5000);

  assert.equal(budget.take(0), true);
  assert.equal(budget.take(4999), false);
  assert.equal(budget.take(5000), true);
  assert.equal(budget.take(5001), false);
});
