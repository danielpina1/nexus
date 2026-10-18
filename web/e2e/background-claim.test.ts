// Run: node --experimental-strip-types --test e2e/background-claim.test.ts
import { test } from "node:test";
import assert from "node:assert/strict";
import { claimsBackgroundWork, untrackedBackgroundWarning } from "../src/lib/backgroundClaim.ts";

test("flags answers that promise untracked background work", () => {
  for (const s of [
    "Agent is running in the background… I'll report back with a deals table once it finishes.",
    "The subagent is working in the background.",
    "I will get back to you when it's done.",
    "I'll report back shortly.",
  ]) {
    assert.ok(claimsBackgroundWork(s), s);
  }
});

test("ignores ordinary answers", () => {
  for (const s of ["Here are the best deals on Worten today.", "I can run tasks in parallel with teams.", "Background: Nexus is a personal assistant."]) {
    assert.ok(!claimsBackgroundWork(s), s);
  }
});

test("warns only when nothing tracked is running", () => {
  const a = "Agent is running in the background; I'll report back.";
  assert.equal(untrackedBackgroundWarning(a, false, 0), true);
  assert.equal(untrackedBackgroundWarning(a, false, 1), false); // a tracked child is running
  assert.equal(untrackedBackgroundWarning(a, true, 0), false); // the task itself is still active
  assert.equal(untrackedBackgroundWarning("Done.", false, 0), false);
});
