import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { fileURLToPath } from "node:url";

const hub = process.env.NEXUS_HUB ?? "http://localhost:7700";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: 5173,
    // The mock fixtures read ../vault-template at build time.
    fs: { allow: [fileURLToPath(new URL("..", import.meta.url))] },
    proxy: {
      // Every Connect RPC lives under /nexus.v1.<Service>/<Method>.
      "^/nexus\\.v1\\..*": { target: hub, changeOrigin: true, ws: false },
    },
  },
  preview: {
    proxy: {
      "^/nexus\\.v1\\..*": { target: hub, changeOrigin: true },
    },
  },
  build: {
    target: "es2022",
    chunkSizeWarningLimit: 600,
  },
});
