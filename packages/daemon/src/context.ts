import type { WebSocket } from "ws";
import {
  SwarmDatabase,
  SqliteVectorIndex,
  TaskService,
  OllamaClient,
  KbStore,
  MemoryJobs,
  loadConfig,
  ensureSwarmDirs,
  readOrCreateToken,
  type SwarmConfig,
} from "@swarm/core";
import type { getSwarmPaths } from "@swarm/core";

export interface SwarmContext {
  config: SwarmConfig;
  paths: ReturnType<typeof getSwarmPaths>;
  db: SwarmDatabase;
  tasks: TaskService;
  ollama: OllamaClient;
  kb: KbStore;
  memory: MemoryJobs;
  token: string;
  clients: Set<WebSocket>;
  broadcast: (msg: unknown) => void;
}

export function createContext(home?: string): SwarmContext {
  const paths = ensureSwarmDirs(home);
  const config = loadConfig(home);
  const db = new SwarmDatabase(paths.db, config.embedDimensions);
  const embedCheck = db.checkEmbeddingConfig(config);
  if (!embedCheck.ok) {
    console.warn(embedCheck.reason);
  }
  const vector = new SqliteVectorIndex(db.db);
  const ollama = new OllamaClient(config);
  const tasks = new TaskService(db.db);
  const merged = tasks.consolidateDuplicateSessions();
  if (merged > 0) {
    console.warn(`[swarm] consolidated ${merged} duplicate session tile(s)`);
  }
  const kb = new KbStore(db.db, vector, ollama, paths);
  const memory = new MemoryJobs(ollama, kb, tasks);
  const token = readOrCreateToken(paths);
  const clients = new Set<WebSocket>();

  const broadcast = (msg: unknown) => {
    const data = JSON.stringify(msg);
    for (const client of clients) {
      if (client.readyState === 1) client.send(data);
    }
  };

  kb.startWatching(() => broadcast({ type: "kb_updated" }));

  return { config, paths, db, tasks, ollama, kb, memory, token, clients, broadcast };
}
