import { type ReactNode, createContext, useCallback, useContext, useState } from "react";

export interface ToastInput { message: string; action?: { label: string; onClick: () => void } }

const Ctx = createContext<(t: ToastInput) => void>(() => {});
let nextId = 0;

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<(ToastInput & { id: number })[]>([]);
  const push = useCallback((t: ToastInput) => {
    const id = ++nextId;
    setToasts((x) => [...x, { ...t, id }]);
    setTimeout(() => setToasts((x) => x.filter((y) => y.id !== id)), 6000);
  }, []);
  return (
    <Ctx.Provider value={push}>
      {children}
      <div role="status" aria-live="polite" className="pointer-events-none fixed inset-x-0 bottom-4 z-50 flex flex-col items-center gap-2">
        {toasts.map((t) => (
          <div key={t.id} className="pointer-events-auto flex max-w-md items-center gap-3 rounded-md border border-line bg-panel px-3 py-2 shadow-lg">
            <span>{t.message}</span>
            {t.action && (
              <button type="button" className="font-medium text-accent" onClick={t.action.onClick}>
                {t.action.label}
              </button>
            )}
          </div>
        ))}
      </div>
    </Ctx.Provider>
  );
}

export const useToast = () => useContext(Ctx);
