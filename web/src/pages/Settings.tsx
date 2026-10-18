import { useState } from "react";
import { Activity, CheckCircle2, Eye, EyeOff, FlaskConical, LogOut, Monitor, Moon, RefreshCw, Sun, XCircle } from "lucide-react";
import { useSettings, mockForcedByEnv, type ThemePref } from "../lib/settings";
import { api, hubBaseUrl, isMock, reconfigureApi } from "../lib/api";
import { resetEventCache, startEventStream, stopEventStream } from "../lib/events";
import { queryClient } from "../lib/queries";
import { toast, errorMessage } from "../lib/toast";
import { useUI } from "../lib/ui";
import { useEvents } from "../lib/store";
import { PageHeader } from "../components/ui/PageHeader";
import { Card, CardHeader } from "../components/ui/Card";
import { Button } from "../components/ui/Button";
import { Input, Switch } from "../components/ui/Input";
import { DeviceList } from "../components/Devices";
import { PairDeviceButton, PairMachineButton } from "../components/Pairing";
import { cx } from "../lib/format";

function Field({ id, label, hint, children }: { id: string; label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div>
      <label htmlFor={id} className="mb-1.5 block text-[13px] font-medium text-fg">
        {label}
      </label>
      {children}
      {hint && <p className="mt-1 text-xs text-muted">{hint}</p>}
    </div>
  );
}

function Row({ title, body, children }: { title: string; body?: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="flex flex-wrap items-center gap-x-6 gap-y-3 px-4 py-4">
      <div className="min-w-0 flex-1 basis-64">
        <div className="text-[13.5px] font-medium text-fg">{title}</div>
        {body && <div className="mt-0.5 text-[13px] text-muted">{body}</div>}
      </div>
      <div className="flex shrink-0 flex-wrap items-center gap-2">{children}</div>
    </div>
  );
}

