export interface Entry { data: unknown; error: unknown; loading: boolean }
type Loader = () => Promise<unknown>;

export class QueryStore {
  private readonly entries = new Map<string, Entry>();
  private readonly loaders = new Map<string, Loader>();
  private readonly subs = new Map<string, Set<() => void>>();
  private readonly inflight = new Map<string, Promise<void>>();
  private readonly dirty = new Set<string>();

  get(key: string): Entry | undefined {
    return this.entries.get(key);
  }

  subscribe(key: string, fn: () => void): () => void {
    const set = this.subs.get(key) ?? new Set();
    set.add(fn);
    this.subs.set(key, set);
    return () => {
      set.delete(fn);
    };
  }

  fetch(key: string, load: Loader, force = false): Promise<void> {
    this.loaders.set(key, load);
    const running = this.inflight.get(key);
    if (running) {
      if (force) this.dirty.add(key);
      return running;
    }
    const cur = this.entries.get(key);
    if (!force && cur && !cur.loading) return Promise.resolve();
    this.set(key, { data: cur?.data, error: undefined, loading: true });
    const p = load()
      .then(
        (data) => this.set(key, { data, error: undefined, loading: false }),
        (error: unknown) => this.set(key, { data: cur?.data, error, loading: false }),
      )
      .finally(() => {
        this.inflight.delete(key);
        if (this.dirty.delete(key)) {
          // R1 fix: a response fetched before an invalidation must never commit as fresh. If a
          // subscriber is still around once it lands, refetch for them; otherwise drop it outright
          // so a later non-forced `fetch` (e.g. on remount) can't short-circuit on stale data.
          if ((this.subs.get(key)?.size ?? 0) > 0) void this.fetch(key, this.loaders.get(key) ?? load, true);
          else this.entries.delete(key);
        }
      });
    this.inflight.set(key, p);
    return p;
  }

  // "" matches every key; subscribed keys refetch, others are dropped (they'll reload on next subscribe).
  invalidate(prefixes: string[]): void {
    for (const key of [...this.entries.keys()]) {
      if (!prefixes.some((p) => key.startsWith(p))) continue;
      if (this.inflight.has(key)) {
        // R1 fix: an in-flight read was issued before this invalidation, so its landing response
        // must not satisfy a later non-forced fetch. Mark it dirty regardless of subscriber count —
        // `fetch`'s `finally()` re-checks subscribers once it settles and refetches or drops it then.
        // No `loader` guard needed: every key in `entries` was put there by `fetch()`, which sets
        // `loaders.get(key)` before either `entries` or `inflight`, and `loaders` is never cleared —
        // so a key that's in both `entries` (this loop's source) and `inflight` always has one.
        this.dirty.add(key);
        continue;
      }
      const loader = this.loaders.get(key);
      if (loader && (this.subs.get(key)?.size ?? 0) > 0) void this.fetch(key, loader, true);
      else this.entries.delete(key);
    }
  }

  private set(key: string, e: Entry): void {
    this.entries.set(key, e);
    for (const fn of this.subs.get(key) ?? []) fn();
  }
}
