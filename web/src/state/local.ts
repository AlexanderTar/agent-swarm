import { useCallback, useState } from "react";

// The window.localStorage getter itself throws SecurityError when site data is blocked.
export function storage(kind: "localStorage" | "sessionStorage"): Storage | null {
  try {
    return window[kind];
  } catch {
    return null;
  }
}

export function readJson<T>(store: Storage | null, key: string, fallback: T): T {
  try {
    const raw = store?.getItem(key) ?? null;
    return raw === null ? fallback : (JSON.parse(raw) as T);
  } catch {
    return fallback;
  }
}

export function writeJson(store: Storage | null, key: string, value: unknown): void {
  try {
    store?.setItem(key, JSON.stringify(value));
  } catch {
    // private mode or quota: remembering UI state is best effort
  }
}

export function useLocalSet(key: string, initial: readonly string[]): [Set<string>, (id: string) => void] {
  const [set, setSet] = useState(() => new Set(readJson<string[]>(storage("localStorage"), key, [...initial])));
  const toggle = useCallback(
    (id: string) =>
      setSet((prev) => {
        const next = new Set(prev);
        if (next.has(id)) next.delete(id);
        else next.add(id);
        writeJson(storage("localStorage"), key, [...next]);
        return next;
      }),
    [key],
  );
  return [set, toggle];
}
