import { defineConfig } from "vite";
import { gsx } from "@gsxhq/vite-plugin-gsx";

export default defineConfig({
  plugins: [gsx()],
  server: {
    proxy: {
      // Everything except Vite-owned namespaces goes to the Go server.
      "^(?!/@vite|/@id|/@fs|/web/|/node_modules|/__reload).*": {
        target: "http://localhost:7777",
        changeOrigin: true,
        ws: true,
      },
    },
  },
  build: {
    manifest: true,
    outDir: "dist",
    rollupOptions: { input: "web/main.js" },
  },
});
