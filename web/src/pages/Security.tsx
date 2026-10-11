import { useMemo, useState } from "react";
import { useSearchParams } from "react-router";
import { useMutation } from "@tanstack/react-query";
import { OctagonX, Shield, ShieldAlert } from "lucide-react";
import { api } from "../lib/api";
import { toast } from "../lib/toast";
import { useAgents } from "../lib/queries";
import { useRunningTasks } from "../lib/selectors";
import { useEvents } from "../lib/store";
import { PageHeader } from "../components/ui/PageHeader";
import { Card, CardHeader } from "../components/ui/Card";
import { Button } from "../components/ui/Button";
import { Badge } from "../components/ui/Badge";
import { Dialog } from "../components/ui/Dialog";
import { Input } from "../components/ui/Input";
import { DeviceList, PairDeviceButton } from "../components/Devices";
import { AgentIcon } from "../components/AgentIcon";
import { ApprovalState } from "../gen/nexus/v1/approval_pb";
import { TrustLevel } from "../gen/nexus/v1/common_pb";
import { actionClassLabel, formatDateTime, trustLabel, tsDate, tsMs } from "../lib/format";

const CLASSES = ["read", "write_internal", "compute", "write_external", "code_push", "spend", "destructive", "device_control"];

export default function Security() {
  const [params, setParams] = useSearchParams();
  const [openState, setOpenState] = useState(false);
  // ?kill=1 (from the command palette) opens the confirmation directly.
  const open = openState || params.get("kill") === "1";
  const setOpen = (v: boolean) => {
    setOpenState(v);
    if (!v && params.get("kill")) setParams({}, { replace: true });
  };
  const [pause, setPause] = useState(true);
  const [confirmText, setConfirmText] = useState("");
  const running = useRunningTasks();
  const approvals = useEvents((s) => s.approvals);
  const { data: agents } = useAgents();

  const stop = useMutation({
    mutationFn: async () => api().tasks.stopAll({ pauseTriggers: pause }),
    onSuccess: (res) => {
      toast.warn("Kill switch engaged", `${res.cancelled} task${res.cancelled === 1 ? "" : "s"} stopped${pause ? ", triggers paused" : ""}.`);
      setOpen(false);
      setConfirmText("");
    },
    onError: (e) => toast.error("Kill switch failed", e),
  });

  const audit = useMemo(
    () => Object.values(approvals).filter((a) => a.state !== ApprovalState.PENDING).sort((a, b) => tsMs(b.createdAt) - tsMs(a.createdAt)).slice(0, 12),
    [approvals],
  );

  return (
    <div className="space-y-5">
      <PageHeader icon={<Shield />} title="Security" subtitle="Devices, trust levels, decisions and the kill switch." />

      <Card className="border-danger/40">
        <div className="flex flex-wrap items-center gap-4 p-5">
          <div className="flex size-11 items-center justify-center rounded-xl bg-danger-soft text-danger">
            <OctagonX className="size-5" />
          </div>
          <div className="min-w-0 flex-1">
            <div className="text-[15px] font-semibold text-fg">Kill switch</div>
            <div className="text-[13px] text-muted">
              Stops every running agent on every machine and optionally pauses triggers. {running.length} task{running.length === 1 ? "" : "s"} active now.
            </div>
          </div>
          <Button variant="danger" icon={<OctagonX className="size-4" />} onClick={() => setOpen(true)}>
            Stop all agents
          </Button>
        </div>
      </Card>

      <div className="grid grid-cols-1 gap-5 xl:grid-cols-2">
        <Card>
          <CardHeader title="Devices" subtitle="Revoking removes the device's keys from the Hub and all nodes" actions={<PairDeviceButton />} />
          <DeviceList allowRevoke />
        </Card>

        <Card className="overflow-hidden">
          <CardHeader title="Recent decisions" subtitle="Approvals decided from any device" />
          {audit.length === 0 ? (
            <div className="p-6 text-center text-xs text-muted">No decisions yet.</div>
          ) : (
            <div className="divide-y divide-border">
              {audit.map((a) => (
                <div key={a.id} className="flex items-center gap-3 px-4 py-2.5 text-[13px]">
                  <Badge tone={a.state === ApprovalState.APPROVED ? "ok" : a.state === ApprovalState.DENIED ? "danger" : "neutral"}>{ApprovalState[a.state].toLowerCase()}</Badge>
                  <span className="min-w-0 flex-1 truncate text-fg">{a.title}</span>
                  {a.level === TrustLevel.BIOMETRIC && <Badge tone="accent">Face ID</Badge>}
                  <span className="hidden text-xs text-muted sm:inline">{formatDateTime(tsDate(a.createdAt))}</span>
                </div>
              ))}
            </div>
          )}
        </Card>
      </div>

      <Card className="overflow-hidden">
        <CardHeader icon={<ShieldAlert className="size-4" />} title="Trust levels" subtitle="Per-agent policy overrides (from each agent note). Blank = vault default policy." />
        <div className="overflow-x-auto">
          <table className="w-full text-[12.5px]">
            <thead>
              <tr className="border-b border-border text-left text-xs text-muted">
                <th className="px-4 py-2 font-medium">Agent</th>
                {CLASSES.map((c) => (
                  <th key={c} className="px-2 py-2 font-medium whitespace-nowrap">
                    {actionClassLabel[CLASSES.indexOf(c) + 1 as keyof typeof actionClassLabel]}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {(agents ?? []).map((a) => (
                <tr key={a.id} className="border-b border-border/60 last:border-0">
                  <td className="px-4 py-2">
                    <span className="flex items-center gap-2 font-medium whitespace-nowrap text-fg">
                      <AgentIcon agentId={a.id} icon={a.icon} size="sm" />
                      {a.name}
                    </span>
                  </td>
                  {CLASSES.map((c) => {
                    const lvl = a.policy[c];
                    return (
                      <td key={c} className="px-2 py-2">
                        {lvl ? <Badge tone={lvl >= TrustLevel.BIOMETRIC ? "danger" : lvl >= TrustLevel.APPROVE ? "warn" : "ok"}>{trustLabel[lvl]}</Badge> : <span className="text-muted">·</span>}
                      </td>
                    );
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>

      <Dialog
        open={open}
        onClose={() => setOpen(false)}
        title="Stop all agents?"
        description="Every running task is cancelled immediately. Nodes lock themselves until you unlock from your iPhone."
        footer={
          <>
            <Button onClick={() => setOpen(false)}>Cancel</Button>
            <Button variant="danger" icon={<OctagonX className="size-4" />} disabled={confirmText.trim().toUpperCase() !== "STOP"} loading={stop.isPending} onClick={() => stop.mutate()}>
              Stop everything
            </Button>
          </>
        }
      >
        <div className="space-y-3 text-[13px]">
          <label className="flex items-center gap-2 text-fg-2">
            <input type="checkbox" checked={pause} onChange={(e) => setPause(e.target.checked)} className="accent-[var(--danger)]" />
            Also pause triggers (cron, webhooks, geofences)
          </label>
          <div>
            <div className="mb-1.5 text-muted">
              Type <b className="text-fg">STOP</b> to confirm
            </div>
            <Input value={confirmText} onChange={(e) => setConfirmText(e.target.value)} placeholder="STOP" autoFocus />
          </div>
        </div>
      </Dialog>
    </div>
  );
}
