import { errorText } from "../api";
import { C } from "../copy";
import { useMutation } from "../data/hooks";
import { useDraft } from "../state/drafts";
import type { Request } from "../types";
import { useToast } from "./Toast";

export function QuestionView({ request, connected }: { request: Request; connected: boolean }) {
  const [text, setText, clear] = useDraft(request.id, "answer");
  const toast = useToast();
  const answer = useMutation((api, t: string) => api.answer(request.id, t), ["requests", "items", "item:"]);
  const terminal = useMutation((api, name: string) => api.agentAction(name, "terminal"));
  const options = Array.isArray(request.options) ? request.options : [];
  const send = async (t: string) => {
    try {
      await answer.run(t);
      clear();
    } catch (e) {
      toast({ message: errorText(e) });
    }
  };
  return (
    <div className="space-y-3">
      <p className="whitespace-pre-wrap text-base">{request.prompt}</p>
      {options.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {options.map((o) => (
            <button key={o} type="button" disabled={!connected || answer.pending} onClick={() => void send(o)} className="rounded border border-line px-2 py-1 hover:bg-raised">
              {o}
            </button>
          ))}
        </div>
      )}
      <textarea aria-label={C.answer} rows={4} value={text} onChange={(e) => setText(e.target.value)} className="w-full rounded border border-line bg-canvas px-2 py-1" />
      <div className="flex gap-2">
        <button type="button" disabled={!connected || answer.pending || text.trim() === ""} onClick={() => void send(text)} className="rounded bg-accent px-3 py-1 text-white disabled:opacity-50">
          {C.sendAnswer}
        </button>
        <button
          type="button"
          disabled={!connected || !request.agent_name}
          onClick={() => request.agent_name && void terminal.run(request.agent_name).catch(() => undefined)}
          className="rounded border border-line px-3 py-1"
        >
          {C.openTerminal}
        </button>
      </div>
    </div>
  );
}
