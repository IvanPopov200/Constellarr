import { spawnSync } from "node:child_process";
import { accessSync, constants, existsSync, mkdirSync, mkdtempSync, readFileSync, renameSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { delimiter, dirname, join, resolve } from "node:path";

export const requirementsFile = "requirements-subtitles.txt";
export const subtitleVenv = resolve("bin/subtitle-tools");
export const subtitleBin = join(subtitleVenv, process.platform === "win32" ? "Scripts" : "bin");
export const mediaFixture = resolve("bin/test-media/real-helper-v1.mkv");

const fixtureSeconds = 199;
const cueCount = 40;
const cuePeriod = 5;
const cueStart = 1.2;
const cueLength = 1.8;
const installFFmpeg = 'install ffmpeg (macOS: brew install ffmpeg; Debian/Ubuntu: sudo apt-get install ffmpeg)';
const installBackup = 'install the PostgreSQL 18 client tools (macOS: brew install postgresql@18; Debian/Ubuntu: postgresql-client-18 from apt.postgresql.org)';
const tools = [
  { name: "ffmpeg", hint: installFFmpeg },
  { name: "ffprobe", hint: installFFmpeg },
  { name: "par2", hint: "install par2cmdline (macOS: brew install par2; Debian/Ubuntu: sudo apt-get install par2)" },
  { name: "pg_dump", major: 18, hint: installBackup },
  { name: "pg_restore", major: 18, hint: installBackup },
  { name: "ffsubsync", pin: true, hint: 'run "make subtitle-tools" to install ffsubsync into bin/subtitle-tools' },
];

function executable(path) {
  try {
    if (!statSync(path).isFile()) return false;
    accessSync(path, constants.X_OK);
    return true;
  } catch {
    return false;
  }
}

export function findTool(name, local = name === "ffsubsync") {
  if (local && executable(join(subtitleBin, name))) return join(subtitleBin, name);
  const suffixes = process.platform === "win32" ? (process.env.PATHEXT || ".EXE").split(";") : [""];
  for (const directory of (process.env.PATH || "").split(delimiter)) {
    if (!directory) continue;
    for (const suffix of suffixes) {
      const candidate = join(directory, name + suffix.toLowerCase());
      if (executable(candidate)) return candidate;
    }
  }
  return "";
}

export function toolVersion(path) {
  const result = spawnSync(path, ["--version"], { encoding: "utf8", timeout: 10000 });
  const text = `${result.stdout || ""}\n${result.stderr || ""}`;
  const match = text.match(/\b(\d+\.\d+(?:\.\d+)?)\b/);
  return match ? match[1] : "";
}

export function subtitleRequirement() {
  try {
    const match = readFileSync(requirementsFile, "utf8").match(/^\s*ffsubsync\s*==\s*(\S+)/m);
    return match ? match[1] : "";
  } catch {
    return "";
  }
}

export function inspectTools() {
  const requirement = subtitleRequirement();
  return tools.map((tool) => {
    const entry = { name: tool.name, path: "", version: "", ok: false, problem: "", hint: tool.hint };
    entry.path = findTool(tool.name);
    if (!entry.path) {
      entry.problem = `${tool.name} was not found`;
      return entry;
    }
    entry.version = toolVersion(entry.path);
    const major = entry.version ? Number(entry.version.split(".")[0]) : NaN;
    if (!Number.isFinite(major)) {
      entry.problem = `${tool.name} did not report a version`;
    } else if (tool.major && major < tool.major) {
      entry.problem = `${tool.name} ${entry.version} is older than PostgreSQL ${tool.major}`;
    } else if (tool.pin && requirement && entry.version !== requirement) {
      entry.problem = `${tool.name} ${entry.version} does not match the pinned ${requirement}`;
    }
    entry.ok = !entry.problem;
    return entry;
  });
}

export function nativeReport(entries = inspectTools()) {
  const lines = ["Native subtitle, media, and backup tools:"];
  for (const entry of entries) {
    lines.push(entry.ok
      ? `  ok    ${entry.name} ${entry.version} (${entry.path})`
      : `  fail  ${entry.problem}; ${entry.hint}`);
  }
  return { ok: entries.every((entry) => entry.ok), text: lines.join("\n") };
}

export function ensureMediaFixture({ force = false } = {}) {
  if (!force && existsSync(mediaFixture)) return mediaFixture;
  const ffmpeg = findTool("ffmpeg");
  if (!ffmpeg) throw new Error(`ffmpeg is required to build the media fixture; ${installFFmpeg}`);
  const target = `${mediaFixture}.tmp`;
  const work = mkdtempSync(join(tmpdir(), "constellarr-fixture-"));
  try {
    const sheet = join(work, "reference.srt");
    writeFileSync(sheet, cueSheet());
    const audio = `0.30*between(mod(t\\,${cuePeriod})\\,${cueStart}\\,${cueStart + cueLength})`
      + `*(sin(2*PI*130*t)+0.6*sin(2*PI*260*t)+0.4*sin(2*PI*390*t)+0.3*sin(2*PI*780*t)+0.06*random(0))`;
    mkdirSync(dirname(mediaFixture), { recursive: true });
    const result = spawnSync(ffmpeg, [
      "-nostdin", "-v", "error", "-y",
      "-f", "lavfi", "-i", `aevalsrc=exprs='${audio}':s=16000:d=${fixtureSeconds}`,
      "-f", "lavfi", "-i", `testsrc=duration=${fixtureSeconds}:size=320x240:rate=10`,
      "-i", sheet,
      "-map", "0:a", "-map", "1:v", "-map", "2:s",
      "-c:a", "aac", "-b:a", "64k", "-c:v", "mpeg4", "-q:v", "8", "-c:s", "srt",
      "-f", "matroska", "-shortest", target,
    ], { encoding: "utf8", timeout: 120000 });
    if (result.status !== 0) {
      const detail = `${result.stderr || ""}${result.error?.message || ""}`.trim().split("\n").pop();
      throw new Error(`ffmpeg could not build the media fixture: ${detail || "unknown error"}`);
    }
    renameSync(target, mediaFixture);
  } finally {
    rmSync(work, { recursive: true, force: true });
    rmSync(target, { force: true });
  }
  return mediaFixture;
}

function cueSheet() {
  let sheet = "";
  for (let index = 0; index < cueCount; index++) {
    const start = index * cuePeriod + cueStart;
    sheet += `${index + 1}\n${stamp(start)} --> ${stamp(start + cueLength)}\nLine ${index + 1}\n\n`;
  }
  return sheet;
}

function stamp(seconds) {
  const parts = (value, width) => String(value).padStart(width, "0");
  const ms = Math.round(seconds * 1000);
  return `${parts(Math.floor(ms / 3600000), 2)}:${parts(Math.floor(ms / 60000) % 60, 2)}:${parts(Math.floor(ms / 1000) % 60, 2)},${parts(ms % 1000, 3)}`;
}

if (process.argv[1]?.endsWith("native.mjs")) {
  const entries = inspectTools();
  const report = nativeReport(entries);
  if (process.argv.includes("--json")) {
    console.log(JSON.stringify({ ok: report.ok, tools: entries }, null, 2));
  } else {
    console.log(report.text);
    if (!report.ok) console.error('Install or update the tools above, then rerun "make doctor".');
  }
  process.exit(report.ok ? 0 : 1);
}
