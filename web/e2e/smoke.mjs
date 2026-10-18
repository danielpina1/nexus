#!/usr/bin/env node
// End-to-end smoke test of the dashboard against a REAL Hub (not mock mode).
//
//   node web/e2e/smoke.mjs https://127.0.0.1:7701 <token>     (or: cd web && npm run e2e -- <url> <token>)
//
// Flow: Connect screen → token → Home → Chat "@echo … ?approve" → approve from Inbox → run succeeds
// → task page + event timeline → Vault search + note → Machines + file browser → Teams (validate,
// run, live tree, cancel) → Agent Architect → ⌘K palette.
//
// Needs Playwright once:  npm i --no-save playwright@1.56.1 && npx playwright install chromium
// (or set E2E_CHANNEL=chrome to drive the installed Google Chrome instead of downloading Chromium).
// The Hub's private CA is accepted (ignoreHTTPSErrors). Screenshots of failures go to
// $E2E_OUT (default: ./e2e-results). HEADED=1 shows the browser.

import { mkdirSync } from "node:fs";
import { resolve } from "node:path";

const [, , baseArg, tokenArg] = process.argv;
const base = (baseArg || process.env.NEXUS_URL || "https://127.0.0.1:7701").replace(/\/+$/, "");
const token = tokenArg || process.env.NEXUS_TOKEN;
if (!token) {
  console.error("usage: node web/e2e/smoke.mjs <hub-url> <token>   (token: security.dev_token or the one run-local.sh printed)");
  process.exit(2);
}
const out = resolve(process.env.E2E_OUT || "e2e-results");
mkdirSync(out, { recursive: true });

let chromium;
try {
  ({ chromium } = await import("playwright"));
} catch {
  console.error("Playwright is not installed. Run once in web/:\n  npm i --no-save playwright@1.56.1 && npx playwright install chromium\n(or E2E_CHANNEL=chrome with Google Chrome installed)");
  process.exit(2);
}

const browser = await chromium.launch({ headless: !process.env.HEADED, channel: process.env.E2E_CHANNEL || undefined });
const ctx = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1360, height: 860 } });
// Start clean: no stored token, dark theme.
await ctx.addInitScript(() => {
  if (!sessionStorage.getItem("e2e-init")) {
    localStorage.clear();
    sessionStorage.setItem("e2e-init", "1");
  }
});
const page = await ctx.newPage();
const consoleErrors = [];
page.on("pageerror", (e) => consoleErrors.push(`pageerror: ${e.message}`));
page.on("console", (m) => {
  if (m.type() === "error" && !/Failed to load resource/.test(m.text())) consoleErrors.push(m.text());
});
const failedRequests = [];
page.on("response", (r) => {
  if (r.status() >= 500 || (r.status() >= 400 && !r.url().includes("/assets/"))) failedRequests.push(`${r.status()} ${r.url().replace(base, "")}`);
});

const results = [];
const prompt = `smoke ${Date.now().toString(36)} hello from the e2e ?approve`;
let taskUrl = "";

async function step(name, fn) {
  const t0 = Date.now();
  try {
    await fn();
    const ms = Date.now() - t0;
    results.push({ name, ok: true, ms });
    console.log(`  ok   ${name}  (${ms} ms)`);
  } catch (e) {
    const ms = Date.now() - t0;
    const file = `${out}/fail-${results.length + 1}.png`;
    await page.screenshot({ path: file, fullPage: false }).catch(() => {});
    results.push({ name, ok: false, ms, error: String(e?.message || e).split("\n")[0] });
    console.log(`  FAIL ${name}  (${ms} ms)\n       ${String(e?.message || e).split("\n")[0]}\n       screenshot: ${file}`);
  }
}

const go = (path) => page.goto(base + path, { waitUntil: "domcontentloaded" });

console.log(`Nexus smoke test against ${base}`);

await step("Connect screen appears without a token", async () => {
  await go("/");
  await page.getByRole("heading", { name: "Connect to your Nexus" }).waitFor({ timeout: 15_000 });
});

await step("Wrong token is rejected inline", async () => {
  await page.getByTestId("connect-token").fill("not-a-real-token");
  await page.getByTestId("connect-submit").click();
  await page.getByRole("alert").filter({ hasText: /rejected|reach/i }).waitFor({ timeout: 10_000 });
});

await step("Connect with the token → Home", async () => {
  await page.getByTestId("connect-token").fill(token);
  const t0 = Date.now();
  await page.getByTestId("connect-submit").click();
  await page.getByRole("heading", { name: /^Good (morning|afternoon|evening|night)/ }).waitFor({ timeout: 15_000 });
  await page.getByText("Live", { exact: true }).first().waitFor({ timeout: 15_000 });
  console.log(`       connected and live in ${Date.now() - t0} ms`);
});

