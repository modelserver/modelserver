import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "node:path";

export default defineConfig({
  plugins: [react()],
  base: "/admin/",
  server: {
    port: 5174,
    proxy: {
      "/admin/api": "http://localhost:8090",
      "/admin/login": "http://localhost:8090",
      "/admin/callback": "http://localhost:8090",
      "/admin/logout": "http://localhost:8090",
      "/admin/whoami": "http://localhost:8090",
    },
  },
  resolve: {
    alias: { "@": path.resolve(__dirname, "src") },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
