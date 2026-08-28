import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: { outDir: "out", emptyOutDir: true },
  server: { port: 5173, proxy: { "/api": "http://127.0.0.1:7777", "/ws": { target: "ws://127.0.0.1:7777", ws: true } } },
});
