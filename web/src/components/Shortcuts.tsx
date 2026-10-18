import { useUI } from "../lib/ui";
import { JUMPS } from "./useGlobalShortcuts";
import { isMac } from "./layout/Sidebar";
import { Dialog } from "./ui/Dialog";
import { Kbd } from "./ui/Input";

export function ShortcutsDialog() {
  const open = useUI((s) => s.shortcutsOpen);
  const set = useUI((s) => s.set);
  const rows: Array<[React.ReactNode, string]> = [
    [<><Kbd>{isMac ? "⌘" : "Ctrl"}</Kbd><Kbd>K</Kbd></>, "Command palette: run, search, jump"],
    [<Kbd>/</Kbd>, "Focus the command bar"],
    [<Kbd>?</Kbd>, "This help"],
    [<Kbd>`</Kbd>, "Performance HUD"],
    ...JUMPS.map((n) => [<><Kbd>g</Kbd><Kbd>{n.key}</Kbd></>, `Go to ${n.label}`] as [React.ReactNode, string]),
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
