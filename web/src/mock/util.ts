import { timestampFromMs } from "@bufbuild/protobuf/wkt";

export const MIN = 60_000;
export const HOUR = 60 * MIN;
export const DAY = 24 * HOUR;

export const ts = (ms: number) => timestampFromMs(Math.floor(ms));

const CROCK = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";
let counter = 0;
/** ULID-ish ids with the Hub's prefixes (tsk_, apr_, ...). */
export function newId(prefix: string, at = Date.now()): string {
  let t = Math.floor(at);
  let time = "";
  for (let i = 0; i < 10; i++) {
    time = CROCK[t % 32] + time;
    t = Math.floor(t / 32);
  }
  counter = (counter + 1) % 32768;
  let rnd = "";
  let seed = (counter * 2654435761) >>> 0;
  for (let i = 0; i < 16; i++) {
    seed = (seed * 1103515245 + 12345) >>> 0;
    rnd += CROCK[(seed >>> 16) % 32];
  }
  return `${prefix}_${time}${rnd}`;
}

export function randomBytes(n: number): Uint8Array {
  const b = new Uint8Array(n);
  crypto.getRandomValues(b);
  return b;
}

export const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

export function latency(): Promise<void> {
  return sleep(80 + Math.random() * 220);
}

/** Deterministic pseudo-random in [0,1) from an integer seed. */
export function prand(seed: number): number {
  const x = Math.sin(seed * 12.9898 + 78.233) * 43758.5453;
  return x - Math.floor(x);
}

export const eur = (cents: number) => ({ currency: "EUR", minorUnits: BigInt(Math.round(cents)) });
