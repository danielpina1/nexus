/**
 * Tiny always-on counters for the perf HUD (toggle with the ` key or from ⌘K). Recording is a few
 * additions per flush, so it stays on in production builds; only the HUD UI is lazy-loaded.
 */

export interface PerfSample {
  /** Events received from the stream in the last second. */
  eventsPerSec: number;
  /** Store flushes in the last second. */
  flushesPerSec: number;
  /** Average / max time spent in store ingest during the last second (ms). */
  ingestAvgMs: number;
  ingestMaxMs: number;
  /** Total events ingested since load. */
  totalEvents: number;
}

let events = 0;
let flushes = 0;
let ingestTotal = 0;
let ingestMax = 0;
let totalEvents = 0;
let last: PerfSample = { eventsPerSec: 0, flushesPerSec: 0, ingestAvgMs: 0, ingestMaxMs: 0, totalEvents: 0 };
let windowStart = typeof performance !== "undefined" ? performance.now() : 0;

function roll() {
  const now = performance.now();
  if (now - windowStart < 1000) return;
  const secs = (now - windowStart) / 1000;
  last = {
    eventsPerSec: Math.round(events / secs),
    flushesPerSec: Math.round(flushes / secs),
    ingestAvgMs: flushes ? ingestTotal / flushes : 0,
    ingestMaxMs: ingestMax,
    totalEvents,
  };
  events = 0;
  flushes = 0;
  ingestTotal = 0;
  ingestMax = 0;
  windowStart = now;
}

export function recordFlush(count: number, ms: number) {
  roll();
  events += count;
  totalEvents += count;
  flushes++;
  ingestTotal += ms;
  if (ms > ingestMax) ingestMax = ms;
}

export function perfSample(): PerfSample {
  roll();
  return { ...last, totalEvents };
}

// Exposed for the e2e / perf scripts.
if (typeof window !== "undefined") {
  (window as unknown as { __nexusPerf: () => PerfSample }).__nexusPerf = perfSample;
}
