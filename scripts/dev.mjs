import { spawn, spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import { setTimeout as delay } from "node:timers/promises";
import { environment } from "./env.mjs";

if (!process.argv.includes("--no-db")) {
  const result = spawnSync("docker", ["compose", "-f", "compose.yaml", "-f", "compose.dev.yaml", "up", "-d", "--wait", "postgres"], { stdio: "inherit" });
  if (result.error || result.status !== 0) {
    console.error("PostgreSQL could not start. Check Docker and POSTGRES_PORT in .env.");
    process.exit(1);
  }
}

if (process.argv.includes("--test")) {
  const database = `constellarr_test_${randomBytes(6).toString("hex")}`;
  const url = new URL(environment.DATABASE_URL);
  url.pathname = database;
  const databaseCommand = (action) => spawnSync("docker", ["compose", "exec", "-T", "postgres", "sh", "-c",
    `PGPASSWORD="$POSTGRES_PASSWORD" ${action} -U "$POSTGRES_USER" "$1"`, "sh", database], { stdio: "inherit" });
  const suppliedURL = environment.TEST_DATABASE_URL;
  if (!suppliedURL && databaseCommand("createdb").status !== 0) process.exit(1);
  let code = 1;
  try {
    const result = spawnSync("go", ["-C", "backend", "test", "-count=1", "-v", "./..."], {
      stdio: "inherit", env: { ...environment, TEST_DATABASE_URL: suppliedURL || url.href },
    });
    code = result.status ?? 1;
  } finally {
    if (!suppliedURL) databaseCommand("dropdb");
  }
  process.exit(code);
}

const grouped = process.platform !== "win32";
const children = [
  spawn("go", ["-C", "backend", "run", "./cmd/constellarr"], { stdio: "inherit", env: environment, detached: grouped }),
];
let stopping = false;

function signal(child, name) {
  if (!child.pid) return;
  try {
    if (grouped) process.kill(-child.pid, name);
    else child.kill(name);
  } catch (error) {
    if (error.code !== "ESRCH") console.error(error.message);
  }
}

function stop(code) {
  if (stopping) return;
  stopping = true;
  process.exitCode = code;
  for (const child of children) signal(child, "SIGTERM");
  setTimeout(() => {
    for (const child of children) signal(child, "SIGKILL");
  }, 3000).unref();
}

function watch(child) {
  child.once("exit", (code) => stop(code ?? 1));
  child.once("error", (error) => {
    console.error(error.message);
    stop(1);
  });
}
children.forEach(watch);
process.on("SIGINT", () => stop(0));
process.on("SIGTERM", () => stop(0));
process.on("SIGHUP", () => stop(0));
process.on("exit", () => children.forEach((child) => signal(child, "SIGKILL")));

while (!stopping) {
  try {
    if ((await fetch(`${environment.API_PROXY_TARGET}/healthz`, { signal: AbortSignal.timeout(1000) })).ok) break;
  } catch {}
  await delay(200);
}
if (!stopping) {
  const frontend = spawn("npm", ["--prefix", "frontend", "run", "dev"], { stdio: "inherit", env: environment, detached: grouped });
  children.push(frontend);
  watch(frontend);
}
