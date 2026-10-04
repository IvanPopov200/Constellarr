import { existsSync } from "node:fs";
import { resolve } from "node:path";

if (!existsSync(".env")) {
  console.error('Missing .env; run "make setup" first.');
  process.exit(1);
}
process.loadEnvFile(".env");

const database = new URL("postgresql://127.0.0.1");
database.username = process.env.POSTGRES_USER || "constellarr";
database.password = process.env.POSTGRES_PASSWORD || "";
database.port = process.env.POSTGRES_PORT || "5432";
database.pathname = process.env.POSTGRES_DB || "constellarr";
database.searchParams.set("sslmode", "disable");

export const environment = {
  ...process.env,
  DATABASE_URL: process.env.DATABASE_URL || database.href,
  HTTP_ADDR: `127.0.0.1:${process.env.APP_PORT || "8080"}`,
  API_PROXY_TARGET: `http://127.0.0.1:${process.env.APP_PORT || "8080"}`,
  DOWNLOAD_DIR: resolve(process.env.DOWNLOAD_DIR || "data"),
};
