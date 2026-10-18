#!/usr/bin/env node
// Streaming perf check in demo mode: one run streaming N chunks as fast as the mock hub can,
// sampled through the perf HUD (window.__nexusHud) once per second.
//
//   node web/e2e/perf.mjs <dashboard-url> [chunks=5000] [page=chat|task]
//
// Works against any URL that serves the built dashboard (the Hub, or `npm run preview`); it
// switches the browser to demo mode, so no token is needed. Needs Playwright (see smoke.mjs).

const [, , baseArg, nArg, pageArg] = process.argv;
const base = (baseArg || "https://127.0.0.1:7701").replace(/\/+$/, "");
const chunks = Number(nArg || 5000);
const where = pageArg || "chat";

let chromium;
try {
  ({ chromium } = await import("playwright"));
} catch {
  console.error("Playwright is not installed (npm i --no-save playwright@1.56.1 && npx playwright install chromium)");
  process.exit(2);
}
const browser = await chromium.launch({ channel: process.env.E2E_CHANNEL || undefined });
const ctx = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1440, height: 900 } });
await ctx.addInitScript(() => {
  localStorage.setItem("nexus.settings", JSON.stringify({ mock: true, theme: "dark" }));
  localStorage.setItem("nexus.perfHud", "true");
});
const page = await ctx.newPage();
await page.goto(base + "/chat");
await page.getByTestId("command-input").fill(`@assistant stress ${chunks}`);
await page.getByTestId("command-input").press("Enter");
await page.getByTestId("run-view").waitFor();
if (where === "task") {
  await page.getByTestId("run-view").getByRole("link").first().click();
  await page.waitForURL(/\/tasks\//);
}
const samples = [];
const t0 = Date.now();
for (;;) {
  await page.waitForTimeout(1000);
  const s = await page.evaluate(() => window.__nexusHud);
  if (s) samples.push(s);
  const done = await page.getByTestId("run-view").getByText(/Streamed \d+ chunks/).count();
  if (done || Date.now() - t0 > 120_000) break;
}
await page.waitForTimeout(1200);
const busy = samples.filter((s) => s.eventsPerSec > 0);
const avg = (k) => busy.reduce((a, s) => a + s[k], 0) / Math.max(1, busy.length);
const max = (k) => Math.max(...busy.map((s) => s[k]));
const min = (k) => Math.min(...busy.map((s) => s[k]));
const last = samples[samples.length - 1] ?? {};
console.log(
  JSON.stringify(
    {
      page: where,
      chunks,
      seconds: busy.length,
      eventsPerSec: { avg: Math.round(avg("eventsPerSec")), max: max("eventsPerSec") },
      fps: { avg: Math.round(avg("fps")), min: min("fps") },
      worstFrameMs: Math.round(max("worstFrame")),
      ingestMs: { avg: +avg("ingestAvgMs").toFixed(2), max: +max("ingestMaxMs").toFixed(2) },
      flushesPerSec: Math.round(avg("flushesPerSec")),
      domNodes: last.dom,
      heapMb: last.heapMb && +last.heapMb.toFixed(1),
    },
    null,
    2,
  ),
);
await browser.close();
