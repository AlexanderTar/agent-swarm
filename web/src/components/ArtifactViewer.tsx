import { X } from "lucide-react";
import { useEffect, useRef } from "react";
import { errorText } from "../api";
import { C } from "../copy";
import { useArtifact } from "../data/queries";
import { Markdown } from "./Markdown";
import { Button } from "./ui/button";
import { Alert } from "./ui/alert";

export function ArtifactViewer(p: { artifactId: string; revision?: number; section?: string; onClose(): void }) {
  const art = useArtifact(p.artifactId, p.revision, p.section);
  const closeButton = useRef<HTMLButtonElement>(null);
  // Standing rule: transient UI takes focus on open and restores it to its trigger on close — the
  // same pattern Sheet.tsx uses (T15 fix round), applied here since the brief's dialog didn't have it.
  useEffect(() => {
    const previouslyFocused = document.activeElement as HTMLElement | null;
    closeButton.current?.focus();
    return () => previouslyFocused?.focus();
  }, []);
  return (
    <dialog open aria-label={art.data?.artifact.path ?? p.artifactId} className="fixed inset-8 z-50 overflow-auto rounded-lg border border-border bg-card p-4 text-foreground shadow-2xl">
      <div className="mb-3 flex items-center justify-between gap-2">
        <span className="key truncate">{art.data?.artifact.path ?? p.artifactId}</span>
        <Button ref={closeButton} variant="ghost" size="icon" type="button" aria-label="Close" onClick={p.onClose}><X className="size-4" /></Button>
      </div>
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
    </dialog>
  );
}
