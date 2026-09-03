import { useDroppable } from "@dnd-kit/core";
import { useCallback, useEffect, useMemo, useState } from "react";
import {
  DndContext,
  DragOverlay,
  PointerSensor,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core";
import { SortableContext, useSortable, verticalListSortingStrategy } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import ReactMarkdown from "react-markdown";
import { Activity, Bot, Copy, GripVertical, MessageSquare, PanelRightClose, Trash2 } from "lucide-react";

const STATUSES = ["ready", "in_progress", "blocked", "review", "done"] as const;

type TaskStatus = (typeof STATUSES)[number];

interface Task {
  id: number;
  key: string;
  title: string;
  status: TaskStatus | "handoff";
  originAgent: string;
  originSessionId?: string | null;
  originModel: string | null;
  repoPath: string | null;
  branch: string | null;
  initialContext: string | null;
  handoffNote: string | null;
  turnCount: number;
  lastActivityAt: string | null;
  claimedBy: string | null;
  claimedAgent?: string | null;
  claimedSessionId?: string | null;
  artifactsJson: string;
  kbLinksJson: string;
  tagsJson?: string;
}

const BOARD_TITLE_MAX = 48;

function boardTitle(title: string, maxLength = BOARD_TITLE_MAX): string {
  const trimmed = title.trim().replace(/\s+/g, " ");
  if (trimmed.length <= maxLength) return trimmed;
  return `${trimmed.slice(0, maxLength - 1).trimEnd()}…`;
}

const AGENT_COLORS: Record<string, string> = {
  claude: "bg-orange-500/20 text-orange-300 border-orange-500/40",
  cursor: "bg-blue-500/20 text-blue-300 border-blue-500/40",
  codex: "bg-emerald-500/20 text-emerald-300 border-emerald-500/40",
  antigravity: "bg-purple-500/20 text-purple-300 border-purple-500/40",
  unknown: "bg-zinc-500/20 text-zinc-300 border-zinc-500/40",
};

function AgentBadge({ agent }: { agent: string }) {
  return (
    <span className={`text-xs px-2 py-0.5 rounded-full border min-w-0 max-w-full break-all ${AGENT_COLORS[agent] ?? AGENT_COLORS.unknown}`}>
      {agent}
    </span>
  );
}

function Column({ status, tasks, onSelect, onRemove }: { status: TaskStatus; tasks: Task[]; onSelect: (t: Task) => void; onRemove: (key: string) => void }) {
  const { setNodeRef, isOver } = useDroppable({ id: status });
  return (
    <div className="w-[320px] flex-shrink-0 flex flex-col h-full min-h-0">
      <h2 className="flex-shrink-0 text-xs font-semibold uppercase tracking-wider text-zinc-500 mb-2 px-1">
        {status.replace("_", " ")} ({tasks.length})
      </h2>
      <SortableContext items={tasks.map((t) => t.key)} strategy={verticalListSortingStrategy}>
        <div
          ref={setNodeRef}
          className={`flex-1 min-h-0 overflow-y-auto swarm-scroll space-y-2 rounded-lg p-2 border transition-colors ${
            isOver ? "bg-zinc-800/80 border-zinc-600" : "bg-zinc-900/50 border-zinc-800/50"
          }`}
          data-status={status}
        >
          {tasks.map((task) => (
            <TaskCard key={task.key} task={task} onSelect={onSelect} onRemove={onRemove} />
          ))}
        </div>
      </SortableContext>
    </div>
  );
}

function TaskCard({ task, onSelect, onRemove }: { task: Task; onSelect: (t: Task) => void; onRemove: (key: string) => void }) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id: task.key });
  const style = { transform: CSS.Transform.toString(transform), transition, opacity: isDragging ? 0.5 : 1 };
  const files = useMemo(() => {
    try {
      return (JSON.parse(task.artifactsJson).files as string[]) ?? [];
    } catch {
      return [];
    }
  }, [task.artifactsJson]);
  const tags = useMemo(() => {
    try {
      const raw = task.tagsJson ? (JSON.parse(task.tagsJson) as unknown) : [];
      return Array.isArray(raw) ? raw.filter((t): t is string => typeof t === "string") : [];
    } catch {
      return [];
    }
  }, [task.tagsJson]);
  const transcript = useMemo(() => {
    try {
      const t = JSON.parse(task.artifactsJson).transcript as unknown;
      return Array.isArray(t) ? (t as unknown[]).find((v): v is string => typeof v === "string") : undefined;
    } catch {
      return undefined;
    }
  }, [task.artifactsJson]);
  // Prefer the live claim owner; fall back to the origin agent for unclaimed tiles.
  const displayAgent = task.claimedAgent ?? task.originAgent;
  const sessionLabel = task.claimedSessionId ?? task.originSessionId ?? null;
  const agentTitle = [
    task.claimedAgent ? `claimed by ${task.claimedAgent}` : `origin ${task.originAgent}`,
    sessionLabel ? `session ${sessionLabel}` : null,
    transcript ? `transcript ${transcript}` : null,
  ].filter(Boolean).join(" · ");
  const isLive = task.status === "in_progress" && task.lastActivityAt &&
    Date.now() - new Date(task.lastActivityAt).getTime() < 120_000;

  return (
    <div
      ref={setNodeRef}
      style={style}
      className="group w-full max-w-full overflow-hidden bg-zinc-900 border border-zinc-800 rounded-lg p-3 shadow-sm hover:border-zinc-600 cursor-pointer"
      onClick={() => onSelect(task)}
    >
      <div className="flex items-start gap-2">
        <button type="button" className="text-zinc-500 hover:text-zinc-300 mt-0.5" {...attributes} {...listeners} onClick={(e) => e.stopPropagation()}>
          <GripVertical size={14} />
        </button>
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 mb-1">
            <span className="text-xs font-mono text-zinc-500">{task.key}</span>
            {isLive && <Activity size={12} className="text-green-400 animate-pulse" />}
          </div>
          <h3 className="font-medium text-sm truncate" title={task.title}>
            {boardTitle(task.title)}
          </h3>
          <div className="flex flex-wrap gap-1 mt-2">
            <span title={agentTitle}>
              <AgentBadge agent={displayAgent} />
            </span>
            {task.originModel && <span className="text-xs px-1.5 py-0.5 bg-zinc-800 rounded min-w-0 max-w-full break-all">{task.originModel}</span>}
            {task.branch && <span className="text-xs px-1.5 py-0.5 bg-zinc-800 rounded min-w-0 max-w-full break-all">{task.branch}</span>}
            {tags.map((t) => (
              <span key={t} className="text-xs px-1.5 py-0.5 bg-zinc-800/60 rounded text-zinc-400 min-w-0 max-w-full break-all">{t}</span>
            ))}
            {files.length > 0 && <span className="text-xs text-zinc-500">{files.length} files</span>}
          </div>
        </div>
        <button
          type="button"
          className="opacity-0 group-hover:opacity-100 text-zinc-500 hover:text-red-400 p-1 rounded hover:bg-zinc-800 transition-opacity"
          title="Remove from board"
          aria-label="Remove from board"
          onClick={(e) => {
            e.stopPropagation();
            onRemove(task.key);
          }}
        >
          <Trash2 size={14} />
        </button>
      </div>
    </div>
  );
}

