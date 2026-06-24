import { defineConfig, loadEnv } from "vite";
import { gsx } from "@gsxhq/vite-plugin-gsx";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const goPort = env.GO_PORT || "7777";
  const vitePort = parseInt(env.VITE_PORT || "5173", 10);
  return {
    plugins: [gsx()],
    server: {
      port: vitePort,
      proxy: {
        // Everything except Vite-owned namespaces goes to the Go server.
        "^(?!/@vite|/@id|/@fs|/web/|/node_modules|/__reload).*": {
          target: `http://localhost:${goPort}`,
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
  };
});
