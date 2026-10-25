import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
// Isolated static panel fixture: no application server, API proxy, database or container.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: "node_modules/.cache/retention-preview",
    emptyOutDir: true,
    rollupOptions: { input: "tests/retention/index.html" },
  },
  preview: { host: "127.0.0.1", port: 18375, strictPort: true },
});
