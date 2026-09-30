import { C } from "../copy";
import type { Todo, TodoStatus } from "../types";
import { Button } from "./ui/button";

const ICON: Record<TodoStatus, [string, string]> = {
  completed: ["✓", "text-success"],
  in_progress: ["▶", "text-link"],
  pending: ["○", "text-muted-foreground"],
};

export function TodoList({ todos, onSelect }: { todos: Todo[]; onSelect(key: string): void }) {
  const done = todos.filter((t) => t.status === "completed").length;
  return (
    <section aria-label={C.progress} className="border-t border-border pt-3">
      <h3 className="mb-1 font-semibold">{`${C.progress} ${done}/${todos.length}`}</h3>
      <ul>
        {todos.map((t) => {
          const [icon, tone] = ICON[t.status];
          const key = t.item_key;
          return (
            <li key={t.id} className="flex gap-2">
              <span aria-hidden="true" className={tone}>{icon}</span>
              {key ? (
                <Button type="button" variant="link" onClick={() => onSelect(key)} className="h-auto p-0 text-left">{t.label}</Button>
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
