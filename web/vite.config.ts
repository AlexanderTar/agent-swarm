/// <reference types="vitest/config" />
import { writeFileSync } from "node:fs";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

const daemon = process.env.SWARM_DEV_URL ?? "http://127.0.0.1:17777";

export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    {
      // emptyOutDir wipes dist/; //go:embed all:dist needs the tracked .gitkeep back.
      name: "keep-dist-gitkeep",
      apply: "build",
      closeBundle() {
        writeFileSync(new URL("./dist/.gitkeep", import.meta.url), "");
      },
    },
  ],
  build: { outDir: "dist", emptyOutDir: true },
  server: { port: 5173, proxy: { "/api": { target: daemon } } },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
    css: false,
  },
});
