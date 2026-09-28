import { useState } from "react";
import { errorText } from "../api";
import { C, T } from "../copy";
import { useMutation } from "../data/hooks";
import { useDraft } from "../state/drafts";
import { useToast } from "./Toast";
import { Button } from "./ui/button";
import { Textarea } from "./ui/textarea";

export function RequestChanges({ requestId, agentName, connected }: { requestId: string; agentName: string; connected: boolean }) {
  const toast = useToast();
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
      toast.success(T.toastChangesSent(agentName));
      clear();
      setOpen(false);
    } catch (e) {
      setError(errorText(e));
    }
  };
  if (!open) {
    return (
      <Button type="button" variant="outline" size="sm" disabled={!connected} onClick={() => setOpen(true)}>
        {C.requestChanges}
      </Button>
    );
  }
  return (
    <div className="w-full space-y-2">
      <Textarea aria-label={C.comment} rows={3} maxLength={2000} value={comment} onChange={(e) => setComment(e.target.value)} />
      {error && <p className="text-destructive">{error}</p>}
      <Button type="button" variant="outline" size="sm" disabled={!connected || send.pending} onClick={() => void submit()}>
        {C.sendChangeRequest}
      </Button>
    </div>
  );
}
