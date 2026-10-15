// Dev server for working on the dashboard against a running OpenLore (see scripts/harness-dev.mjs).
// The proxy adds a bearer token, so no passkey sign-in is needed while developing.
import { defineConfig, mergeConfig } from "vitest/config";
import base from "./vite.config.ts";

const target = process.env.OPENLORE_DEV_URL ?? "http://localhost:8485";
const token = process.env.OPENLORE_DEV_TOKEN ?? "";
const proxy = {
  target,
  changeOrigin: true,
  headers: token ? { Authorization: `Bearer ${token}` } : {},
};

export default mergeConfig(
  base,
  defineConfig({
    server: {
      port: Number(process.env.DASHBOARD_DEV_PORT ?? 5180),
      strictPort: true,
      proxy: {
        "/dashboard/api": proxy,
        "/api": proxy,
        // /lore/... is a dashboard view; only ?raw=1 is served by OpenLore itself.
        "/lore": {
          ...proxy,
          bypass: (req) => (req.url?.includes("raw=1") ? undefined : "/dashboard/index.html"),
        },
      },
    },
  }),
);
