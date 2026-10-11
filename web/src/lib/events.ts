import { useEffect } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import type { Event } from "../gen/nexus/v1/event_pb";
import { api, hubBaseUrl, isMock } from "./api";
import { getSettings } from "./settings";
import { clearSnapshot, restoreSnapshot, useEvents } from "./store";

/**
 * Global event stream: EventService.Subscribe(since_seq) with auto-reconnect, exponential backoff
 * with jitter, and the last seq persisted (via the store snapshot) so a reload resumes the stream.
 * Events are buffered and flushed into the store at most every 80 ms so replays stay cheap.
 */

const MIN_BACKOFF = 500;
const MAX_BACKOFF = 30_000;

let controller: AbortController | null = null;
let wake: (() => void) | null = null;
let buffer: Event[] = [];
let flushTimer: ReturnType<typeof setTimeout> | undefined;

function snapshotKey(): string | null {
  if (isMock()) return null; // the mock hub restarts from scratch on every load
  return `nexus.snapshot:${hubBaseUrl()}`;
}

function enqueue(ev: Event) {
  buffer.push(ev);
  if (!flushTimer) {
    flushTimer = setTimeout(flush, 80);
  }
}

function flush() {
  flushTimer = undefined;
  const batch = buffer;
  buffer = [];
  useEvents.getState().ingest(batch, { global: true });
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const t = setTimeout(done, ms);
    function done() {
      clearTimeout(t);
      signal.removeEventListener("abort", done);
      wake = null;
      resolve();
    }
    wake = done;
    signal.addEventListener("abort", done, { once: true });
  });
}

async function run(signal: AbortSignal) {
  const store = useEvents.getState;
  let attempt = 0;
  while (!signal.aborted) {
    store().setConnection({ status: attempt === 0 ? "connecting" : "reconnecting", retryAt: undefined });
    try {
      const stream = api().events.subscribe({ sinceSeq: store().lastSeq }, { signal });
      for await (const res of stream) {
        if (attempt !== 0 || store().connection.status !== "live") {
          attempt = 0;
          store().setConnection({ status: "live", error: undefined, attempt: 0 });
        }
        if (res.event) enqueue(res.event);
      }
      // Server closed the stream cleanly (e.g. deploy); reconnect promptly.
      if (signal.aborted) break;
      if (store().connection.status !== "live") attempt++;
    } catch (err) {
      if (signal.aborted) break;
      const ce = ConnectError.from(err);
      if (ce.code === Code.Canceled && signal.aborted) break;
      attempt++;
      store().setConnection({ error: ce.rawMessage || ce.message });
    }
    if (buffer.length) flush();
    const backoff = Math.min(MAX_BACKOFF, MIN_BACKOFF * 2 ** Math.max(0, attempt - 1));
    const delay = attempt === 0 ? 250 : backoff * (0.75 + Math.random() * 0.5);
    store().setConnection({
      status: attempt >= 4 ? "offline" : "reconnecting",
      retryAt: Date.now() + delay,
      attempt,
    });
    await sleep(delay, signal);
  }
}

/** Starts (or restarts) the global stream. Safe to call more than once. */
export function startEventStream() {
  stopEventStream();
  const key = snapshotKey();
  useEvents.getState().reset();
  restoreSnapshot(key);
  controller = new AbortController();
  void run(controller.signal);
}

export function stopEventStream() {
  controller?.abort();
  controller = null;
  if (flushTimer) clearTimeout(flushTimer);
  flushTimer = undefined;
  buffer = [];
}

/** Skip the current backoff wait and reconnect now. */
export function reconnectNow() {
  wake?.();
}

/** Forget the cached seq + snapshot and replay the whole log. */
export function resetEventCache() {
  clearSnapshot(snapshotKey());
  startEventStream();
}

// Reconnect immediately when the tab comes back online / visible.
if (typeof window !== "undefined") {
  window.addEventListener("online", () => reconnectNow());
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible" && useEvents.getState().connection.status !== "live") reconnectNow();
  });
}

/**
 * Per-task history: subscribes with task_ids=[id] from seq 0 while mounted, merging the replayed
 * history (and live events) into the task's timeline. The global stream keeps running alongside;
 * timelines dedupe by seq.
 */
export function useTaskHistory(taskId: string | undefined) {
  useEffect(() => {
    if (!taskId) return;
    const ac = new AbortController();
    let pending: Event[] = [];
    let timer: ReturnType<typeof setTimeout> | undefined;
    const flushLocal = () => {
      timer = undefined;
      const batch = pending;
      pending = [];
      useEvents.getState().ingest(batch, { global: false });
    };
    (async () => {
      let attempt = 0;
      while (!ac.signal.aborted) {
        try {
          const stream = api().events.subscribe({ sinceSeq: 0n, taskIds: [taskId] }, { signal: ac.signal });
          for await (const res of stream) {
            attempt = 0;
            if (res.event) {
              pending.push(res.event);
              if (!timer) timer = setTimeout(flushLocal, 60);
            }
          }
        } catch {
          /* fall through to backoff */
        }
        if (ac.signal.aborted) break;
        attempt++;
        await sleep(Math.min(MAX_BACKOFF, MIN_BACKOFF * 2 ** attempt), ac.signal);
      }
    })();
    return () => {
      ac.abort();
      if (timer) clearTimeout(timer);
    };
  }, [taskId]);
}

export function connectionLabel(): string {
  return getSettings().mock ? "Mock hub" : hubBaseUrl();
}
