import { useEffect } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import type { Event } from "../gen/nexus/v1/event_pb";
import { TaskState } from "../gen/nexus/v1/task_pb";
import { api, hubBaseUrl, isMock } from "./api";
import { getSettings } from "./settings";
import { clearSnapshot, restoreSnapshot, useEvents } from "./store";
import { recordFlush } from "./perf";
import { queryClient } from "./queryClient";

/**
 * The ONE shared event stream: EventService.Subscribe(since_seq).
 *
 * - Resumes from the last seq (persisted with a compact snapshot of derived state, so a reload
 *   resumes without losing the inbox).
 * - Exponential backoff with jitter (0.5 s → 30 s); Unauthenticated stops and asks for a token.
 * - Sleep/wake: the Hub sends no heartbeats, so a laptop that slept may hold a dead socket that
 *   never errors. We force a fresh subscription (from the last seq) when the tab becomes visible
 *   after being hidden, when the browser comes back online, and when a timer notices the clock
 *   jumped (the machine was asleep). Active TanStack queries are invalidated on catch-up.
 * - Events are buffered and applied once per animation frame (a timer when the tab is hidden), so a
 *   burst of thousands of chunks is a handful of store updates.
 * - Query cache invalidation is driven by events, not polling.
 */

const MIN_BACKOFF = 500;
const MAX_BACKOFF = 30_000;
const HIDDEN_RESYNC_MS = 15_000;
const WAKE_TICK_MS = 5_000;
const LIVENESS_MS = 45_000;

let controller: AbortController | null = null;
/** Aborts only the current attempt (forced reconnect), not the loop. */
let attempt: AbortController | null = null;
let wake: (() => void) | null = null;
let buffer: Event[] = [];
let flushScheduled = false;
let catchUp = false;

function snapshotKey(): string | null {
  if (isMock()) return null; // the mock hub restarts from scratch on every load
  return `nexus.snapshot:${hubBaseUrl()}`;
}

function enqueue(ev: Event) {
  buffer.push(ev);
  scheduleFlush();
}

function scheduleFlush() {
  if (flushScheduled) return;
  flushScheduled = true;
  // rAF coalesces to one store update per frame while visible; it doesn't fire in background
  // tabs, so a timer covers that case (whichever comes first wins).
  const t = setTimeout(flush, document.visibilityState === "visible" ? 100 : 400);
  if (document.visibilityState === "visible") {
    requestAnimationFrame(() => {
      clearTimeout(t);
      flush();
    });
  }
}

function flush() {
  if (!flushScheduled) return;
  flushScheduled = false;
  const batch = buffer;
  buffer = [];
  if (batch.length === 0) return;
  const t0 = performance.now();
  useEvents.getState().ingest(batch, { global: true });
  recordFlush(batch.length, performance.now() - t0);
  invalidateFromEvents(batch);
}

// ---- event-driven cache invalidation ------------------------------------------------------------

let pendingKeys = new Map<string, readonly unknown[]>();
let invalidateTimer: ReturnType<typeof setTimeout> | undefined;

function invalidateFromEvents(batch: Event[]) {
  const knownNodes = queryClient.getQueryData<Array<{ id: string }>>(["nodes"]);
  for (const ev of batch) {
    const b = ev.body;
    if (b.case === "taskUpdated" && b.value.task) {
      const t = b.value.task;
      pendingKeys.set(`task:${t.id}`, ["task", t.id]);
      if (t.state === TaskState.SUCCEEDED) {
        // Agents write notes; refresh the vault views if they're open.
        pendingKeys.set("graph", ["graph"]);
        pendingKeys.set("note", ["note"]);
      }
    } else if (b.case === "nodeStatus") {
      if (!knownNodes || !knownNodes.some((n) => n.id === b.value.nodeId)) pendingKeys.set("nodes", ["nodes"]);
      pendingKeys.set(`node:${b.value.nodeId}`, ["node", b.value.nodeId]);
    } else if (b.case === "notice") {
      pendingKeys.set("devices", ["devices"]);
    }
  }
  if (pendingKeys.size && !invalidateTimer) {
    invalidateTimer = setTimeout(() => {
      invalidateTimer = undefined;
      const keys = pendingKeys;
      pendingKeys = new Map();
      // Only active queries refetch; inactive ones are just marked stale.
      for (const k of keys.values()) void queryClient.invalidateQueries({ queryKey: k, refetchType: "active" });
    }, 500);
  }
}