await step("Home shows machines", async () => {
  await page.getByRole("region", { name: "Machines" }).getByRole("link").first().waitFor({ timeout: 10_000 });
});

await step("Chat: run @echo with ?approve", async () => {
  await go("/chat");
  const input = page.getByTestId("command-input");
  await input.waitFor();
  await input.fill(`@echo ${prompt}`);
  await input.press("Enter");
  // The run view streams on the right; the task waits for approval.
  await page.getByTestId("run-view").getByText(/Waiting approval/i).first().waitFor({ timeout: 30_000 });
});

await step("Inbox: approve it (optimistic)", async () => {
  await go("/inbox");
  const card = page.locator("[data-testid=approval-card]").filter({ hasText: /echo|approve|smoke/i }).first();
  await card.waitFor({ timeout: 15_000 });
  const ms = await card.evaluate(
    (el) =>
      new Promise((done) => {
        const btn = [...el.querySelectorAll("button")].find((b) => b.textContent.trim() === "Approve");
        const t0 = performance.now();
        const mo = new MutationObserver(() => {
          if (!el.isConnected) {
            mo.disconnect();
            done(performance.now() - t0);
          }
        });
        mo.observe(document.body, { childList: true, subtree: true });
        btn.click();
      }),
  );
  console.log(`       card left the inbox ${ms.toFixed(1)} ms after the click (optimistic)`);
  await page.getByText("Approved", { exact: true }).first().waitFor({ timeout: 10_000 });
});

await step("Task succeeds and its page opens", async () => {
  await go("/tasks");
  const row = page.getByRole("link").filter({ hasText: prompt.split(" ?")[0].slice(0, 20) }).first();
  await row.waitFor({ timeout: 15_000 });
  await row.click();
  await page.waitForURL(/\/tasks\/tsk_/);
  taskUrl = page.url();
  await page.getByText("Succeeded").first().waitFor({ timeout: 30_000 });
  await page.getByTestId("run-view").getByText("hello", { exact: false }).first().waitFor({ timeout: 10_000 });
});

await step("Task timeline lists events", async () => {
  await page.getByRole("tab", { name: /Event timeline/ }).click();
  await page.getByText(/\d+ events/).waitFor({ timeout: 10_000 });
  const n = Number((await page.getByText(/\d+ events/).first().textContent()).match(/\d+/)[0]);
  if (n < 3) throw new Error(`only ${n} events`);
});

await step("Vault: search and open a note", async () => {
  await go("/vault");
  const search = page.getByPlaceholder("Search the vault…");
  await search.waitFor({ timeout: 10_000 });
  await search.fill("echo");
  const hit = page.getByTestId("vault-hit").first();
  await hit.waitFor({ timeout: 10_000 });
  await hit.click();
  await page.getByTestId("note-body").waitFor({ timeout: 10_000 });
});

