import { useEffect } from "react";
import { useSettings } from "./settings";

function apply(pref: string) {
  const dark =
    pref === "dark" || (pref === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.classList.toggle("dark", dark);
  document.querySelectorAll('meta[name="theme-color"]').forEach((m) => m.setAttribute("content", dark ? "#0A0A0B" : "#F8FAFC"));
}

export function useThemeSync() {
  const theme = useSettings((s) => s.theme);
  useEffect(() => {
    apply(theme);
    if (theme !== "system") return;
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const on = () => apply("system");
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, [theme]);
}

export function useIsDark(): boolean {
  const theme = useSettings((s) => s.theme);
  if (theme === "system") return typeof window !== "undefined" && window.matchMedia("(prefers-color-scheme: dark)").matches;
  return theme === "dark";
}
