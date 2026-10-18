import { useEffect } from "react";
import { useNavigate } from "react-router";
import { useUI } from "../lib/ui";
import { SECTIONS, SUB_KEYS } from "../app/registry";

function typing(e: KeyboardEvent): boolean {
  const el = e.target as HTMLElement | null;
  if (!el) return false;
  return el.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(el.tagName);
}

/** All `g <key>` jumps: sections first, then sub-pages. */
export const JUMPS: Array<{ key: string; to: string; label: string }> = [
  ...SECTIONS.map((s) => ({ key: s.key, to: s.to, label: s.label })),
  ...SUB_KEYS.filter((k) => !SECTIONS.some((s) => s.key === k.key)),
];

/** Global keyboard shortcuts: ⌘K palette, g-sequences, / command bar, ? help, ` perf HUD. */
export function useGlobalShortcuts() {
  const navigate = useNavigate();
  useEffect(() => {
    let g = 0;
    const onKey = (e: KeyboardEvent) => {
      const ui = useUI.getState();
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        if (ui.paletteOpen) ui.set({ paletteOpen: false });
        else ui.openPalette();
        return;
      }
      if (typing(e) || e.metaKey || e.ctrlKey || e.altKey || ui.paletteOpen) return;
      if (e.key === "?") {
        ui.set({ shortcutsOpen: true });
        return;
      }
      if (e.key === "`") {
        ui.togglePerf();
        return;
      }
      if (e.key === "/") {
        const input = document.querySelector<HTMLInputElement | HTMLTextAreaElement>("[data-command-input]");
        e.preventDefault();
        if (input) input.focus();
        else ui.openPalette();
        return;
      }
      if (e.key === "g") {
        g = Date.now();
        return;
      }
      if (Date.now() - g < 1200) {
        const item = JUMPS.find((n) => n.key === e.key);
        g = 0;
        if (item) {
          e.preventDefault();
          navigate(item.to);
        }
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [navigate]);
}
