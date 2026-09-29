import type { CSSProperties, ReactNode } from "react";
import { Toaster as SonnerToaster, toast as sonner } from "sonner";

export interface ToastInput { message: string; action?: { label: string; onClick: () => void } }

export type ToastFn = ((t: ToastInput) => void) & {
  success(message: string, description?: string): void;
  error(message: string, description?: string): void;
};

const SUCCESS_MS = 4000;
const ERROR_MS = 6000;

// Legacy `toast({ message })` call sites are failure or refusal messages.
const toastFn: ToastFn = Object.assign(
  (t: ToastInput) => {
    sonner.error(t.message, { duration: ERROR_MS, action: t.action && { label: t.action.label, onClick: t.action.onClick } });
  },
  {
    success: (message: string, description?: string) => { sonner.success(message, { description, duration: SUCCESS_MS }); },
    error: (message: string, description?: string) => { sonner.error(message, { description, duration: ERROR_MS }); },
  },
);

export const useToast = (): ToastFn => toastFn;

export function Toaster() {
  return (
    <SonnerToaster
      theme="dark"
      position="bottom-left"
      visibleToasts={3}
      toastOptions={{
        style: { "--normal-bg": "var(--popover)" } as CSSProperties,
        classNames: {
          toast: "bg-popover text-popover-foreground border border-border shadow-lg font-sans text-[13px]",
          description: "text-muted-foreground",
          actionButton: "!bg-transparent !text-link font-medium",
          success: "[&_[data-icon]]:text-success",
          error: "[&_[data-icon]]:text-destructive",
        },
      }}
    />
  );
}

export function ToastProvider({ children }: { children: ReactNode }) {
  return (
    <>
      {children}
      <Toaster />
    </>
  );
}
