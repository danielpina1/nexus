import { create } from "zustand";

interface UIState {
  paletteOpen: boolean;
  shortcutsOpen: boolean;
  navOpen: boolean;
  set: (p: Partial<Omit<UIState, "set">>) => void;
}

export const useUI = create<UIState>((set) => ({
  paletteOpen: false,
  shortcutsOpen: false,
  navOpen: false,
  set: (p) => set(p),
}));
