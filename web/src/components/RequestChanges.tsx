import { useState } from "react";
import { errorText } from "../api";
import { C } from "../copy";
import { useMutation } from "../data/hooks";
import { useDraft } from "../state/drafts";

export function RequestChanges({ requestId, connected }: { requestId: string; connected: boolean }) {
  const [open, setOpen] = useState(false);
  const [comment, setComment, clear] = useDraft(requestId, "changes");
  const [error, setError] = useState("");
  const send = useMutation((api, c: string) => api.requestChanges(requestId, c), ["requests", "items", "item:"]);
  const submit = async () => {
    if (comment.trim() === "") {
      setError(C.emptyChange);
      return;
    }
    setError("");
    try {
      await send.run(comment);
      clear();
      setOpen(false);
    } catch (e) {
      setError(errorText(e));
    }
  };
  if (!open) {
    return (
      <button type="button" disabled={!connected} onClick={() => setOpen(true)} className="rounded border border-line px-3 py-1">
        {C.requestChanges}
      </button>
    );
  }
  return (
    <div className="w-full space-y-2">
      <textarea aria-label={C.comment} rows={3} maxLength={2000} value={comment} onChange={(e) => setComment(e.target.value)} className="w-full rounded border border-line bg-canvas px-2 py-1" />
      {error && <p className="text-bad">{error}</p>}
      <button type="button" disabled={!connected || send.pending} onClick={() => void submit()} className="rounded border border-line px-3 py-1">
        {C.sendChangeRequest}
      </button>
    </div>
  );
}