// ---- stream loop --------------------------------------------------------------------------------

function sleep(ms: number, signal: AbortSignal, wakeable = true): Promise<void> {
  return new Promise((resolve) => {
    const t = Number.isFinite(ms) ? setTimeout(done, ms) : undefined;
    function done() {
      clearTimeout(t);
      signal.removeEventListener("abort", done);
      if (wakeable && wake === done) wake = null;
      resolve();
    }
    if (wakeable) wake = done;
    signal.addEventListener("abort", done, { once: true });
  });
}

function withTimeout(signal: AbortSignal, ms: number): { signal: AbortSignal; cancel: () => void } {
  const ac = new AbortController();
  const t = setTimeout(() => ac.abort(), ms);
  const on = () => ac.abort();
  signal.addEventListener("abort", on, { once: true });
  return {
    signal: ac.signal,
    cancel: () => {
      clearTimeout(t);
      signal.removeEventListener("abort", on);
    },
  };
}

/**
 * The Hub's Subscribe sends no response headers until the first event and no heartbeats, so an
 * idle-but-healthy stream is indistinguishable from a hung one. A cheap unary call over the same
 * connection tells us whether the Hub is reachable and the token is accepted.
 */
async function probe(signal: AbortSignal): Promise<"ok" | "unauthorized" | "failed"> {
  const t = withTimeout(signal, 10_000);
  try {
    await api().nodes.listNodes({}, { signal: t.signal });
    return "ok";
  } catch (err) {
    const code = ConnectError.from(err).code;
    return code === Code.Unauthenticated || code === Code.PermissionDenied ? "unauthorized" : "failed";
  } finally {
    t.cancel();
  }
}

let lastActivity = 0;

