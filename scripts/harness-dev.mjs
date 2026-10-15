#!/usr/bin/env node
// Runs a local OpenLore for development, set up exactly as a deployed Agent Harness server
// (`openlore harness init`: no anonymous access, SSH off), plus the dashboard's Vite dev server with hot reload.
//
//   node scripts/harness-dev.mjs [--seed <docs folder>]
//
// The server runs on http://localhost:8485 from bin/openlore(.exe) (build it first with
// `go build -o bin/openlore.exe ./cmd/openlore`). Its config and docs live in .dev/ (not tracked; delete it to start
// over); --seed copies a folder of docs in when the docs folder is empty. On first run it prints a setup code: paste it
// into Agent Harness → Settings → Knowledge base to connect that app to this server as an admin machine.
// The dashboard is at http://localhost:5180/dashboard/; Vite's proxy signs every request in as the owner (`dev`) with
// a token minted here, so there is no passkey sign-in. The server's own http://localhost:8485/dashboard/ uses passkeys.
import { spawn, execFileSync } from "node:child_process";
import { cpSync, existsSync, readdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const dev = join(root, ".dev");
const exe = join(root, "bin", process.platform === "win32" ? "openlore.exe" : "openlore");
const port = Number(process.env.OPENLORE_DEV_PORT ?? 8485);
const url = `http://localhost:${port}`;
const yml = join(dev, "openlore.yml");

if (!existsSync(exe)) {
  console.error(`No server at ${exe}. Build it: go build -o bin/openlore.exe ./cmd/openlore`);
  process.exit(1);
}

execFileSync(exe, ["harness", "init", "--dir", dev, "--url", url, "--http-port", String(port), "--owner", "dev"], { stdio: "inherit" });
const docs = join(dev, "docs");
const seed = process.argv.indexOf("--seed");
if (seed > 0 && process.argv[seed + 1] && readdirSync(docs).length === 0) {
  cpSync(resolve(process.argv[seed + 1]), docs, { recursive: true });
}

const token = execFileSync(exe, ["token", "mint", "--config", yml, "--identity", "dev", "--ttl", "720h"], { cwd: dev, encoding: "utf8" }).trim();

const children = [];
const run = (name, cmd, args, opts) => {
  const p = spawn(cmd, args, { stdio: "inherit", ...opts });
  p.on("exit", (code) => {
    console.log(`${name} exited (${code}); stopping.`);
    stop();
  });
  children.push(p);
};
const stop = () => {
  for (const c of children) if (c.exitCode === null) c.kill();
  process.exit();
};
process.on("SIGINT", stop);
process.on("SIGTERM", stop);

run("openlore", exe, ["-c", yml, docs], { cwd: dev });
run("vite", "npx", ["vite", "--config", "vite.dev.config.ts"], {
  cwd: join(root, "dashboard"),
  shell: process.platform === "win32",
  env: { ...process.env, OPENLORE_DEV_URL: url, OPENLORE_DEV_TOKEN: token },
});
console.log(`\nDashboard (hot reload): http://localhost:${process.env.DASHBOARD_DEV_PORT ?? 5180}/dashboard/\nServer: ${url}\n`);
