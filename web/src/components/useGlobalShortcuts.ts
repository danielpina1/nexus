import { useEffect } from "react";
import { useNavigate } from "react-router";
import { useUI } from "../lib/ui";
import { NAV } from "./layout/nav";

function typing(e: KeyboardEvent): boolean {
  const el = e.target as HTMLElement | null;
  if (!el) return false;
  return el.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(el.tagName);
}

/** Global keyboard shortcuts: ⌘K palette, g-sequences for navigation, ? for help. */
export function useGlobalShortcuts() {
  const navigate = useNavigate();
  useEffect(() => {
    let g = 0;
    const onKey = (e: KeyboardEvent) => {
      const ui = useUI.getState();
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        ui.set({ paletteOpen: !ui.paletteOpen });
        return;
      }
      if (typing(e) || e.metaKey || e.ctrlKey || e.altKey || ui.paletteOpen) return;
      if (e.key === "?") {
        ui.set({ shortcutsOpen: true });
        return;
      }
      if (e.key === "/") {
        const input = document.querySelector<HTMLInputElement>("[data-command-input]");
        if (input) {
          e.preventDefault();
          input.focus();
        }
        return;
      }
      if (e.key === "g") {
        g = Date.now();
        return;
      }
      if (Date.now() - g < 1200) {
        const item = NAV.find((n) => n.key === e.key);
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

