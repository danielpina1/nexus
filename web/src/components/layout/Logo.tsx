import { cx } from "../../lib/format";
import { isMock } from "../../lib/api";

export function Logo({ size = "md", showDemo = true }: { size?: "md" | "lg"; showDemo?: boolean }) {
  const lg = size === "lg";
  return (
    <div className="flex items-center gap-2">
      <div className={cx("flex items-center justify-center rounded-lg bg-primary text-primary-fg", lg ? "size-9" : "size-7")}>
        <svg viewBox="0 0 32 32" className={lg ? "size-5" : "size-4"} fill="none" stroke="currentColor" strokeWidth="3.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
          <path d="M9 23V9l14 14V9" />
        </svg>
      </div>
      <span className={cx("font-semibold tracking-[-0.01em] text-fg", lg ? "text-[20px]" : "text-[15px]")}>Nexus</span>
      {showDemo && isMock() && <span className="rounded-md bg-warn-soft px-1.5 py-0.5 text-[10px] font-semibold tracking-wide text-warn uppercase">demo</span>}
    </div>
  );
}
