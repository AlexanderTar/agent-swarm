import { describe, expect, it, vi } from "vitest";
import { connectEvents, createSseParser } from "./sse";
import type { BoardEvent, ConnState } from "./types";

describe("createSseParser", () => {
  it("parses events split across chunks", () => {
    const parse = createSseParser();
    expect(parse("id: 1\nevent: item.changed\nda")).toEqual([]);
    expect(parse('ta: {"key":"TASK-1"}\n\n')).toEqual([{ id: "1", event: "item.changed", data: '{"key":"TASK-1"}' }]);
  });

  it("joins multi-line data, skips comments, strips CR and keeps the last id", () => {
    const parse = createSseParser();
    const out = parse(": ping\r\n\r\nid: 5\r\ndata: a\r\ndata: b\r\n\r\ndata: c\n\n");
    expect(out).toEqual([
      { id: "5", event: "message", data: "a\nb" },
      { id: "5", event: "message", data: "c" },
    ]);
  });

  it("dispatches an event with no data", () => {
    expect(createSseParser()("event: reset\n\n")).toEqual([{ id: null, event: "reset", data: "" }]);
  });
});

function streamResponse(status = 200) {
  let ctrl!: ReadableStreamDefaultController<Uint8Array>;
  const body = new ReadableStream<Uint8Array>({ start(c) { ctrl = c; } });
  const enc = new TextEncoder();
  return {
    res: new Response(body, { status, headers: { "Content-Type": "text/event-stream" } }),
    push: (s: string) => ctrl.enqueue(enc.encode(s)),
    end: () => ctrl.close(),
  };
}

function harness(backoffMs: number[] = [0]) {
  const streams: ReturnType<typeof streamResponse>[] = [];
  const requests: Record<string, string>[] = [];
  const fetchFn = vi.fn(async (_url: RequestInfo | URL, init?: RequestInit) => {
    requests.push(init?.headers as Record<string, string>);
    const s = streamResponse();
    streams.push(s);
    return s.res;
  }) as unknown as typeof fetch;
  const events: BoardEvent[] = [];
  const states: ConnState[] = [];
  const handle = connectEvents({
    url: "/api/events",
    headers: async () => ({ Authorization: "Bearer t" }),
    fetchFn,
    onEvent: (e) => events.push(e),
    onState: (s) => states.push(s),
    backoffMs,
  });
  return { streams, requests, events, states, handle };
}

describe("connectEvents", () => {
  it("opens, sends auth and delivers parsed events", async () => {
    const h = harness();
    await vi.waitFor(() => expect(h.states).toEqual(["connecting", "open"]));
    expect(h.requests[0]).toMatchObject({ Authorization: "Bearer t", Accept: "text/event-stream" });
    expect(h.requests[0]).not.toHaveProperty("Last-Event-ID");
    h.streams[0]?.push('id: 7\nevent: agent.changed\ndata: {"name":"a","root_key":"EPIC-1"}\n\n');
    await vi.waitFor(() => expect(h.events).toEqual([{ seq: 7, type: "agent.changed", data: { name: "a", root_key: "EPIC-1" } }]));
    h.handle.close();
  });

  it("reconnects with Last-Event-ID after the stream ends", async () => {
    const h = harness();
    await vi.waitFor(() => expect(h.streams).toHaveLength(1));
    h.streams[0]?.push("id: 42\nevent: item.changed\ndata: {}\n\n");
    await vi.waitFor(() => expect(h.events).toHaveLength(1));
    h.streams[0]?.end();
    await vi.waitFor(() => expect(h.streams).toHaveLength(2));
    expect(h.states).toContain("closed");
    expect(h.requests[1]?.["Last-Event-ID"]).toBe("42");
    expect(h.handle.lastEventId()).toBe("42");
    h.handle.close();
  });

  it("passes reset through", async () => {
    const h = harness();
    await vi.waitFor(() => expect(h.streams).toHaveLength(1));
    h.streams[0]?.push("event: reset\ndata: {}\n\n");
    await vi.waitFor(() => expect(h.events[0]?.type).toBe("reset"));
    h.handle.close();
  });

  it("treats an HTTP error as closed and retries after the backoff", async () => {
    let n = 0;
    const states: ConnState[] = [];
    const fetchFn = vi.fn(async () => (++n === 1 ? new Response("no", { status: 503 }) : streamResponse().res)) as unknown as typeof fetch;
    const handle = connectEvents({ url: "/e", headers: async () => ({}), fetchFn, onEvent: () => {}, onState: (s) => states.push(s), backoffMs: [0] });
    await vi.waitFor(() => expect(states).toEqual(["connecting", "closed", "open"]));
    handle.close();
  });

  it("retryNow skips the backoff wait; close stops everything", async () => {
    let n = 0;
    const fetchFn = vi.fn(async () => {
      n++;
      return new Response("no", { status: 503 });
    }) as unknown as typeof fetch;
    const handle = connectEvents({ url: "/e", headers: async () => ({}), fetchFn, onEvent: () => {}, onState: () => {}, backoffMs: [60_000] });
    await vi.waitFor(() => expect(n).toBe(1));
    handle.retryNow();
    await vi.waitFor(() => expect(n).toBe(2));
    handle.close();
    handle.retryNow();
    await new Promise((r) => setTimeout(r, 10));
    expect(n).toBe(2);
  });
});
