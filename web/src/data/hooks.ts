import {
  type ReactElement, type ReactNode, createContext, createElement, useCallback, useContext, useEffect, useMemo, useRef,
  useState, useSyncExternalStore,
} from "react";
import type { Api } from "../api";
import { type EventsHandle, connectEvents } from "../sse";
import type { ConnState } from "../types";
import { keysForEvent } from "./invalidate";
import { QueryStore } from "./store";

interface DataCtx { api: Api; store: QueryStore; conn: ConnState; retry: () => void }
const Ctx = createContext<DataCtx | null>(null);

function useData(): DataCtx {
  const c = useContext(Ctx);
  if (!c) throw new Error("DataProvider missing");
  return c;
}

export function DataProvider(p: { api: Api; children: ReactNode; events?: boolean; backoffMs?: readonly number[] }): ReactElement {
  const { api, events = true, backoffMs } = p;
  const store = useMemo(() => new QueryStore(), []);
  const [conn, setConn] = useState<ConnState>(events ? "connecting" : "open");
  const handle = useRef<EventsHandle | null>(null);
  useEffect(() => {
    if (!events) return;
    const h = connectEvents({
      url: "/api/events",
      headers: api.authHeaders,
      backoffMs,
      // contracts §7.1/onEvent contract: this must never throw — a throw aborts the SSE read loop
      // and replays the same event on reconnect (see sse.ts). keysForEvent is a pure switch and
      // store.invalidate only touches in-memory maps, so nothing here is expected to throw; the
      // try/catch is a backstop, not a code path this codebase exercises.
      onEvent: (e) => {
        try {
          store.invalidate(keysForEvent(e));
        } catch (err) {
          console.error("onEvent:", err);
        }
      },
      onState: (s) => {
        setConn(s);
        // F17: the contract wants REST-then-subscribe; subscribing at the same time as the first
        // load can miss an event fired in between. Invalidating everything on every `open` (first
        // connect and every reconnect) covers that gap — subscribed queries pay one extra refetch.
        if (s === "open") store.invalidate([""]);
      },
    });
    handle.current = h;
    return () => h.close();
  }, [api, store, events, backoffMs]);
  const retry = useCallback(() => handle.current?.retryNow(), []);
  const value = useMemo(() => ({ api, store, conn, retry }), [api, store, conn, retry]);
  return createElement(Ctx.Provider, { value }, p.children);
}

export const useApi = () => useData().api;

export function useConnection() {
  const { conn, retry } = useData();
  return { state: conn, connected: conn !== "closed", retry };
}

export function useInvalidate(): (prefixes: string[]) => void {
  const { store } = useData();
  return useCallback((prefixes: string[]) => store.invalidate(prefixes), [store]);
}

export interface QueryResult<T> { data: T | undefined; error: unknown; loading: boolean; reload(): void }

const noop = () => () => {};

export function useQuery<T>(key: string | null, load: (api: Api) => Promise<T>): QueryResult<T> {
  const k = key || null; // "" means "no key" too
  const { api, store } = useData();
  const loadRef = useRef(load);
  loadRef.current = load;
  const subscribe = useCallback((fn: () => void) => (k ? store.subscribe(k, fn) : noop()), [store, k]);
  const entry = useSyncExternalStore(subscribe, () => (k ? store.get(k) : undefined));
  useEffect(() => {
    if (k) void store.fetch(k, () => loadRef.current(api));
  }, [k, store, api]);
  const reload = useCallback(() => {
    if (k) void store.fetch(k, () => loadRef.current(api), true);
  }, [k, store, api]);
  return {
    data: entry?.data as T | undefined,
    error: entry?.error,
    loading: k !== null && (entry?.loading ?? true),
    reload,
  };
}

export interface Mutation<A extends unknown[], R> { run(...a: A): Promise<R>; pending: boolean; error: unknown; reset(): void }

export function useMutation<A extends unknown[], R>(
  fn: (api: Api, ...a: A) => Promise<R>,
  invalidates: string[] = [],
): Mutation<A, R> {
  const { api, store } = useData();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>(undefined);
  const fnRef = useRef(fn);
  fnRef.current = fn;
  // Callers pass an inline array literal (`useMutation(f, [qk.items])`), a fresh reference on every
  // render. A ref, mirroring fnRef, keeps `run`'s identity stable across those renders without
  // needing to serialize the array into a dependency string.
  const invRef = useRef(invalidates);
  invRef.current = invalidates;
  const run = useCallback(
    async (...a: A) => {
      setPending(true);
      setError(undefined);
      try {
        const r = await fnRef.current(api, ...a);
        store.invalidate(invRef.current);
        return r;
      } catch (e) {
        setError(e);
        throw e;
      } finally {
        setPending(false);
      }
    },
    [api, store],
  );
  const reset = useCallback(() => setError(undefined), []);
  return { run, pending, error, reset };
}
