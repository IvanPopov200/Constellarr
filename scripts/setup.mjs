import { spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { ensureMediaFixture, findTool, inspectTools, nativeReport, requirementsFile, subtitleBin, subtitleRequirement, subtitleVenv, toolVersion } from "./native.mjs";

const args = new Set(process.argv.slice(2));

function usage(code) {
  console.log(`Usage: node scripts/setup.mjs [--subtitle-tools | --media-fixture | --help]

Creates .env when missing, installs the isolated subtitle tools, and reports missing native tools.
  --subtitle-tools  install only ffsubsync ${subtitleRequirement() || "(pinned)"} into bin/subtitle-tools
  --media-fixture   rebuild the synthetic video used by the real-helper tests
  --help            show this message`);
  process.exit(code);
}

function fail(message) {
  console.error(message);
  process.exit(1);
}

function ensureEnvironmentFile() {
  if (existsSync(".env")) return;
  const template = readFileSync(".env.example", "utf8");
  const configured = template
    .replace("POSTGRES_PASSWORD=", `POSTGRES_PASSWORD=${randomBytes(24).toString("hex")}`)
    .replace("APP_UID=1000", `APP_UID=${process.getuid?.() ?? 1000}`)
    .replace("APP_GID=1000", `APP_GID=${process.getgid?.() ?? 1000}`);
  writeFileSync(".env", configured, { mode: 0o600 });
  console.log("Created .env with a generated database password.");
}

function installSubtitleTools() {
  const requirement = subtitleRequirement();
  const helper = join(subtitleBin, process.platform === "win32" ? "ffsubsync.exe" : "ffsubsync");
  const installed = existsSync(helper) ? toolVersion(helper) : "";
  if (requirement && installed === requirement) {
    console.log(`Subtitle tools ready (ffsubsync ${requirement}).`);
    return;
  }
  const python = findTool("python3") || findTool("python");
  if (!python) fail('Python 3 is required for the subtitle tools; install python3 and rerun "make setup".');
  mkdirSync(dirname(subtitleVenv), { recursive: true });
  if (spawnSync(python, ["-m", "venv", subtitleVenv], { stdio: "inherit" }).status !== 0) {
    fail('"python3 -m venv" failed; on Debian/Ubuntu install the python3-venv package and rerun "make setup".');
  }
  const venvPython = join(subtitleBin, process.platform === "win32" ? "python.exe" : "python");
  if (spawnSync(venvPython, ["-m", "pip", "install", "--disable-pip-version-check", "--no-input", "-r", requirementsFile], { stdio: "inherit" }).status !== 0) {
    fail(`Installing ${requirementsFile} failed; check the output above and rerun "make subtitle-tools".`);
  }
  const version = toolVersion(helper);
  if (requirement && version !== requirement) {
    fail(`ffsubsync ${requirement} was expected but ${version || "no version was installed"}; check ${requirementsFile} and rerun "make subtitle-tools".`);
  }
  console.log(`Installed ffsubsync ${version || requirement} into bin/subtitle-tools.`);
}

if (args.has("--help")) usage(0);
if (args.has("--subtitle-tools")) {
  installSubtitleTools();
} else if (args.has("--media-fixture")) {
  try {
    console.log(`Media fixture: ${ensureMediaFixture({ force: true })}`);
  } catch (error) {
    fail(error.message);
  }
} else if (args.size) {
  usage(2);
} else {
  ensureEnvironmentFile();
  installSubtitleTools();
  const report = nativeReport(inspectTools());
  console.log(report.text);
  if (!report.ok) console.log('Some native tools are missing or outdated; the report above lists what to install.');
}