export default function Settings() {
  const s = useSettings();
  const perfOpen = useUI((u) => u.perfOpen);
  const lastSeq = useEvents((e) => e.lastSeq);
  const [hubUrl, setHubUrl] = useState(s.hubUrl);
  const [token, setToken] = useState(s.token);
  const [deviceId, setDeviceId] = useState(s.deviceId);
  const [show, setShow] = useState(false);
  const [test, setTest] = useState<{ ok: boolean; msg: string } | null>(null);
  const [testing, setTesting] = useState(false);
  const dirty = hubUrl !== s.hubUrl || token !== s.token || deviceId !== s.deviceId;
  const mock = isMock();

  const save = () => {
    s.update({ hubUrl: hubUrl.trim(), token: token.trim(), deviceId: deviceId.trim() || "web" });
    reconfigureApi();
    void queryClient.invalidateQueries();
    startEventStream();
    toast.success("Saved", mock ? "Demo mode is on; the real Hub is not used." : `Reconnecting to ${hubBaseUrl()}`);
  };

  const runTest = async () => {
    setTesting(true);
    setTest(null);
    try {
      const t0 = performance.now();
      const res = await api().nodes.listNodes({});
      setTest({ ok: true, msg: `Connected · ${res.nodes.length} machine${res.nodes.length === 1 ? "" : "s"} · ${Math.round(performance.now() - t0)} ms` });
    } catch (e) {
      setTest({ ok: false, msg: errorMessage(e) });
    } finally {
      setTesting(false);
    }
  };

  const themes: Array<[ThemePref, typeof Sun, string]> = [
    ["dark", Moon, "Dark"],
    ["light", Sun, "Light"],
    ["system", Monitor, "System"],
  ];

  return (
    <div className="space-y-6">
      <PageHeader title="Settings" subtitle="Connection, appearance and the devices paired with your Hub." />

      <Card>
        <CardHeader title="Hub connection" subtitle="This dashboard talks only to your Nexus Hub." />
        <form
          className="grid grid-cols-1 gap-4 p-4 md:grid-cols-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (dirty) save();
          }}
        >
          <Field id="set-hub" label="Hub URL" hint="Empty = this page's origin (the Hub serves the dashboard).">
            <Input id="set-hub" value={hubUrl} onChange={(e) => setHubUrl(e.target.value)} placeholder={window.location.origin} disabled={mock} spellCheck={false} inputMode="url" />
          </Field>
          <Field id="set-token" label="Access token" hint="Sent as a bearer token. Dev: security.dev_token in nexus.yaml.">
            <div className="relative">
              <Input
                id="set-token"
                type={show ? "text" : "password"}
                value={token}
                onChange={(e) => setToken(e.target.value)}
                placeholder="Paste your token"
                className="pr-11 font-mono"
                autoComplete="current-password"
                disabled={mock}
              />
              <button type="button" onClick={() => setShow((v) => !v)} className="absolute inset-y-0 right-0 flex w-11 items-center justify-center text-muted hover:text-fg" aria-label={show ? "Hide token" : "Show token"}>
                {show ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
              </button>
            </div>
          </Field>
          <Field id="set-device" label="Device ID" hint="Identifies this browser when you decide approvals.">
            <Input id="set-device" value={deviceId} onChange={(e) => setDeviceId(e.target.value)} className="font-mono" spellCheck={false} />
          </Field>
          <div className="flex flex-wrap items-end gap-2">
            <Button type="submit" variant="primary" size="md" disabled={!dirty}>
              Save and reconnect
            </Button>
            <Button type="button" size="md" loading={testing} onClick={runTest}>
              Test connection
            </Button>
            {!mock && (
              <Button
                type="button"
                size="md"
                variant="ghost"
                icon={<LogOut className="size-3.5" />}
                onClick={() => {
                  stopEventStream();
                  s.update({ token: "" });
                }}
              >
                Sign out
              </Button>
            )}
          </div>
          {test && (
            <div role="status" className={cx("flex items-center gap-2 rounded-lg px-3 py-2 text-[13px] md:col-span-2", test.ok ? "bg-ok-soft text-ok" : "bg-danger-soft text-danger")}>
              {test.ok ? <CheckCircle2 className="size-4 shrink-0" aria-hidden /> : <XCircle className="size-4 shrink-0" aria-hidden />}
              <span className="break-all">{test.msg}</span>
            </div>
          )}
        </form>
      </Card>

      <Card className="divide-y divide-border">
        <Row title="Appearance" body="Dark is the default for an agent console.">
          <div role="radiogroup" aria-label="Theme" className="flex rounded-lg border border-border bg-surface-2 p-0.5">
            {themes.map(([v, Icon, label]) => (
              <button
                key={v}
                role="radio"
                aria-checked={s.theme === v}
                onClick={() => s.update({ theme: v })}
                className={cx(
                  "flex h-8 items-center gap-1.5 rounded-md px-3 text-[13px] font-medium transition-colors duration-150 pointer-coarse:h-10",
                  s.theme === v ? "bg-surface text-fg shadow-card" : "text-muted hover:text-fg",
                )}
              >
                <Icon className="size-3.5" aria-hidden />
                {label}
              </button>
            ))}
          </div>
        </Row>
        <Row
          title="Demo mode"
          body={mockForcedByEnv ? "Forced on by VITE_MOCK=1 in this build." : "An in-browser Hub with sample agents, tasks and notes. Reloads the page."}
        >
          <FlaskConical className="size-4 text-muted" aria-hidden />
          <Switch
            checked={s.mock}
            disabled={mockForcedByEnv}
            label="Demo mode"
            onChange={(v) => {
              s.update({ mock: v });
              setTimeout(() => location.reload(), 150);
            }}
          />
        </Row>
        <Row title="Performance HUD" body={<>Frame rate, events per second and store timings. Shortcut: <kbd className="font-mono">`</kbd></>}>
          <Activity className="size-4 text-muted" aria-hidden />
          <Switch checked={perfOpen} label="Performance HUD" onChange={() => useUI.getState().togglePerf()} />
        </Row>
        <Row title="Event cache" body={<>The live stream resumes from seq <span className="font-mono tabular">{lastSeq.toString()}</span> stored in this browser.</>}>
          <Button
            icon={<RefreshCw className="size-3.5" />}
            onClick={() => {
              resetEventCache();
              toast.info("Replaying the event log from the start");
            }}
          >
            Reset and resync
          </Button>
        </Row>
      </Card>

      <Card>
        <CardHeader
          title="Devices"
          subtitle={mock ? "Sample devices in demo mode" : "Phones, watches, browsers and CLIs paired with this Hub"}
          actions={
            <div className="flex flex-wrap gap-2">
              {!mock && <PairMachineButton />}
              <PairDeviceButton />
            </div>
          }
        />
        <DeviceList />
      </Card>
    </div>
  );
}
