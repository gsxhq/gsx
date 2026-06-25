import { defineConfig, loadEnv } from "vite";
import { gsx } from "@gsxhq/vite-plugin-gsx";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const goPort = env.GO_PORT || "7777";
  const vitePort = parseInt(env.VITE_PORT || "5173", 10);
  return {
    // Don't wipe the terminal — keep the combined Go + Vite log readable.
    clearScreen: false,
    plugins: [gsx()],
    server: {
      port: vitePort,
      proxy: {
        // Everything except Vite-owned namespaces goes to the Go server.
        // No `ws: true`: the Go server has no WebSocket endpoints, and proxying
        // ws would capture Vite's own HMR socket and flood the log with
        // write-after-end errors on each Go restart. If you add a Go WebSocket,
        // set `ws: true` here AND isolate Vite's HMR with
        // server.hmr.path = "/@vite-hmr" so it stays unproxied.
        "^(?!/@vite|/@id|/@fs|/web/|/node_modules|/__reload).*": {
          target: `http://localhost:${goPort}`,
          changeOrigin: true,
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
