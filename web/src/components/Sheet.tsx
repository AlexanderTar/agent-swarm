import { type ReactNode, useRef } from "react";
import {
  Sheet as UiSheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";

export function Sheet(p: {
  title: string;
  subtitle?: string;
  width?: number;
  modal?: boolean;
  header?: boolean;
  onClose(): void;
  children: ReactNode;
  footer?: ReactNode;
}) {
  const modal = p.modal ?? true;
  const previousFocus = useRef(typeof document === "undefined" ? null : document.activeElement as HTMLElement | null);
  const keepOpen = modal ? undefined : (e: Event) => e.preventDefault();
  return (
    <UiSheet open modal={modal} onOpenChange={(open) => { if (!open) p.onClose(); }}>
      <SheetContent
        side="right"
        overlay={modal}
        aria-label={p.title}
        aria-describedby={undefined}
        style={{ width: `${p.width ?? 420}px` }}
        className="flex w-full max-w-full flex-col gap-0 bg-card p-0 sm:max-w-full"
        onInteractOutside={keepOpen}
        onPointerDownOutside={keepOpen}
        onCloseAutoFocus={(e) => {
          e.preventDefault();
          previousFocus.current?.focus();
        }}
      >
        {p.header === false ? (
          <SheetTitle className="sr-only">{p.title}</SheetTitle>
        ) : (
          <SheetHeader className="border-b border-border px-5 py-4">
            <SheetTitle className="text-[15px] font-semibold">{p.title}</SheetTitle>
            {p.subtitle && <SheetDescription>{p.subtitle}</SheetDescription>}
          </SheetHeader>
        )}
        <div className="flex-1 space-y-4 overflow-y-auto px-5 py-4">{p.children}</div>
        {p.footer && <SheetFooter className="flex-row items-center justify-end gap-2 border-t border-border px-5 py-3">{p.footer}</SheetFooter>}
      </SheetContent>
    </UiSheet>
  );
}
