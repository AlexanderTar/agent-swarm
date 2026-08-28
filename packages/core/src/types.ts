export const TASK_STATUSES = [
  "backlog",
  "ready",
  "in_progress",
  "blocked",
  "review",
  "done",
  "archived",
] as const;

export type TaskStatus = (typeof TASK_STATUSES)[number];

export type AgentKind = "claude" | "cursor" | "codex" | "antigravity" | "unknown";

export interface SwarmConfig {
  port: number;
  host: string;
  ollamaUrl: string;
  embedModel: string;
  chatModel: string;
  embedDimensions: number;
  claimLeaseSeconds: number;
  autoUpdate: boolean;
  updateIntervalSeconds: number;
  janitorIdleMinutes: number;
  janitorMinTurns: number;
  repoUrl: string;
}

export const DEFAULT_CONFIG: SwarmConfig = {
  port: 7777,
  host: "127.0.0.1",
  ollamaUrl: "http://127.0.0.1:11434",
  embedModel: "nomic-embed-text",
  chatModel: "qwen3:4b",
  embedDimensions: 256,
  claimLeaseSeconds: 300,
  autoUpdate: true,
  updateIntervalSeconds: 3600,
  janitorIdleMinutes: 30,
  janitorMinTurns: 1,
  repoUrl: "https://github.com/alexandertar/agent-swarm.git",
};

export interface TaskRecord {
  id: number;
  key: string;
  title: string;
  status: TaskStatus;
  priority: string;
  repoPath: string | null;
  repoRemote: string | null;
  branch: string | null;
  worktree: string | null;
  originAgent: AgentKind;
  originSessionId: string | null;
  originModel: string | null;
  originCwd: string | null;
  originPid: number | null;
  claimedBy: string | null;
  claimedAgent: AgentKind | null;
  claimedSessionId: string | null;
  claimedAt: string | null;
  claimExpiresAt: string | null;
  heartbeatAt: string | null;
  initialContext: string | null;
  handoffNote: string | null;
  artifactsJson: string;
  kbLinksJson: string;
  tagsJson: string;
  turnCount: number;
  lastActivityAt: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface TaskEvent {
  id: number;
  taskId: number;
  eventType: string;
  payloadJson: string;
  createdAt: string;
}

export interface SubtaskRecord {
  id: number;
  taskId: number;
  subject: string;
  description: string | null;
  completed: boolean;
  createdAt: string;
}

export interface SessionRecord {
  id: string;
  agentKind: AgentKind;
  cwd: string | null;
  model: string | null;
  pid: number | null;
  taskId: number | null;
  startedAt: string;
  endedAt: string | null;
}

export interface KbDoc {
  id: number;
  slug: string;
  title: string;
  path: string;
  frontmatterJson: string;
  contentHash: string;
  supersededBy: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface KbChunk {
  id: number;
  docId: number;
  chunkIndex: number;
  heading: string | null;
  body: string;
  contentHash: string;
}

export interface BoardFilters {
  status?: TaskStatus;
  repo?: string;
  agent?: AgentKind;
  stale?: boolean;
}

export interface HandoffNote {
  goal: string;
  done: string;
  nextSteps: string[];
  decisions: string[];
  gotchas: string[];
  verification: string[];
  files: Array<{ path: string; reason: string }>;
  kbRefs: string[];
  openQuestions: string[];
}

export interface SwarmPaths {
  home: string;
  config: string;
  db: string;
  kb: string;
  logs: string;
  run: string;
  backups: string;
  app: string;
  current: string;
  bin: string;
}

export function getSwarmPaths(home = process.env.SWARM_HOME ?? `${process.env.HOME}/.swarm`): SwarmPaths {
  return {
    home,
    config: `${home}/config.json`,
    db: `${home}/swarm.db`,
    kb: `${home}/kb`,
    logs: `${home}/logs`,
    run: `${home}/run`,
    backups: `${home}/backups`,
    app: `${home}/app`,
    current: `${home}/app/current`,
    bin: `${home}/bin`,
  };
}
