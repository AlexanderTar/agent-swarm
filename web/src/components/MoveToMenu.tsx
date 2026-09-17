import { Lock } from "lucide-react";
import { type KeyboardEvent, useEffect, useRef, useState } from "react";
import { C } from "../copy";
import { type MoveCheck, type Movable, moveOptions } from "../logic/transitions";
import type { ItemStatus } from "../types";

export function MoveToMenu(p: {
  item: Movable;
  onMove(status: ItemStatus, check: MoveCheck): void;
  disabled?: boolean;
  buttonLabel?: string;
  ariaLabel?: string;
}) {
  const [open, setOpen] = useState(false);
  const menu = useRef<HTMLDivElement>(null);
  const options = moveOptions(p.item);
  const enabled = (c: MoveCheck) => c.ok || c.special !== undefined;

  const focusAt = (idx: number) => {
    const items = [...(menu.current?.querySelectorAll<HTMLButtonElement>("button:not([disabled])") ?? [])];
    items[(idx + items.length) % items.length]?.focus();
  };
  useEffect(() => {
    if (open) focusAt(0);
  }, [open]);
  const onKeyDown = (e: KeyboardEvent) => {
    const items = [...(menu.current?.querySelectorAll<HTMLButtonElement>("button:not([disabled])") ?? [])];
    const cur = items.indexOf(document.activeElement as HTMLButtonElement);
    if (e.key === "ArrowDown") focusAt(cur + 1);
    else if (e.key === "ArrowUp") focusAt(cur - 1);
    else if (e.key === "Home") focusAt(0);
    else if (e.key === "End") focusAt(items.length - 1);
    else if (e.key === "Escape") setOpen(false);
    else return;
    e.preventDefault();
    e.stopPropagation();
  };

  return (
    <div className="relative inline-block">
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={p.ariaLabel}
        disabled={p.disabled}
        onClick={() => setOpen((o) => !o)}
        className="rounded border border-line px-2 py-0.5 hover:bg-raised disabled:opacity-50"
      >
        {p.buttonLabel ?? C.moveTo}
      </button>
      {open && (
        <div ref={menu} role="menu" tabIndex={-1} onKeyDown={onKeyDown} className="absolute right-0 z-30 mt-1 w-72 rounded-md border border-line bg-panel p-1 shadow-lg">
          {options.map((o) => (
            <button
              key={o.status}
              type="button"
              role="menuitem"
              disabled={!enabled(o.check)}
              onClick={() => {
                setOpen(false);
                p.onMove(o.status, o.check);
              }}
              className="flex w-full flex-col items-start rounded px-2 py-1 text-left hover:bg-raised disabled:cursor-not-allowed disabled:text-muted"
            >
              <span className="flex items-center gap-1">
                {!enabled(o.check) && <Lock aria-hidden className="size-3" />}
                {o.label}
              </span>
              {!o.check.ok && <span className="text-[12px] text-muted">{o.check.reason}</span>}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
