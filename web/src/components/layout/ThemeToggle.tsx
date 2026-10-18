import { Monitor, Moon, Sun } from "lucide-react";
import { useSettings, type ThemePref } from "../../lib/settings";
import { cx } from "../../lib/format";

const opts: Array<{ v: ThemePref; icon: typeof Sun; label: string }> = [
  { v: "light", icon: Sun, label: "Light" },
  { v: "system", icon: Monitor, label: "System" },
  { v: "dark", icon: Moon, label: "Dark" },
];

export function ThemeToggle() {
  const theme = useSettings((s) => s.theme);
  const update = useSettings((s) => s.update);
  return (
    <div className="flex items-center rounded-lg bg-surface-2 p-0.5" role="radiogroup" aria-label="Theme">
      {opts.map(({ v, icon: Icon, label }) => (
        <button
          key={v}
          role="radio"
          aria-checked={theme === v}
          title={label}
          aria-label={`${label} theme`}
          onClick={() => update({ theme: v })}
          className={cx("flex size-6 items-center justify-center rounded-md transition-colors", theme === v ? "bg-surface text-fg shadow-card" : "text-muted hover:text-fg")}
        >
          <Icon className="size-3.5" />
        </button>
      ))}
    </div>
  );
}
