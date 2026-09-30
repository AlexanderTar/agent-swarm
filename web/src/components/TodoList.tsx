import { C } from "../copy";
import type { Todo, TodoStatus } from "../types";
import { Button } from "./ui/button";

const ICON: Record<TodoStatus, [string, string]> = {
  completed: ["✓", "text-success"],
  in_progress: ["▶", "text-link"],
  pending: ["○", "text-muted-foreground"],
};

const STATUS: Record<TodoStatus, string> = {
  completed: "Completed",
  in_progress: "In progress",
  pending: "Pending",
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
            <li key={t.id} className="flex min-w-0 items-start gap-2">
              <span aria-hidden="true" className={`${tone} shrink-0`}>{icon}</span>
              <span className="sr-only">{STATUS[t.status]}: </span>
              {key ? (
                <Button type="button" variant="link" onClick={() => onSelect(key)} className="h-auto min-w-0 flex-1 shrink justify-start whitespace-normal break-words p-0 text-left">{t.label}</Button>
              ) : (
                <span className="min-w-0 break-words">{t.label}</span>
              )}
            </li>
          );
        })}
      </ul>
    </section>
  );
}
