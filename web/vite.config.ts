import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The build lands inside the Go module so depotd can embed it.
export default defineConfig({
  plugins: [react()],
  build: { outDir: "../internal/webui/dist", emptyOutDir: true },
  // ws: live video (/api/live/ws) is a WebSocket. changeOrigin stays off: the
  // hub checks that Origin matches Host, and both name the dev server.
  server: { proxy: { "/api": { target: "http://localhost:8080", ws: true } } },
});
