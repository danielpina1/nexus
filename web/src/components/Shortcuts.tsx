import { useEffect } from "react";
import { useNavigate } from "react-router";
import { useUI } from "../lib/ui";
import { NAV } from "./layout/nav";
import { Dialog } from "./ui/Dialog";
import { Kbd } from "./ui/Input";

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

export function ShortcutsDialog() {
  const open = useUI((s) => s.shortcutsOpen);
  const set = useUI((s) => s.set);
  const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform);
  const rows: Array<[React.ReactNode, string]> = [
    [<><Kbd>{isMac ? "⌘" : "Ctrl"}</Kbd><Kbd>K</Kbd></>, "Command palette"],
    [<Kbd>/</Kbd>, "Focus the command bar"],
    [<Kbd>?</Kbd>, "This help"],
    ...NAV.map((n) => [<><Kbd>g</Kbd><Kbd>{n.key}</Kbd></>, `Go to ${n.label}`] as [React.ReactNode, string]),
  ];
  return (
    <Dialog open={open} onClose={() => set({ shortcutsOpen: false })} title="Keyboard shortcuts">
      <div className="grid gap-1.5">
        {rows.map(([k, label], i) => (
          <div key={i} className="flex items-center justify-between text-[13px]">
            <span className="text-fg-2">{label}</span>
            <span className="flex gap-1">{k}</span>
          </div>
        ))}
      </div>
    </Dialog>
  );
}
