import { useCallback, useEffect, useState } from "react";
import { readJson, storage, writeJson } from "./local";

const key = (id: string, field: string) => `swarm.draft.${id}.${field}`;

export const readDraft = (id: string, field: string) => readJson<string>(storage("sessionStorage"), key(id, field), "");
export const writeDraft = (id: string, field: string, value: string) => writeJson(storage("sessionStorage"), key(id, field), value);
export function clearDraft(id: string, field: string): void {
  try {
    storage("sessionStorage")?.removeItem(key(id, field));
  } catch {
    // storage unavailable: nothing to clear
  }
}

export function useDraft(id: string, field: string): [string, (v: string) => void, () => void] {
  const [value, setValue] = useState(() => readDraft(id, field));
  useEffect(() => setValue(readDraft(id, field)), [id, field]);
  const set = useCallback(
    (v: string) => {
      setValue(v);
      writeDraft(id, field, v);
    },
    [id, field],
  );
  const clear = useCallback(() => {
    setValue("");
    clearDraft(id, field);
  }, [id, field]);
  return [value, set, clear];
}
