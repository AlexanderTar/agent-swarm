import crypto from "node:crypto";
import { readFileSync, writeFileSync, mkdirSync, existsSync } from "node:fs";
import { DEFAULT_CONFIG, getSwarmPaths, type SwarmConfig } from "./types.js";

export { DEFAULT_CONFIG, getSwarmPaths };
export * from "./types.js";
export * from "./db.js";
export * from "./ollama.js";
export * from "./tasks.js";
export * from "./hooks.js";
export * from "./sessions.js";
export * from "./cursorSessions.js";
export * from "./antigravitySessions.js";
export * from "./sessionTitles.js";
export * from "./transcripts.js";
export * from "./sessionSummary.js";
export * from "./kb.js";
export * from "./memory.js";

export function loadConfig(home?: string): SwarmConfig {
  const paths = getSwarmPaths(home);
  if (!existsSync(paths.config)) {
    mkdirSync(paths.home, { recursive: true });
    writeFileSync(paths.config, JSON.stringify(DEFAULT_CONFIG, null, 2));
    return { ...DEFAULT_CONFIG };
  }
  const raw = JSON.parse(readFileSync(paths.config, "utf8")) as Partial<SwarmConfig>;
  return { ...DEFAULT_CONFIG, ...raw };
}

export function saveConfig(config: SwarmConfig, home?: string): void {
  const paths = getSwarmPaths(home);
  mkdirSync(paths.home, { recursive: true });
  writeFileSync(paths.config, JSON.stringify(config, null, 2));
}

export function ensureSwarmDirs(home?: string): ReturnType<typeof getSwarmPaths> {
  const paths = getSwarmPaths(home);
  for (const dir of [paths.home, paths.kb, paths.logs, paths.run, paths.backups, paths.bin, `${paths.kb}/notes`, `${paths.kb}/handoffs`, `${paths.kb}/decisions`, `${paths.kb}/inbox`]) {
    mkdirSync(dir, { recursive: true });
  }
  return paths;
}

export function readOrCreateToken(paths: ReturnType<typeof getSwarmPaths>): string {
  const tokenPath = `${paths.run}/daemon.token`;
  if (existsSync(tokenPath)) return readFileSync(tokenPath, "utf8").trim();
  const token = crypto.randomUUID();
  writeFileSync(tokenPath, token, { mode: 0o600 });
  return token;
}
