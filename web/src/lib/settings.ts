import { create } from "zustand";
import { readJSON, writeJSON } from "./storage";

export type ThemePref = "system" | "light" | "dark";

export interface Settings {
  /** Hub base URL. Empty = same origin (nexusd serves the dashboard). */
  hubUrl: string;
  /** Bearer token sent on every request. */
  token: string;
  /** Device id used when deciding approvals. */
  deviceId: string;
  /** Use the in-memory mock hub. */
  mock: boolean;
  theme: ThemePref;
}

const KEY = "nexus.settings";

const envMock = import.meta.env.VITE_MOCK === "1" || import.meta.env.VITE_MOCK === "true";

const defaults: Settings = {
  hubUrl: import.meta.env.VITE_HUB_URL ?? "",
  token: "",
  deviceId: "web",
  mock: envMock,
  theme: "dark",
};

function load(): Settings {
  const stored = readJSON<Partial<Settings>>(KEY, {});
  const s = { ...defaults, ...stored };
  // VITE_MOCK=1 always wins: a mock build must be demoable without configuration.
  if (envMock) s.mock = true;
  return s;
}

interface SettingsState extends Settings {
  update: (patch: Partial<Settings>) => void;
}

export const useSettings = create<SettingsState>((set, get) => ({
  ...load(),
  update: (patch) => {
    set(patch);
    const { hubUrl, token, deviceId, mock, theme } = get();
    writeJSON(KEY, { hubUrl, token, deviceId, mock, theme });
  },
}));

export const mockForcedByEnv = envMock;

export function getSettings(): Settings {
  return useSettings.getState();
}
