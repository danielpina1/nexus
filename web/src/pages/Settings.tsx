import { useState } from "react";
import { CheckCircle2, Eye, EyeOff, FlaskConical, Monitor, Moon, PlugZap, RefreshCw, Settings as SettingsIcon, Sun, XCircle } from "lucide-react";
import { useSettings, mockForcedByEnv, type ThemePref } from "../lib/settings";
import { api, hubBaseUrl, isMock, reconfigureApi } from "../lib/api";
import { resetEventCache, startEventStream } from "../lib/events";
import { queryClient } from "../lib/queries";
import { toast, errorMessage } from "../lib/toast";
import { PageHeader } from "../components/ui/PageHeader";
import { Card, CardHeader } from "../components/ui/Card";
import { Button } from "../components/ui/Button";
import { Input, Label, Switch } from "../components/ui/Input";
import { DeviceList, PairDeviceButton } from "../components/Devices";
import { cx } from "../lib/format";

export default function Settings() {
  const s = useSettings();
  const [hubUrl, setHubUrl] = useState(s.hubUrl);
  const [token, setToken] = useState(s.token);
  const [deviceId, setDeviceId] = useState(s.deviceId);
  const [show, setShow] = useState(false);
  const [test, setTest] = useState<{ ok: boolean; msg: string } | null>(null);
  const [testing, setTesting] = useState(false);
  const dirty = hubUrl !== s.hubUrl || token !== s.token || deviceId !== s.deviceId;

  const save = () => {
    s.update({ hubUrl: hubUrl.trim(), token: token.trim(), deviceId: deviceId.trim() || "web" });
    reconfigureApi();
    void queryClient.invalidateQueries();
    startEventStream();
    toast.success("Connection settings saved", isMock() ? "Mock mode is on — the real Hub is not used." : `Reconnecting to ${hubBaseUrl()}`);
  };

  const runTest = async () => {
    setTesting(true);
    setTest(null);
    try {
      const t0 = performance.now();
      const res = await api().nodes.listNodes({});
      setTest({ ok: true, msg: `OK · ${res.nodes.length} nodes · ${Math.round(performance.now() - t0)} ms` });
    } catch (e) {
      setTest({ ok: false, msg: errorMessage(e) });
    } finally {
      setTesting(false);
    }
  };

  return (
    <div className="space-y-5">
      <PageHeader icon={<SettingsIcon />} title="Settings" />

      <Card>
        <CardHeader icon={<PlugZap className="size-4" />} title="Hub connection" subtitle="The dashboard only ever talks to your Nexus Hub (Connect RPC under /nexus.v1.*)." />
        <div className="grid grid-cols-1 gap-4 p-4 md:grid-cols-2">
          <div>
            <Label hint="Leave empty when nexusd serves this dashboard (same origin).">Hub URL</Label>
            <Input value={hubUrl} onChange={(e) => setHubUrl(e.target.value)} placeholder={window.location.origin} />
          </div>
          <div>
            <Label hint="Sent as Authorization: Bearer … (dev: security.dev_token in nexus.yaml).">Access token</Label>
            <div className="relative">
              <Input type={show ? "text" : "password"} value={token} onChange={(e) => setToken(e.target.value)} placeholder="nx_…" className="pr-9 font-mono" autoComplete="off" />
              <button onClick={() => setShow((v) => !v)} className="absolute top-1/2 right-2.5 -translate-y-1/2 text-muted hover:text-fg" aria-label={show ? "Hide token" : "Show token"}>
                {show ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
              </button>
            </div>
          </div>
          <div>
            <Label hint="Identifies this browser when deciding approvals.">Device ID</Label>
            <Input value={deviceId} onChange={(e) => setDeviceId(e.target.value)} className="font-mono" />
          </div>
          <div className="flex items-end gap-2">
            <Button variant="primary" disabled={!dirty} onClick={save}>
              Save &amp; reconnect
            </Button>
            <Button loading={testing} onClick={runTest}>
              Test connection
            </Button>
          </div>
          {test && (
            <div className={cx("flex items-center gap-2 rounded-lg px-3 py-2 text-[13px] md:col-span-2", test.ok ? "bg-ok-soft text-ok" : "bg-danger-soft text-danger")}>
              {test.ok ? <CheckCircle2 className="size-4" /> : <XCircle className="size-4" />}
              <span className="break-all">{test.msg}</span>
            </div>
          )}
        </div>
      </Card>

      <Card>
        <CardHeader icon={<FlaskConical className="size-4" />} title="Mock mode" subtitle="Run the dashboard against an in-memory Hub with realistic fixtures and a live simulation." />
        <div className="flex items-center gap-3 p-4">
          <Switch
            checked={s.mock}
            disabled={mockForcedByEnv}
            label="Mock mode"
            onChange={(v) => {
              s.update({ mock: v });
              toast.info(v ? "Mock mode on" : "Mock mode off", "Reloading…");
              setTimeout(() => location.reload(), 400);
            }}
          />
          <div className="text-[13px] text-fg-2">
            {mockForcedByEnv ? "Forced on by VITE_MOCK=1 in this build." : s.mock ? "Using the mock Hub. Toggle off to connect to your real Hub." : "Connected to the real Hub."}
          </div>
        </div>
      </Card>

      <Card>
        <CardHeader title="Appearance" />
        <div className="flex flex-wrap gap-2 p-4">
          {(
            [
              ["system", Monitor, "System"],
              ["light", Sun, "Light"],
              ["dark", Moon, "Dark"],
            ] as Array<[ThemePref, typeof Sun, string]>
          ).map(([v, Icon, label]) => (
            <button
              key={v}
              onClick={() => s.update({ theme: v })}
              className={cx("flex items-center gap-2 rounded-lg border px-3.5 py-2 text-[13px] transition-colors", s.theme === v ? "border-accent bg-accent-soft text-fg" : "border-border text-fg-2 hover:bg-surface-2")}
            >
              <Icon className="size-4" />
              {label}
            </button>
          ))}
        </div>
      </Card>

      <Card>
        <CardHeader title="Devices" subtitle="Phones, watches, browsers and CLIs paired with this Hub" actions={<PairDeviceButton />} />
        <DeviceList />
      </Card>

      <Card>
        <CardHeader title="Local data" subtitle="The event stream resumes from the last seq stored in this browser." />
        <div className="flex flex-wrap items-center gap-2 p-4">
          <Button
            icon={<RefreshCw className="size-3.5" />}
            onClick={() => {
              resetEventCache();
              toast.info("Event cache cleared", "Replaying the event log from the start.");
            }}
          >
            Reset event cache
          </Button>
        </div>
      </Card>
    </div>
  );
}
