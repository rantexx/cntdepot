import { defineConfig } from "orval";

export default defineConfig({
  depot: {
    input: "../api/openapi.yaml",
    output: {
      client: "fetch",
      target: "src/gen/api.ts",
      override: { mutator: { path: "src/http.ts", name: "http" } },
    },
  },
});
