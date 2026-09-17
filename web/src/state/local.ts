import { useCallback, useState } from "react";

export function readJson<T>(store: Storage, key: string, fallback: T): T {
  try {
    const raw = store.getItem(key);
    return raw === null ? fallback : (JSON.parse(raw) as T);
  } catch {
    return fallback;
  }
}

export function writeJson(store: Storage, key: string, value: unknown): void {
  try {
    store.setItem(key, JSON.stringify(value));
  } catch {
    // private mode or quota: remembering UI state is best effort
  }
}

export function useLocalSet(key: string, initial: readonly string[]): [Set<string>, (id: string) => void] {
  const [set, setSet] = useState(() => new Set(readJson<string[]>(localStorage, key, [...initial])));
  const toggle = useCallback(
    (id: string) =>
      setSet((prev) => {
        const next = new Set(prev);
        if (next.has(id)) next.delete(id);
        else next.add(id);
        writeJson(localStorage, key, [...next]);
        return next;
      }),
    [key],
  );
  return [set, toggle];
}
