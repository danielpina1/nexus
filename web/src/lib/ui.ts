import { create } from "zustand";
import { readJSON, writeJSON } from "./storage";

interface UIState {
  paletteOpen: boolean;
  /** Text to prefill the palette with when it opens. */
  paletteQuery: string;
  shortcutsOpen: boolean;
  /** Mobile "More" sheet. */
  navOpen: boolean;
  /** Dev perf HUD (toggle with `). */
  perfOpen: boolean;
  set: (p: Partial<Omit<UIState, "set" | "togglePerf" | "openPalette">>) => void;
  togglePerf: () => void;
  openPalette: (query?: string) => void;
}

export const useUI = create<UIState>((set, get) => ({
  paletteOpen: false,
  paletteQuery: "",
  shortcutsOpen: false,
  navOpen: false,
  perfOpen: readJSON<boolean>("nexus.perfHud", false),
  set: (p) => set(p),
  togglePerf: () => {
    const v = !get().perfOpen;
    writeJSON("nexus.perfHud", v);
    set({ perfOpen: v });
  },
  openPalette: (query = "") => set({ paletteOpen: true, paletteQuery: query, navOpen: false }),
}));
