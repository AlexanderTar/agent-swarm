import type { WebSocket } from "ws";
import {
  SwarmDatabase,
  SqliteVectorIndex,
  SessionService,
  TaskService,
  OllamaClient,
  KbStore,
  KbIndexer,
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
  sessions: SessionService;
  tasks: TaskService;
  ollama: OllamaClient;
  kb: KbStore;
  indexer: KbIndexer;
  token: string;
  clients: Set<WebSocket>;
  broadcast: (msg: unknown) => void;
  boardUrl: string;
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
  const sessions = new SessionService(db.db);
  const tasks = new TaskService(db.db, sessions);
  const kb = new KbStore(db.db, vector, ollama, paths);
  const indexer = new KbIndexer(kb, tasks, sessions);
  const token = readOrCreateToken(paths);
  const clients = new Set<WebSocket>();

  const broadcast = (msg: unknown) => {
    const data = JSON.stringify(msg);
    for (const client of clients) {
      if (client.readyState === 1) client.send(data);
    }
  };

  kb.startWatching(() => broadcast({ type: "kb_updated" }));

  return {
    config,
    paths,
    db,
    sessions,
    tasks,
    ollama,
    kb,
    indexer,
    token,
    clients,
    broadcast,
    boardUrl: `http://${config.host}:${config.port}`,
  };
}
