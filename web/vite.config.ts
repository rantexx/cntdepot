import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The build lands inside the Go module so depotd can embed it.
export default defineConfig({
  plugins: [react()],
  build: { outDir: "../internal/webui/dist", emptyOutDir: true },
  server: { proxy: { "/api": "http://localhost:8080" } },
});
