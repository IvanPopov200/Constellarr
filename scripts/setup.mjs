import { randomBytes } from "node:crypto";
import { existsSync, readFileSync, writeFileSync } from "node:fs";

if (!existsSync(".env")) {
  const template = readFileSync(".env.example", "utf8");
  const configured = template
    .replace("POSTGRES_PASSWORD=", `POSTGRES_PASSWORD=${randomBytes(24).toString("hex")}`)
    .replace("APP_UID=1000", `APP_UID=${process.getuid?.() ?? 1000}`)
    .replace("APP_GID=1000", `APP_GID=${process.getgid?.() ?? 1000}`);
  writeFileSync(".env", configured, { mode: 0o600 });
  console.log("Created .env with a generated database password.");
}
