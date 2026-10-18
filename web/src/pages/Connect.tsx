import { useState } from "react";
import { ArrowRight, Eye, EyeOff, FlaskConical, Loader2 } from "lucide-react";
import { useSettings } from "../lib/settings";
import { api, hubBaseUrl, reconfigureApi } from "../lib/api";
import { startEventStream } from "../lib/events";
import { queryClient } from "../lib/queryClient";
import { useEvents } from "../lib/store";
import { errorMessage } from "../lib/toast";
import { Input } from "../components/ui/Input";
import { Logo } from "../components/layout/Logo";

/**
 * First-run / signed-out screen: hub URL (defaults to this origin), access token, or demo mode.
 * Shown by the Gate in App.tsx whenever there is no token or the Hub rejects it.
 */
export default function Connect() {
  const s = useSettings();
  const rejected = useEvents((st) => (st.connection.status === "unauthorized" ? st.connection.error : undefined));
  const [hubUrl, setHubUrl] = useState(s.hubUrl);
  const [token, setToken] = useState(s.token);
  const [show, setShow] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(s.token && rejected ? "The Hub rejected the saved token. Paste a new one." : null);

  const connect = async (e: React.FormEvent) => {
    e.preventDefault();
    const tok = token.trim();
    if (!tok) {
      setError("Paste the access token from your Hub (security.dev_token in nexus.yaml, or the one run-local.sh printed).");
      return;
    }
    setBusy(true);
    setError(null);
    const prev = { hubUrl: s.hubUrl, token: s.token };
    // Apply first so the request goes to the new URL with the new token.
    useSettings.setState({ hubUrl: hubUrl.trim(), token: tok });
    reconfigureApi();
    try {
      await api().nodes.listNodes({}, { timeoutMs: 8000 });
      s.update({ hubUrl: hubUrl.trim(), token: tok, mock: false });
      void queryClient.invalidateQueries();
      startEventStream();
    } catch (err) {
      useSettings.setState(prev);
      reconfigureApi();
      const msg = errorMessage(err);
      setError(
        /unauthenticated|401|token/i.test(msg)
          ? "That token was rejected by the Hub."
          : /fetch|network|load failed/i.test(msg)
            ? `Can't reach a Hub at ${hubUrl.trim() || window.location.origin}. Check the URL, and that your browser trusts the Hub's certificate.`
            : msg,
      );
    } finally {
      setBusy(false);
    }
  };

  const demo = () => {
    s.update({ mock: true });
    location.reload();
  };

  return (
    <div className="flex min-h-dvh items-center justify-center bg-bg px-4 py-10">
      <div className="animate-fade-up w-full max-w-[400px]">
        <div className="mb-8 flex justify-center">
          <Logo size="lg" />
        </div>
        <div className="rounded-2xl border border-border bg-surface p-6 shadow-card sm:p-7">
          <h1 className="text-[20px] leading-tight font-semibold tracking-[-0.01em] text-fg">Connect to your Nexus</h1>
          <p className="mt-1.5 text-[13px] text-muted">The dashboard talks only to your Hub. Your token stays in this browser.</p>
          <form className="mt-6 space-y-4" onSubmit={connect} noValidate>
            <div>
              <label htmlFor="hub-url" className="mb-1.5 block text-[13px] font-medium text-fg">
                Hub URL
              </label>
              <Input id="hub-url" value={hubUrl} onChange={(e) => setHubUrl(e.target.value)} placeholder={window.location.origin} autoComplete="url" inputMode="url" spellCheck={false} />
              <p className="mt-1 text-xs text-muted">Leave empty when this page is served by the Hub.</p>
            </div>
            <div>
              <label htmlFor="hub-token" className="mb-1.5 block text-[13px] font-medium text-fg">
                Access token
              </label>
              <div className="relative">
                <Input
                  id="hub-token"
                  data-testid="connect-token"
                  type={show ? "text" : "password"}
                  value={token}
                  onChange={(e) => setToken(e.target.value)}
                  placeholder="Paste your token"
                  className="pr-11 font-mono"
                  autoComplete="current-password"
                  spellCheck={false}
                  autoFocus
                  aria-describedby={error ? "connect-error" : undefined}
                  aria-invalid={!!error}
                />
                <button
                  type="button"
                  onClick={() => setShow((v) => !v)}
                  className="absolute inset-y-0 right-0 flex w-11 items-center justify-center text-muted hover:text-fg"
                  aria-label={show ? "Hide token" : "Show token"}
                >
                  {show ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
                </button>
              </div>
            </div>
            {error && (
              <p id="connect-error" role="alert" className="rounded-lg bg-danger-soft px-3 py-2 text-[13px] text-danger">
                {error}
              </p>
            )}
            <button
              type="submit"
              disabled={busy}
              data-testid="connect-submit"
              className="flex h-11 w-full items-center justify-center gap-2 rounded-lg bg-primary text-[14px] font-medium text-primary-fg transition-opacity hover:opacity-90 disabled:opacity-60"
            >
              {busy ? <Loader2 className="size-4 animate-spin" /> : null}
              {busy ? "Connecting…" : "Connect"}
              {!busy && <ArrowRight className="size-4" />}
            </button>
          </form>
        </div>
        <button
          onClick={demo}
          className="mx-auto mt-5 flex h-11 items-center gap-2 rounded-lg px-4 text-[13px] text-fg-2 transition-colors hover:bg-surface-2 hover:text-fg"
        >
          <FlaskConical className="size-4" />
          Use demo mode
        </button>
        <p className="mt-2 text-center text-xs text-muted">Demo mode runs an in-browser Hub with sample agents, tasks and notes.</p>
        <p className="sr-only">Current Hub: {hubBaseUrl()}</p>
      </div>
    </div>
  );
}
