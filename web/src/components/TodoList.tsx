import { C } from "../copy";
import type { Todo, TodoStatus } from "../types";

const ICON: Record<TodoStatus, [string, string]> = {
  completed: ["✓", "text-ok"],
  in_progress: ["▶", "text-accent"],
  pending: ["○", "text-muted"],
};

export function TodoList({ todos, onSelect }: { todos: Todo[]; onSelect(key: string): void }) {
  const done = todos.filter((t) => t.status === "completed").length;
  return (
    <section aria-label={C.progress} className="border-t border-line pt-3">
      <h3 className="mb-1 font-semibold">{`${C.progress} ${done}/${todos.length}`}</h3>
      <ul>
        {todos.map((t) => {
          const [icon, tone] = ICON[t.status];
          const key = t.item_key;
          return (
            <li key={t.id} className="flex gap-2">
              <span aria-hidden="true" className={tone}>{icon}</span>
              {key ? (
                <button type="button" onClick={() => onSelect(key)} className="text-left text-accent">{t.label}</button>
              ) : (
                <span>{t.label}</span>
              )}
            </li>
          );
        })}
      </ul>
    </section>
  );
}
