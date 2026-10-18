import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@fontsource-variable/inter/index.css";
import "@fontsource-variable/jetbrains-mono/index.css";
import "@fontsource-variable/montserrat/index.css";
import "./index.css";
import { App } from "./App";
import { initApi } from "./lib/api";
import { startEventStream } from "./lib/events";
import { loadChat } from "./lib/chat";
import { useSettings } from "./lib/settings";

/**
 * `https://hub:7700/#token=…` stores the token and strips it from the address bar, so the
 * one-line URL printed by scripts/mac/run-local.sh logs you in (fragments never reach the server).
 */
function adoptTokenFromHash() {
  const m = /(?:^#|&)token=([^&]+)/.exec(location.hash);
  if (!m) return;
  useSettings.getState().update({ token: decodeURIComponent(m[1]), mock: false });
  history.replaceState(null, "", location.pathname + location.search);
}

async function boot() {
  adoptTokenFromHash();
  await initApi();
  loadChat();
  const { token, mock } = useSettings.getState();
  // Without a token the Connect screen shows; it starts the stream after a successful connect.
  if (token || mock) startEventStream();
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <App />
    </StrictMode>,
  );
}

void boot();
