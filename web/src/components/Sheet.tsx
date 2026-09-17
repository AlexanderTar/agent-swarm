import { X } from "lucide-react";
import { type ReactNode, useEffect } from "react";

export function Sheet(p: { title: string; subtitle?: string; width?: number; onClose(): void; children: ReactNode; footer?: ReactNode }) {
  const { onClose } = p;
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <aside
      role="dialog"
      aria-label={p.title}
      style={{ width: `${p.width ?? 420}px` }}
      className="fixed inset-y-0 right-0 z-40 flex max-w-full flex-col border-l border-line bg-panel shadow-xl"
    >
      <header className="flex items-start justify-between gap-2 border-b border-line p-4">
        <div>
          <h2 className="font-semibold">{p.title}</h2>
          {p.subtitle && <p className="text-muted">{p.subtitle}</p>}
        </div>
        <button type="button" aria-label="Close" onClick={onClose} className="rounded p-1 hover:bg-raised">
          <X className="size-4" />
        </button>
      </header>
      <div className="flex-1 space-y-4 overflow-y-auto p-4">{p.children}</div>
      {p.footer && <footer className="flex justify-end gap-2 border-t border-line p-4">{p.footer}</footer>}
    </aside>
  );
}
