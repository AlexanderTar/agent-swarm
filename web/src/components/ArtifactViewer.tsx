import { X } from "lucide-react";
import { useEffect, useRef } from "react";
import { errorText } from "../api";
import { C } from "../copy";
import { useArtifact } from "../data/queries";
import { Markdown } from "./Markdown";

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
    <dialog open aria-label={art.data?.artifact.path ?? p.artifactId} className="fixed inset-8 z-50 overflow-auto rounded-lg border border-line bg-panel p-4 text-ink shadow-2xl">
      <div className="mb-3 flex items-center justify-between gap-2">
        <span className="key truncate">{art.data?.artifact.path ?? p.artifactId}</span>
        <button ref={closeButton} type="button" aria-label="Close" onClick={p.onClose}><X className="size-4" /></button>
      </div>
      {/* Standing rule: a failed load gets a message + retry, never a permanent "…" placeholder. */}
      {art.error ? (
        <p className="text-bad">
          {errorText(art.error)}{" "}
          <button type="button" className="text-accent underline" onClick={() => art.reload()}>
            {C.retry}
          </button>
        </p>
      ) : art.data ? (
        <Markdown>{art.data.markdown}</Markdown>
      ) : (
        <p className="text-muted">…</p>
      )}
    </dialog>
  );
}
