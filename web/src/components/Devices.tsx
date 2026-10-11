import { useMemo, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Monitor, QrCode, Smartphone, Terminal, Trash2, Watch, Crown } from "lucide-react";
import { DeviceKind, type Device } from "../gen/nexus/v1/device_pb";
import { api } from "../lib/api";
import { qk, useDevices } from "../lib/queries";
import { toast } from "../lib/toast";
import { deviceKindLabel, timeAgo, tsMs } from "../lib/format";
import { useSettings } from "../lib/settings";
import { Button } from "./ui/Button";
import { Badge } from "./ui/Badge";
import { Dialog } from "./ui/Dialog";
import { SkeletonRows } from "./ui/Skeleton";
import { ErrorState } from "./ui/ErrorState";

const ICON: Record<DeviceKind, typeof Smartphone> = {
  [DeviceKind.UNSPECIFIED]: Monitor,
  [DeviceKind.IOS]: Smartphone,
  [DeviceKind.WATCH]: Watch,
  [DeviceKind.WEB]: Monitor,
  [DeviceKind.CLI]: Terminal,
};

function fingerprint(key: Uint8Array): string {
  if (!key.length) return "—";
  return Array.from(key.slice(1, 9), (b) => b.toString(16).padStart(2, "0")).join(":");
}

export function DeviceList({ allowRevoke }: { allowRevoke?: boolean }) {
  const { data, isLoading, isError, error, refetch } = useDevices();
  const qc = useQueryClient();
  const myId = useSettings((s) => s.deviceId);
  const [confirm, setConfirm] = useState<Device | null>(null);
  const revoke = useMutation({
    mutationFn: async (id: string) => api().devices.revokeDevice({ id }),
    onSuccess: () => {
      toast.success("Device revoked", confirm?.name);
      setConfirm(null);
      void qc.invalidateQueries({ queryKey: qk.devices });
    },
    onError: (e) => toast.error("Revoke failed", e),
  });
  if (isLoading) return <SkeletonRows rows={3} />;
  if (isError) return <ErrorState compact error={error} onRetry={() => refetch()} />;
  return (
    <>
      <div className="divide-y divide-border">
        {(data ?? []).map((d) => {
          const Icon = ICON[d.kind];
          return (
            <div key={d.id} className="flex items-center gap-3 px-4 py-3">
              <div className="flex size-8 items-center justify-center rounded-lg bg-surface-2 text-fg-2">
                <Icon className="size-4" />
              </div>
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-1.5 text-[13px] font-medium text-fg">
                  {d.name}
                  {d.primary && (
                    <Badge tone="accent">
                      <Crown className="size-3" />
                      owner key
                    </Badge>
                  )}
                  {d.id === myId && <Badge tone="info">this browser</Badge>}
                  {d.approvalPublicKey.length > 0 && <Badge tone="ok">Face ID approvals</Badge>}
                </div>
                <div className="truncate text-xs text-muted">
                  {deviceKindLabel[d.kind]} · key {fingerprint(d.publicKey)} · seen {timeAgo(tsMs(d.lastSeen))}
                </div>
              </div>
              {allowRevoke && !d.primary && d.id !== myId && (
                <Button size="xs" variant="ghost" className="text-danger hover:text-danger" icon={<Trash2 className="size-3.5" />} onClick={() => setConfirm(d)}>
                  Revoke
                </Button>
              )}
            </div>
          );
        })}
      </div>
      <Dialog
        open={!!confirm}
        onClose={() => setConfirm(null)}
        title={`Revoke ${confirm?.name}?`}
        description="Its keys are removed from the Hub and every node. It will have to be paired again."
        footer={
          <>
            <Button onClick={() => setConfirm(null)}>Cancel</Button>
            <Button variant="danger" loading={revoke.isPending} onClick={() => confirm && revoke.mutate(confirm.id)}>
              Revoke device
            </Button>
          </>
        }
      />
    </>
  );
}

/** Deterministic QR-looking placeholder; real pairing tokens come from `nexusd pair`. */
function FakeQR({ seed }: { seed: string }) {
  const cells = useMemo(() => {
    const n = 25;
    let h = 2166136261;
    for (const c of seed) h = Math.imul(h ^ c.charCodeAt(0), 16777619);
    const out: boolean[] = [];
    for (let i = 0; i < n * n; i++) {
      h = Math.imul(h ^ (h >>> 13), 1274126177);
      out.push(((h >>> 7) & 1) === 1);
    }
    return { n, out };
  }, [seed]);
  const { n, out } = cells;
  const finder = (x: number, y: number) => {
    for (const [fx, fy] of [[0, 0], [n - 7, 0], [0, n - 7]]) {
      if (x >= fx && x < fx + 7 && y >= fy && y < fy + 7) {
        const dx = x - fx;
        const dy = y - fy;
        return dx === 0 || dy === 0 || dx === 6 || dy === 6 || (dx >= 2 && dx <= 4 && dy >= 2 && dy <= 4) ? 1 : 0;
      }
    }
    return -1;
  };
  return (
    <svg viewBox={`-2 -2 ${n + 4} ${n + 4}`} className="size-48 rounded-lg bg-white p-1" shapeRendering="crispEdges">
      {out.map((on, i) => {
        const x = i % n;
        const y = Math.floor(i / n);
        const f = finder(x, y);
        const fill = f === -1 ? on : f === 1;
        return fill ? <rect key={i} x={x} y={y} width={1} height={1} fill="#0b0d12" /> : null;
      })}
    </svg>
  );
}

export function PairDeviceButton() {
  const [open, setOpen] = useState(false);
  const [seed, setSeed] = useState("");
  return (
    <>
      <Button
        variant="primary"
        icon={<QrCode className="size-3.5" />}
        onClick={() => {
          setSeed(Math.random().toString(36).slice(2));
          setOpen(true);
        }}
      >
        Pair a device
      </Button>
      <Dialog open={open} onClose={() => setOpen(false)} title="Pair a new device" description="Scan with the Nexus iOS app (Settings → Pair with Hub).">
        <div className="flex flex-col items-center gap-3">
          <FakeQR seed={seed} />
          <div className="rounded-lg bg-warn-soft px-3 py-2 text-xs text-warn">
            Placeholder QR. Pairing tokens are minted on the Hub — run <code className="font-mono">nexusd pair</code> to get a real one. Both screens will show the same emoji safety code.
          </div>
        </div>
      </Dialog>
    </>
  );
}
