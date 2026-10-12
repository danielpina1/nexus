import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Monitor, Smartphone, Terminal, Trash2, Watch, Crown } from "lucide-react";
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

export { PairDeviceButton, PairMachineButton } from "./Pairing";
