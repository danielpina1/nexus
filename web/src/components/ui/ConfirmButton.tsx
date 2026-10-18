import { useEffect, useState, type ReactNode } from "react";
import { Button, type ButtonProps } from "./Button";

/**
 * Destructive action with a second, explicit confirmation (MASTER §9): the first press turns the
 * button into "Confirm …" (danger) for 4 s; only the second press acts.
 */
export function ConfirmButton({ label, confirmLabel, onConfirm, icon, ...rest }: Omit<ButtonProps, "onClick"> & { label: ReactNode; confirmLabel: ReactNode; onConfirm: () => void }) {
  const [armed, setArmed] = useState(false);
  useEffect(() => {
    if (!armed) return;
    const t = setTimeout(() => setArmed(false), 4000);
    return () => clearTimeout(t);
  }, [armed]);
  return (
    <Button
      {...rest}
      icon={icon}
      variant={armed ? "danger" : (rest.variant ?? "secondary")}
      aria-live="polite"
      onClick={() => {
        if (armed) {
          setArmed(false);
          onConfirm();
        } else setArmed(true);
      }}
    >
      {armed ? confirmLabel : label}
    </Button>
  );
}
