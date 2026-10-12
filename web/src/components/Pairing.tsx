import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useMutation } from "@tanstack/react-query";
import { Copy, Cpu, QrCode, RefreshCw, ShieldCheck } from "lucide-react";
import { encode } from "uqr";
import { PairingKind, type CreatePairingResponse } from "../gen/nexus/v1/device_pb";
import { api } from "../lib/api";
import { toast, errorMessage } from "../lib/toast";
import { tsMs } from "../lib/format";
import { Button } from "./ui/Button";
import { Dialog } from "./ui/Dialog";

/** A real, scannable QR code rendered as one SVG path (dark modules on white). */
export function QRCode({ value, className }: { value: string; className?: string }) {
  const { size, path } = useMemo(() => {
    const qr = encode(value, { ecc: "L", border: 2 });
    let d = "";
    qr.data.forEach((row, y) =>
      row.forEach((on, x) => {
        if (on) d += `M${x} ${y}h1v1h-1z`;
      }),
    );
    return { size: qr.size, path: d };
  }, [value]);
  return (
    <svg viewBox={`0 0 ${size} ${size}`} className={className} shapeRendering="crispEdges" role="img" aria-label="Pairing QR code">
      <rect width={size} height={size} fill="#ffffff" />
      <path d={path} fill="#000000" />
    </svg>
  );
}

/** Seconds left until `ms`, re-rendering every second while open. */
function useSecondsLeft(ms: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!ms) return;
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, [ms]);
  return ms ? Math.max(0, Math.round((ms - now) / 1000)) : 0;
}

function mmss(s: number): string {
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

/** Shell-quote for the copyable `nexus-node pair '<url>'` command. */
function shellQuote(s: string): string {
  return `'${s.replace(/'/g, `'\\''`)}'`;
}

function copy(text: string, what: string) {
  if (!navigator.clipboard) {
    toast.error("Clipboard unavailable", "Select the text and copy it manually.");
    return;
  }
  navigator.clipboard.writeText(text).then(
    () => toast.success(`${what} copied`),
    (e) => toast.error("Copy failed", e),
  );
}

function PairingBody({ kind, pairing, onRenew, renewing }: { kind: PairingKind; pairing: CreatePairingResponse; onRenew: () => void; renewing: boolean }) {
  const left = useSecondsLeft(tsMs(pairing.expiresAt));
  const expired = left === 0;
  const command = `nexus-node pair ${shellQuote(pairing.url)}`;
  const urls = pairing.hubUrls.join(", ");
  return (
    <div className="flex flex-col items-center gap-3">
      {kind === PairingKind.DEVICE ? (
        <>
          <div className={expired ? "opacity-25 blur-[2px] transition" : "transition"}>
            <QRCode value={pairing.url} className="size-56 rounded-lg" />
          </div>
          <p className="text-center text-[13px] text-fg-2">
            Scan with the Nexus iPhone app on your home Wi-Fi.
          </p>
        </>
      ) : (
        <div className="w-full space-y-2">
          <p className="text-[13px] text-fg-2">Run this on the machine (it needs to reach {urls || "the hub"}):</p>
          <div className="flex items-start gap-2 rounded-lg border border-border bg-surface-2 p-2.5">
            <code className={`min-w-0 flex-1 font-mono text-[11px] break-all select-all ${expired ? "text-muted line-through" : "text-fg"}`}>{command}</code>
            <Button size="xs" variant="ghost" icon={<Copy className="size-3.5" />} disabled={expired} onClick={() => copy(command, "Command")}>
              Copy
            </Button>
          </div>
          <p className="text-xs text-muted">
            The node gets a token and a certificate from the Hub CA, then connects with <code className="font-mono">nexus-node run</code>.
          </p>
        </div>
      )}
      <div className="flex w-full items-center justify-between gap-2 text-xs">
        <span className={expired ? "font-medium text-danger" : "text-muted"} aria-live="polite">
          {expired ? "Expired — create a new code" : `Expires in ${mmss(left)} · single use`}
        </span>
        <Button size="xs" icon={<RefreshCw className="size-3.5" />} loading={renewing} onClick={onRenew}>
          New code
        </Button>
      </div>
      <div className="flex w-full items-start gap-2 rounded-lg bg-accent-soft px-3 py-2 text-xs text-fg-2">
        <ShieldCheck className="mt-0.5 size-3.5 shrink-0 text-accent" />
        <span>
          After pairing, both sides show a 6-emoji safety code ({kind === PairingKind.DEVICE ? "the app and the Hub" : "nexus-node and the Hub log"}). If they differ, revoke it. Hub CA{" "}
          <code className="font-mono" title={pairing.caFingerprint}>
            {pairing.caFingerprint.slice(0, 16)}…
          </code>
        </span>
      </div>
    </div>
  );
}

function PairButton({ kind, label, icon, variant }: { kind: PairingKind; label: string; icon: ReactNode; variant?: "primary" }) {
  const [open, setOpen] = useState(false);
  const create = useMutation({
    mutationFn: async () => api().devices.createPairing({ kind, label: kind === PairingKind.DEVICE ? "dashboard device" : "dashboard machine" }),
    onError: (e) => toast.error("Could not create a pairing code", e),
  });
  const start = () => {
    setOpen(true);
    create.reset();
    create.mutate();
  };
  const title = kind === PairingKind.DEVICE ? "Pair a device" : "Pair a machine";
  const description =
    kind === PairingKind.DEVICE ? "An iPhone, iPad or Watch gets its own keys and a certificate from your Hub." : "Connect a computer running nexus-node to this Hub.";
  return (
    <>
      <Button variant={variant} icon={icon} onClick={start}>
        {label}
      </Button>
      <Dialog open={open} onClose={() => setOpen(false)} title={title} description={description}>
        {create.data ? (
          <PairingBody kind={kind} pairing={create.data} onRenew={() => create.mutate()} renewing={create.isPending} />
        ) : create.isError ? (
          <div className="space-y-3">
            <div className="rounded-lg bg-danger-soft px-3 py-2 text-xs break-words text-danger">{errorMessage(create.error)}</div>
            <p className="text-xs text-muted">
              Pairing needs <code className="font-mono">security.hub_urls</code> in nexus.yaml (<code className="font-mono">nexusd init -hub-url …</code>). You can also run{" "}
              <code className="font-mono">nexusd pair</code> on the Hub.
            </p>
            <Button size="xs" icon={<RefreshCw className="size-3.5" />} onClick={() => create.mutate()}>
              Retry
            </Button>
          </div>
        ) : (
          <div className="flex h-56 items-center justify-center text-[13px] text-muted">Creating a one-time code…</div>
        )}
      </Dialog>
    </>
  );
}

export function PairDeviceButton() {
  return <PairButton kind={PairingKind.DEVICE} label="Pair a device" icon={<QrCode className="size-3.5" />} variant="primary" />;
}

export function PairMachineButton() {
  return <PairButton kind={PairingKind.NODE} label="Pair a machine" icon={<Cpu className="size-3.5" />} />;
}