await step("Machines: node online, files browsable", async () => {
  await go("/machines");
  const card = page.getByTestId("node-card").first();
  await card.waitFor({ timeout: 10_000 });
  await card.click();
  await page.waitForURL(/\/machines\//);
  await page.getByRole("tab", { name: "Files" }).click();
  await page.getByTestId("file-row").first().waitFor({ timeout: 15_000 });
});

let teamRunUrl = "";
await step("Teams: list, detail and live validation", async () => {
  await go("/teams");
  const card = page.getByTestId("team-card-link").first();
  await card.waitFor({ timeout: 15_000 });
  await card.click();
  await page.waitForURL(/\/teams\/[^/]+$/);
  await page.getByText("Run this team").waitFor({ timeout: 10_000 });
  await page.getByRole("tab", { name: "Edit" }).click();
  const md = page.getByTestId("team-markdown");
  await md.waitFor();
  await page.getByText(/^Valid/).first().waitFor({ timeout: 10_000 });
  // Break the strategy: UpsertTeam{validate_only} reports it and Save is disabled.
  const text = await md.inputValue();
  await md.fill(text.replace(/^strategy:.*$/m, "strategy: nonsense"));
  await page.getByText(/problems? — fix before saving/).waitFor({ timeout: 10_000 });
  if (!(await page.getByTestId("team-save").isDisabled())) throw new Error("Save should be disabled while invalid");
});

await step("Teams: start a run, see the live tree, cancel it", async () => {
  await page.getByRole("tab", { name: "Overview" }).click();
  await page.getByTestId("team-run-prompt").fill(`smoke team ${Date.now().toString(36)}: one short sentence about the sea`);
  await page.getByTestId("team-run-submit").click();
  await page.waitForURL(/\/teams\/runs\/tsk_/, { timeout: 15_000 });
  teamRunUrl = page.url();
  await page.getByTestId("team-tree-node").first().waitFor({ timeout: 15_000 });
  await page.getByText("Totals against limits").count();
  const cancel = page.getByTestId("team-run-cancel");
  if (await cancel.count()) {
    await cancel.click();
    await page.getByRole("button", { name: "Confirm cancel" }).click();
    await page.getByText(/Cancelled|Failed|Succeeded/).first().waitFor({ timeout: 20_000 });
  }
});

await step("Architect: start screen and draft list", async () => {
  await go("/architect");
  await page.getByTestId("architect-request").waitFor({ timeout: 10_000 });
  await page.getByText("Drafts", { exact: true }).waitFor();
  // The list resolves to rows or the empty state (no endless skeleton).
  await page.locator("[data-testid=draft-row], text=/No open drafts|No drafts yet/").first().waitFor({ timeout: 10_000 });
});

await step("⌘K offers “Create an agent…”", async () => {
  await page.keyboard.press(process.platform === "darwin" ? "Meta+k" : "Control+k");
  const input = page.getByTestId("palette-input");
  await input.waitFor({ timeout: 5_000 });
  await input.fill("create an agent");
  await page.getByRole("option", { name: /Create an agent/ }).first().waitFor({ timeout: 5_000 });
  await page.keyboard.press("Escape");
});

await step("⌘K palette jumps to Inbox", async () => {
  await go("/");
  await page.getByRole("heading", { name: /^Good/ }).waitFor();
  await page.keyboard.press(process.platform === "darwin" ? "Meta+k" : "Control+k");
  const input = page.getByTestId("palette-input");
  await input.waitFor({ timeout: 5_000 });
  await input.fill("inbox");
  await page.keyboard.press("Enter");
  await page.waitForURL(/\/inbox$/, { timeout: 5_000 });
});

await step("Reload resumes from the cached seq (no Connect screen)", async () => {
  const t0 = Date.now();
  await page.reload();
  await page.getByRole("heading", { name: "Inbox" }).waitFor({ timeout: 10_000 });
  await page.getByText("Live", { exact: true }).first().waitFor({ timeout: 10_000 });
  console.log(`       live again ${Date.now() - t0} ms after reload`);
});

await step("Offline → online catches up on missed events", async () => {
  await go("/tasks");
  await page.getByRole("heading", { name: "Tasks" }).waitFor();
  await ctx.setOffline(true);
  await page.getByText(/Offline|Reconnecting/).first().waitFor({ timeout: 15_000 });
  // Meanwhile, another session starts a task.
  const other = await browser.newContext({ ignoreHTTPSErrors: true });
  await other.addInitScript((t) => localStorage.setItem("nexus.settings", JSON.stringify({ token: t })), token);
  const p2 = await other.newPage();
  await p2.goto(base + "/chat");
  const marker = `catchup ${Date.now().toString(36)}`;
  await p2.getByTestId("command-input").fill(`@echo ${marker}`);
  await p2.getByTestId("command-input").press("Enter");
  await p2.getByTestId("run-view").getByText("Succeeded").first().waitFor({ timeout: 30_000 });
  await other.close();
  // DevTools offline emulation only affects new requests, so make sure the old stream is really gone.
  const seenEarly = await page.getByText(marker).count();
  if (seenEarly) console.log("       (note: event arrived over the old connection while offline)");
  const t0 = Date.now();
  await ctx.setOffline(false);
  await page.getByText(marker).first().waitFor({ timeout: 20_000 });
  console.log(`       caught up ${Date.now() - t0} ms after coming back online`);
});

await browser.close();

const failed = results.filter((r) => !r.ok);
console.log(`\n${results.length - failed.length}/${results.length} steps passed${taskUrl ? ` · task: ${taskUrl}` : ""}${teamRunUrl ? ` · team run: ${teamRunUrl}` : ""}`);
if (consoleErrors.length) console.log(`console errors:\n  ${[...new Set(consoleErrors)].slice(0, 10).join("\n  ")}`);
const unexpected = failedRequests.filter((r) => !/^401 /.test(r));
if (unexpected.length) console.log(`failed requests:\n  ${[...new Set(unexpected)].slice(0, 10).join("\n  ")}`);
process.exit(failed.length ? 1 : 0);
