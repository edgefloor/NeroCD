import { defineConfig } from "vite";
import { writeFileSync } from "node:fs";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { tanstackRouter } from "@tanstack/router-plugin/vite";

const apiTarget = process.env.NEROCD_API_PROXY_TARGET ?? "http://127.0.0.1:8080";

export default defineConfig({
  plugins: [
    tanstackRouter({ target: "react", autoCodeSplitting: false }),
    react(),
    tailwindcss(),
    {
      name: "preserve-embedded-dist-placeholder",
      closeBundle() {
        writeFileSync(new URL("../dist/.gitkeep", import.meta.url), "");
      },
    },
  ],
  resolve: {
    alias: {
      "@": new URL("./src", import.meta.url).pathname,
    },
  },
  build: {
    outDir: "../dist",
    emptyOutDir: true,
    sourcemap: process.env.NEROCD_SOURCEMAP === "true",
  },
  server: {
    proxy: {
      "/api": apiTarget,
    },
  },
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.{ts,tsx}"],
    exclude: ["**/node_modules/**", "tests/browser/**"],
  },
});
