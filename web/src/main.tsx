import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import { App } from "./App";
import { initApi } from "./lib/api";
import { startEventStream } from "./lib/events";
import { loadChat } from "./lib/chat";

async function boot() {
  await initApi();
  loadChat();
  startEventStream();
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <App />
    </StrictMode>,
  );
}

void boot();