async function run(signal: AbortSignal) {
  const store = useEvents.getState;
  let failures = 0;
  while (!signal.aborted) {
    if (typeof navigator !== "undefined" && navigator.onLine === false) {
      // No network: don't spin; the "online" event wakes us and we resume from the last seq.
      store().setConnection({ status: "offline", error: "This device is offline", retryAt: undefined });
      catchUp = true;
      await sleep(Infinity, signal);
      continue;
    }
    store().setConnection({ status: failures === 0 ? "connecting" : "reconnecting", retryAt: undefined });
    const ac = new AbortController();
    attempt = ac;
    const onAbort = () => ac.abort();
    signal.addEventListener("abort", onAbort, { once: true });
    let outcome = "closed" as "forced" | "unauthorized" | "error" | "closed";
    let errorMsg = "";
    let live = false;
    const markLive = () => {
      if (live || attempt !== ac) return;
      live = true;
      failures = 0;
      lastActivity = Date.now();
      store().setConnection({ status: "live", error: undefined, attempt: 0, since: Date.now() });
      if (catchUp) {
        catchUp = false;
        void queryClient.invalidateQueries({ refetchType: "active" });
      }
    };
    void probe(ac.signal).then((r) => {
      if (r === "ok") markLive();
      else if (r === "unauthorized" && attempt === ac) {
        outcome = "unauthorized";
        ac.abort();
      }
    });
    try {
      const stream = api().events.subscribe({ sinceSeq: store().lastSeq }, { signal: ac.signal });
      for await (const res of stream) {
        markLive();
        lastActivity = Date.now();
        if (res.event) enqueue(res.event);
      }
    } catch (err) {
      if (!signal.aborted && outcome !== "unauthorized") {
        if (ac.signal.aborted) {
          outcome = "forced";
        } else {
          const ce = ConnectError.from(err);
          errorMsg = ce.rawMessage || ce.message;
          outcome = ce.code === Code.Unauthenticated || ce.code === Code.PermissionDenied ? "unauthorized" : "error";
        }
      }
    } finally {
      signal.removeEventListener("abort", onAbort);
      if (attempt === ac) attempt = null;
    }
    if (buffer.length) flush();
    if (signal.aborted) break;
    if (outcome === "unauthorized") {
      store().setConnection({ status: "unauthorized", error: errorMsg || "The Hub rejected the access token", retryAt: undefined });
      // Wait until settings change (startEventStream) or someone asks to retry.
      await sleep(Infinity, signal);
      continue;
    }
    if (outcome === "forced") {
      catchUp = true;
      continue;
    }
    if (outcome === "error" || !live) failures++;
    if (errorMsg) store().setConnection({ error: errorMsg });
    // A clean close (Hub restart/deploy) after being live reconnects almost at once.
    const delay = failures === 0 ? 250 : Math.min(MAX_BACKOFF, MIN_BACKOFF * 2 ** (failures - 1)) * (0.75 + Math.random() * 0.5);
    catchUp = true;
    store().setConnection({
      status: failures >= 4 || (typeof navigator !== "undefined" && navigator.onLine === false) ? "offline" : "reconnecting",
      retryAt: Date.now() + delay,
      attempt: failures,
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
  buffer = [];
  flushScheduled = false;
}

/** Skip the current backoff wait and reconnect now. */
export function reconnectNow() {
  wake?.();
}

/**
 * Drop the current subscription (which may be a dead socket after sleep) and resubscribe from the
 * last seq right away. The store is kept; missed events replay.
 */
export function forceReconnect() {
  if (!controller) return;
  if (attempt) attempt.abort();
  else wake?.();
}

/** Forget the cached seq + snapshot and replay the whole log. */
export function resetEventCache() {
  clearSnapshot(snapshotKey());
  startEventStream();
}

// ---- sleep / wake / network detection -----------------------------------------------------------

if (typeof window !== "undefined") {
  let hiddenAt = 0;
  window.addEventListener("online", () => forceReconnect());
  window.addEventListener("offline", () => {
    // The socket under the stream is gone (or about to be); drop it instead of waiting for a timeout.
    if (useEvents.getState().connection.status !== "unauthorized") forceReconnect();
  });
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "hidden") {
      hiddenAt = Date.now();
      return;
    }
    const status = useEvents.getState().connection.status;
    if (status === "unauthorized") return;
    if (status !== "live" || (hiddenAt && Date.now() - hiddenAt > HIDDEN_RESYNC_MS)) forceReconnect();
    hiddenAt = 0;
  });
  // Restored from the back/forward cache: sockets are gone.
  window.addEventListener("pageshow", (e) => {
    if (e.persisted) forceReconnect();
  });
  // Timers stall while the machine sleeps; a big gap between ticks means we just woke up.
  let lastTick = Date.now();
  let probing = false;
  setInterval(() => {
    const now = Date.now();
    if (now - lastTick > WAKE_TICK_MS * 3) forceReconnect();
    lastTick = now;
    // Quiet for a while: make sure the connection under the stream is still alive.
    const c = useEvents.getState().connection;
    if (c.status === "live" && !probing && document.visibilityState === "visible" && now - lastActivity > LIVENESS_MS && controller) {
      probing = true;
      void probe(controller.signal).then((r) => {
        probing = false;
        if (r === "ok") lastActivity = Date.now();
        else forceReconnect();
      });
    }
  }, WAKE_TICK_MS);
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
    let scheduled = false;
    const flushLocal = () => {
      if (!scheduled) return;
      scheduled = false;
      const batch = pending;
      pending = [];
      if (batch.length) useEvents.getState().ingest(batch, { global: false });
    };
    const schedule = () => {
      if (scheduled) return;
      scheduled = true;
      const t = setTimeout(flushLocal, 120);
      requestAnimationFrame(() => {
        clearTimeout(t);
        flushLocal();
      });
    };
    (async () => {
      let failures = 0;
      let since = 0n;
      while (!ac.signal.aborted) {
        try {
          const stream = api().events.subscribe({ sinceSeq: since, taskIds: [taskId] }, { signal: ac.signal });
          for await (const res of stream) {
            failures = 0;
            if (res.event) {
              if (res.event.seq > since) since = res.event.seq;
              pending.push(res.event);
              schedule();
            }
          }
        } catch (err) {
          if (ConnectError.from(err).code === Code.Unauthenticated) return;
        }
        if (ac.signal.aborted) break;
        failures++;
        await sleep(Math.min(MAX_BACKOFF, MIN_BACKOFF * 2 ** failures), ac.signal, false);
      }
    })();
    return () => {
      ac.abort();
      flushLocal();
    };
  }, [taskId]);
}

export function connectionLabel(): string {
  return getSettings().mock ? "Demo hub" : hubBaseUrl();
}
