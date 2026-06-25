import { defineConfig, loadEnv } from "vite";
import { gsx, devFallback } from "@gsxhq/vite-plugin-gsx";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const goPort = env.GO_PORT || "7777";
  const vitePort = parseInt(env.VITE_PORT || "5173", 10);
  // Serve a self-recovering interstitial (tails tmp/dev.log, polls /__dev/status)
  // while the Go server is down/restarting, instead of a raw proxy error.
  const fallback = devFallback({ target: `http://localhost:${goPort}`, logFile: "tmp/dev.log" });
  return {
    clearScreen: false,
    publicDir: false,
    plugins: [gsx(), fallback.plugin],
    server: {
      port: vitePort,
      proxy: {
        // Everything except Vite-owned namespaces (and /__dev/status, served by
        // the fallback plugin) goes to the Go server. No `ws: true` — the Go
        // server has no WebSocket; proxying ws would capture Vite's HMR socket.
        "^(?!/@vite|/@id|/@fs|/web/|/node_modules|/__reload|/__dev).*": {
          target: `http://localhost:${goPort}`,
          changeOrigin: true,
          configure: fallback.configureProxy,
        },
      },
    },
    build: {
      manifest: true,
      outDir: "dist",
      rollupOptions: { input: "web/main.js" },
    },
  };
});
