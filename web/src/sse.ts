import type { BoardEvent, ConnState } from "./types";

export interface SseMessage { id: string | null; event: string; data: string }

export function createSseParser(): (chunk: string) => SseMessage[] {
  let buf = "";
  let id: string | null = null;
  let event = "";
  let data: string[] = [];
  return (chunk) => {
    buf += chunk;
    const lines = buf.split("\n");
    buf = lines.pop() ?? "";
    const out: SseMessage[] = [];
    for (const rawLine of lines) {
      const line = rawLine.endsWith("\r") ? rawLine.slice(0, -1) : rawLine;
      if (line === "") {
        if (data.length > 0 || event !== "") out.push({ id, event: event || "message", data: data.join("\n") });
        event = "";
        data = [];
        continue;
      }
      if (line.startsWith(":")) continue;
      const i = line.indexOf(":");
      const field = i < 0 ? line : line.slice(0, i);
      let value = i < 0 ? "" : line.slice(i + 1);
      if (value.startsWith(" ")) value = value.slice(1);
      if (field === "data") data.push(value);
      else if (field === "event") event = value;
      else if (field === "id") id = value;
    }
    return out;
  };
}

function parseData(s: string): unknown {
  if (s === "") return null;
  try {
    return JSON.parse(s);
  } catch {
    return s;
  }
}

export const DEFAULT_BACKOFF: readonly number[] = [1000, 2000, 5000, 10000, 15000];

export interface EventsOptions {
  url: string;
  headers: () => Promise<Record<string, string>>;
  fetchFn?: typeof fetch;
  onEvent: (e: BoardEvent) => void;
  onState: (s: ConnState) => void;
  backoffMs?: readonly number[];
}

export interface EventsHandle { close(): void; retryNow(): void; lastEventId(): string | null }

export function connectEvents(o: EventsOptions): EventsHandle {
  const fetchFn = o.fetchFn ?? ((...a: Parameters<typeof fetch>) => fetch(...a));
  const backoff = o.backoffMs ?? DEFAULT_BACKOFF;
  let lastId: string | null = null;
  let failures = 0;
  let closed = false;
  let running = false;
  let ctrl: AbortController | null = null;
  let timer: ReturnType<typeof setTimeout> | null = null;

  async function run(): Promise<void> {
    timer = null;
    if (closed || running) return;
    running = true;
    ctrl = new AbortController();
    try {
      const headers: Record<string, string> = { ...(await o.headers()), Accept: "text/event-stream" };
      if (lastId !== null) headers["Last-Event-ID"] = lastId;
      const res = await fetchFn(o.url, { headers, signal: ctrl.signal });
      if (!res.ok || !res.body) throw new Error(`events: HTTP ${res.status}`);
      failures = 0;
      o.onState("open");
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      const parse = createSseParser();
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        for (const m of parse(decoder.decode(value, { stream: true }))) {
          if (m.id !== null) lastId = m.id;
          o.onEvent({ seq: m.id === null ? null : Number(m.id), type: m.event, data: parseData(m.data) });
        }
      }
    } catch {
      // any failure falls through to the reconnect below
    } finally {
      running = false;
    }
    if (closed) return;
    o.onState("closed");
    timer = setTimeout(run, backoff[Math.min(failures++, backoff.length - 1)]);
  }

  o.onState("connecting");
  void run();
  return {
    close() {
      closed = true;
      ctrl?.abort();
      if (timer !== null) clearTimeout(timer);
    },
    retryNow() {
      if (timer === null || closed) return;
      clearTimeout(timer);
      void run();
    },
    lastEventId: () => lastId,
  };
}
