import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { VitePWA } from "vite-plugin-pwa";

// The build lands inside the Go module so depotd can embed it.
export default defineConfig({
  plugins: [
    react(),
    // The service worker keeps the app shell, so a gate phone can reload without network.
    // It caches nothing else: API answers never come from it (the gate keeps its own lists in IndexedDB).
    VitePWA({
      registerType: "autoUpdate",
      manifest: false,
      workbox: {
        globPatterns: ["**/*.{js,css,html,woff2,woff,svg,png,ico}"],
        navigateFallback: "index.html",
        navigateFallbackDenylist: [/^\/api\//, /^\/auth\//, /^\/eir\//],
        runtimeCaching: [],
        cleanupOutdatedCaches: true,
      },
    }),
  ],
  build: { outDir: "../internal/webui/dist", emptyOutDir: true },
  // ws: live video (/api/live/ws) is a WebSocket. changeOrigin stays off: the
  // hub checks that Origin matches Host, and both name the dev server.
  server: { proxy: { "/api": { target: "http://localhost:8080", ws: true } } },
});