function TaskDetail({ task, onClose, onRemove }: { task: Task & { events?: Array<{ eventType: string; createdAt: string; payloadJson?: string }> }; onClose: () => void; onRemove: (key: string) => void }) {
  const [tab, setTab] = useState<"context" | "handoff" | "timeline" | "artifacts" | "kb">("context");
  const copy = (text: string) => void navigator.clipboard.writeText(text);
  const files = useMemo(() => {
    try {
      return (JSON.parse(task.artifactsJson).files as string[]) ?? [];
    } catch {
      return [];
    }
  }, [task.artifactsJson]);
  const kbRefs = useMemo(() => {
    try {
      return (JSON.parse(task.kbLinksJson) as string[]) ?? [];
    } catch {
      return [];
    }
  }, [task.kbLinksJson]);

  return (
    <div className="fixed inset-y-0 right-0 w-full max-w-lg bg-zinc-900 border-l border-zinc-800 shadow-2xl z-50 flex flex-col">
      <div className="p-4 border-b border-zinc-800 flex items-center justify-between gap-2">
        <div className="min-w-0">
          <p className="text-xs font-mono text-zinc-500">{task.key}</p>
          <h2 className="font-semibold truncate">{task.title}</h2>
        </div>
        <div className="flex items-center gap-1 flex-shrink-0">
          <button
            type="button"
            onClick={() => onRemove(task.key)}
            className="flex items-center gap-1 text-xs text-zinc-400 hover:text-red-400 px-2 py-1 rounded hover:bg-zinc-800"
            title="Remove from board"
          >
            <Trash2 size={14} />
            Remove
          </button>
          <button type="button" onClick={onClose} className="text-zinc-400 hover:text-white px-2">✕</button>
        </div>
      </div>
      <div className="flex gap-1 p-2 border-b border-zinc-800 overflow-x-auto swarm-scroll">
        {(["context", "handoff", "timeline", "artifacts", "kb"] as const).map((t) => (
          <button
            key={t}
            type="button"
            onClick={() => setTab(t)}
            className={`px-3 py-1 text-sm rounded whitespace-nowrap ${tab === t ? "bg-zinc-700" : "hover:bg-zinc-800"}`}
          >
            {t === "context" ? "Initial Context" : t === "handoff" ? "Summary" : t.charAt(0).toUpperCase() + t.slice(1)}
          </button>
        ))}
      </div>
      <div className="flex-1 overflow-auto swarm-scroll p-4 text-sm text-zinc-300 space-y-2">
        {tab === "context" && (
          <div>
            <button type="button" className="mb-2 flex items-center gap-1 text-xs text-zinc-400" onClick={() => copy(task.initialContext ?? "")}>
              <Copy size={12} /> Copy as prompt
            </button>
            <ReactMarkdown>{task.initialContext ?? "_No initial context_"}</ReactMarkdown>
          </div>
        )}
        {tab === "handoff" && (
          <div>
            <button type="button" className="mb-2 flex items-center gap-1 text-xs text-zinc-400" onClick={() => copy(task.handoffNote ?? "")}>
              <Copy size={12} /> Copy summary
            </button>
            <ReactMarkdown>{task.handoffNote ?? "_No summary yet_"}</ReactMarkdown>
          </div>
        )}
        {tab === "timeline" && (
          <ul className="space-y-2">
            {(task.events ?? []).map((e) => (
              <li key={`${e.eventType}-${e.createdAt}`} className="text-xs border-l-2 border-zinc-700 pl-2">
                <span className="text-zinc-500">{e.createdAt}</span> — {e.eventType}
              </li>
            ))}
          </ul>
        )}
        {tab === "artifacts" && (
          <ul className="space-y-1 font-mono text-xs">
            {files.length === 0 ? <li className="text-zinc-500">No artifacts yet</li> : files.map((f) => (
              <li key={f} className="truncate">{f}</li>
            ))}
          </ul>
        )}
        {tab === "kb" && (
          <ul className="space-y-1 text-xs">
            {kbRefs.length === 0 ? <li className="text-zinc-500">No KB references</li> : kbRefs.map((ref) => (
              <li key={ref}>{ref}</li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

function ConsolePanel({ authToken, open, onToggle }: { authToken: string; open: boolean; onToggle: () => void }) {
  const [messages, setMessages] = useState<Array<{ role: string; content: string }>>([]);
  const [input, setInput] = useState("");
  const [loading, setLoading] = useState(false);

  const send = async () => {
    if (!input.trim()) return;
    const next = [...messages, { role: "user", content: input }];
    setMessages(next);
    setInput("");
    setLoading(true);
    try {
      const res = await fetch("/api/chat", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          ...(authToken ? { Authorization: `Bearer ${authToken}` } : {}),
        },
        body: JSON.stringify({ messages: next }),
      });
      const data = await res.json() as { message?: { content?: string } };
      setMessages([...next, { role: "assistant", content: data.message?.content ?? "No response" }]);
    } catch (e) {
      setMessages([...next, { role: "assistant", content: `Error: ${e}` }]);
    } finally {
      setLoading(false);
    }
  };

  if (!open) {
    return (
      <button
        type="button"
        onClick={onToggle}
        className="flex-shrink-0 w-10 border-l border-zinc-800 bg-zinc-950 hover:bg-zinc-900 flex flex-col items-center justify-center gap-1.5 text-zinc-500 hover:text-zinc-200 transition-colors"
        title="Open Swarm Console"
        aria-label="Open Swarm Console"
      >
        <MessageSquare size={18} />
        <span className="text-[10px] font-medium uppercase tracking-wider [writing-mode:vertical-rl] rotate-180">
          Console
        </span>
      </button>
    );
  }

  return (
    <div className="w-80 flex-shrink-0 border-l border-zinc-800 flex flex-col bg-zinc-950 min-h-0">
      <div className="p-3 border-b border-zinc-800 flex items-center justify-between gap-2">
        <div className="flex items-center gap-2 min-w-0">
          <Bot size={16} className="flex-shrink-0 text-zinc-400" />
          <span className="font-medium text-sm truncate">Swarm Console</span>
        </div>
        <button
          type="button"
          onClick={onToggle}
          className="flex items-center gap-1.5 text-zinc-400 hover:text-zinc-100 px-2 py-1 rounded-md hover:bg-zinc-800 text-xs transition-colors"
          title="Collapse console"
          aria-label="Collapse console"
        >
          <PanelRightClose size={16} />
          <span>Collapse</span>
        </button>
      </div>
      <div className="flex-1 overflow-auto swarm-scroll p-3 space-y-2 text-sm min-h-0">
        {messages.length === 0 && (
          <p className="text-xs text-zinc-500">Ask about tasks, summaries, or the knowledge base.</p>
        )}
        {messages.map((m, i) => (
          <div key={i} className={`p-2 rounded ${m.role === "user" ? "bg-zinc-800 ml-4" : "bg-zinc-900 mr-4"}`}>
            {m.content}
          </div>
        ))}
      </div>
      <div className="p-2 border-t border-zinc-800 flex gap-2">
        <input
          className="flex-1 bg-zinc-900 border border-zinc-700 rounded px-2 py-1 text-sm"
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && void send()}
          placeholder="Ask about tasks or KB..."
        />
        <button type="button" onClick={() => void send()} disabled={loading} className="px-3 py-1 bg-blue-600 rounded text-sm">
          Send
        </button>
      </div>
    </div>
  );
}

export default function App() {
  const [tasks, setTasks] = useState<Task[]>([]);
  const [selected, setSelected] = useState<(Task & { events?: Array<{ eventType: string; createdAt: string }> }) | null>(null);
  const [activeId, setActiveId] = useState<string | null>(null);
  const [authToken, setAuthToken] = useState<string>("");
  const [consoleOpen, setConsoleOpen] = useState(() => {
    try {
      return localStorage.getItem("swarm-console-open") === "true";
    } catch {
      return false;
    }
  });

  const toggleConsole = useCallback(() => {
    setConsoleOpen((prev) => {
      const next = !prev;
      try {
        localStorage.setItem("swarm-console-open", String(next));
      } catch {
        // ignore
      }
      return next;
    });
  }, []);

  const apiHeaders = useMemo(
    () => ({
      "Content-Type": "application/json",
      ...(authToken ? { Authorization: `Bearer ${authToken}` } : {}),
    }),
    [authToken],
  );

  const load = useCallback(async () => {
    const res = await fetch("/api/board", { headers: apiHeaders });
    if (res.ok) setTasks(await res.json());
  }, [apiHeaders]);

  useEffect(() => {
    void fetch("/api/bootstrap")
      .then((r) => r.json())
      .then((d: { token?: string }) => {
        if (d.token) setAuthToken(d.token);
      })
      .catch(() => {});
  }, []);

  useEffect(() => {
    if (!authToken) return;
    void load();
    const ws = new WebSocket(`ws://${location.hostname}:${location.port || 7777}/ws`);
    ws.onmessage = (ev) => {
      const msg = JSON.parse(ev.data as string) as { type: string; tasks?: Task[]; task?: Task };
      if (msg.type === "board_snapshot" && msg.tasks) setTasks(msg.tasks as Task[]);
      if (msg.type === "task_updated" && msg.task) {
        const updated = msg.task as Task;
        if (updated.status === "archived") {
          setTasks((prev) => prev.filter((t) => t.key !== updated.key));
          setSelected((s) => (s?.key === updated.key ? null : s));
          return;
        }
        setTasks((prev) => {
          const idx = prev.findIndex((t) => t.key === updated.key);
          if (idx >= 0) {
            const next = [...prev];
            next[idx] = updated;
            return next;
          }
          return [updated, ...prev];
        });
      }
      if (msg.type === "board_updated") void load();
    };
    const interval = setInterval(load, 30_000);
    return () => {
      ws.close();
      clearInterval(interval);
    };
  }, [load, authToken]);

  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 8 } }));

  const byStatus = useMemo(() => {
    const map = Object.fromEntries(STATUSES.map((s) => [s, [] as Task[]])) as Record<TaskStatus, Task[]>;
    for (const t of tasks) {
      const status = (t.status === "handoff" || t.status === "backlog" ? "ready" : t.status) as TaskStatus;
      if (map[status]) map[status].push({ ...t, status });
    }
    return map;
  }, [tasks]);

  const onDragEnd = async (event: DragEndEvent) => {
    setActiveId(null);
    const { active, over } = event;
    if (!over) return;
    const taskKey = String(active.id);
    const overId = String(over.id);
    const newStatus = (STATUSES.includes(overId as TaskStatus) ? overId : tasks.find((t) => t.key === overId)?.status) as TaskStatus | undefined;
    if (!newStatus) return;
    const task = tasks.find((t) => t.key === taskKey);
    if (!task || task.status === newStatus) return;
    setTasks((prev) => prev.map((t) => (t.key === taskKey ? { ...t, status: newStatus } : t)));
    await fetch(`/api/tasks/${taskKey}`, {
      method: "PATCH",
      headers: apiHeaders,
      body: JSON.stringify({ status: newStatus }),
    });
  };

  const openTask = async (task: Task) => {
    const res = await fetch(`/api/tasks/${task.key}`, { headers: apiHeaders });
    if (res.ok) setSelected(await res.json());
  };

  const removeTask = async (taskKey: string) => {
    const task = tasks.find((t) => t.key === taskKey);
    if (!task) return;
    if (!window.confirm(`Remove "${task.title}" from the board?`)) return;
    setTasks((prev) => prev.filter((t) => t.key !== taskKey));
    setSelected((s) => (s?.key === taskKey ? null : s));
    await fetch(`/api/tasks/${taskKey}`, {
      method: "PATCH",
      headers: apiHeaders,
      body: JSON.stringify({ status: "archived" }),
    });
  };

  return (
    <div className="h-screen flex flex-col">
      <header className="border-b border-zinc-800 px-4 py-3 flex items-center justify-between gap-3">
        <h1 className="text-lg font-semibold tracking-tight">Agent Swarm Board</h1>
        <div className="flex items-center gap-3">
          <span className="text-xs text-zinc-500">{tasks.length} tasks</span>
          <button
            type="button"
            onClick={toggleConsole}
            className={`flex items-center gap-1.5 text-xs px-2.5 py-1.5 rounded-md border transition-colors ${
              consoleOpen
                ? "bg-zinc-800 border-zinc-600 text-zinc-100"
                : "bg-transparent border-zinc-700 text-zinc-400 hover:text-zinc-200 hover:border-zinc-600"
            }`}
            title={consoleOpen ? "Collapse console" : "Open console"}
          >
            <MessageSquare size={14} />
            Console
          </button>
        </div>
      </header>
      <div className="flex flex-1 min-h-0 min-w-0">
        <DndContext
          sensors={sensors}
          onDragStart={(e) => setActiveId(String(e.active.id))}
          onDragEnd={(e) => void onDragEnd(e)}
        >
          <div className="flex-1 min-h-0 h-full overflow-x-auto overflow-y-hidden swarm-scroll p-4 flex gap-3 items-stretch">
            {STATUSES.map((status) => (
              <Column key={status} status={status} tasks={byStatus[status]} onSelect={(t) => void openTask(t)} onRemove={(key) => void removeTask(key)} />
            ))}
          </div>
          <DragOverlay>
            {activeId ? (
              <div className="max-w-[300px] bg-zinc-800 border border-zinc-600 rounded-lg p-3 opacity-90">
                <p className="text-sm font-medium truncate" title={tasks.find((t) => t.key === activeId)?.title}>
                  {boardTitle(tasks.find((t) => t.key === activeId)?.title ?? "")}
                </p>
              </div>
            ) : null}
          </DragOverlay>
        </DndContext>
        <ConsolePanel authToken={authToken} open={consoleOpen} onToggle={toggleConsole} />
      </div>
      {selected && <TaskDetail task={selected} onClose={() => setSelected(null)} onRemove={(key) => void removeTask(key)} />}
    </div>
  );
}
