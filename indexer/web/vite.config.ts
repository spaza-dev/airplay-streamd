import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react({ jsxRuntime: "classic" })],
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    port: 5174,
    proxy: {
      "/api": "http://127.0.0.1:8788",
      "/openapi.json": "http://127.0.0.1:8788",
    },
  },
});
