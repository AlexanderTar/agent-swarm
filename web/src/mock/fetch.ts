import type { MockDaemon, MockEvent, StreamSignal } from "./daemon";

const enc = new TextEncoder();
const frame = (e: MockEvent) => enc.encode(`id: ${e.seq}\nevent: ${e.type}\ndata: ${JSON.stringify(e.data)}\n\n`);

export function eventStream(d: MockDaemon, lastEventId: string | undefined): ReadableStream<Uint8Array> {
  let unsubscribe = () => {};
  return new ReadableStream<Uint8Array>({
    start(ctrl) {
      const after = lastEventId === undefined ? d.events.length : Number(lastEventId);
      // F17: an expired cursor (older than what's retained) and a future cursor (newer than
      // anything sent) both get `event: reset`; the client refetches everything either way.
      const expired = lastEventId !== undefined && after < d.retainedFrom - 1;
      const future = lastEventId !== undefined && after > d.events.length;
      if (expired || future) ctrl.enqueue(enc.encode("event: reset\ndata: {}\n\n"));
      else for (const e of d.events) if (e.seq > after) ctrl.enqueue(frame(e));
      unsubscribe = d.subscribe((s: StreamSignal) => {
        if (s === "close") {
          unsubscribe();
          ctrl.close();
        } else ctrl.enqueue(frame(s));
      });
    },
    cancel() {
      unsubscribe();
    },
  });
}

export function mockFetch(d: MockDaemon): typeof fetch {
  return (async (input: RequestInfo | URL, init?: RequestInit) => {
    if (d.offline) throw new TypeError("Failed to fetch");
    const url = new URL(String(input), "http://mock.test");
    const headers = Object.fromEntries(new Headers(init?.headers).entries());
    if (url.pathname === "/api/events") {
      if (headers.authorization !== `Bearer ${d.db.token}`) return new Response(null, { status: 401 });
      return new Response(eventStream(d, headers["last-event-id"]), { status: 200, headers: { "Content-Type": "text/event-stream" } });
    }
    const method = init?.method ?? "GET";
    // F12: a held route (see MockDaemon.hold) delays the response; `handle()` itself is synchronous.
    await d.waitFor(`${method.toUpperCase()} ${url.pathname}`);
    const body = typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
    const r = d.handle({ method, url: url.pathname + url.search, headers, body });
    const payload = r.body === undefined || r.status === 204 ? null : JSON.stringify(r.body);
    return new Response(payload, { status: r.status, headers: { "Content-Type": "application/json" } });
  }) as typeof fetch;
}
