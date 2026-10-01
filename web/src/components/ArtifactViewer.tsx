import { X } from "lucide-react";
import { useRef } from "react";
import { Dialog } from "radix-ui";
import { errorText } from "../api";
import { C } from "../copy";
import { useArtifact } from "../data/queries";
import { Markdown } from "./Markdown";
import { Button } from "./ui/button";
import { Alert } from "./ui/alert";

export function ArtifactViewer(p: { artifactId: string; revision?: number; section?: string; onClose(): void }) {
  const art = useArtifact(p.artifactId, p.revision, p.section);
  const dialog = useRef<HTMLDialogElement>(null);
  const previousFocus = useRef(document.activeElement as HTMLElement | null);
  return (
    <Dialog.Root open onOpenChange={(open) => { if (!open) p.onClose(); }}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-50 bg-black/60" />
        <Dialog.Content asChild aria-describedby={undefined}
          onOpenAutoFocus={(e) => { e.preventDefault(); dialog.current?.focus(); }}
          onCloseAutoFocus={(e) => { e.preventDefault(); previousFocus.current?.focus(); }}>
          <dialog open ref={dialog} tabIndex={-1} aria-label={art.data?.artifact.path ?? p.artifactId} className="fixed inset-4 m-auto flex h-[calc(100dvh-2rem)] w-[calc(100%-2rem)] max-w-5xl flex-col z-50 outline-none rounded-lg border border-border bg-card p-4 text-foreground shadow-2xl">
            <div className="mb-3 flex shrink-0 items-center justify-between gap-2">
              <Dialog.Title asChild><span className="key min-w-0 truncate">{art.data?.artifact.path ?? p.artifactId}</span></Dialog.Title>
              <Button variant="ghost" size="icon" type="button" aria-label="Close" onClick={p.onClose}><X className="size-4" /></Button>
            </div>
            <div data-artifact-content className="min-h-0 overflow-y-auto">
              {/* Standing rule: a failed load gets a message + retry, never a permanent "…" placeholder. */}
              {art.error ? (
                <Alert variant="destructive">
                  {errorText(art.error)}{" "}
                  <Button type="button" variant="link" size="sm" onClick={() => art.reload()}>
                    {C.retry}
                  </Button>
                </Alert>
              ) : art.data ? (
                <Markdown>{art.data.markdown}</Markdown>
              ) : (
                <p className="text-muted-foreground">…</p>
              )}
            </div>
          </dialog>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
