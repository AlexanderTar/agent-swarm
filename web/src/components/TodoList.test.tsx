import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { Todo } from "../types";
import { TodoList } from "./TodoList";

const todos: Todo[] = [
  { id: "TASK-10", label: "TASK-10 · Schema migration", status: "completed", item_key: "TASK-10" },
  { id: "TASK-12", label: "TASK-12 · Add login form", status: "in_progress", item_key: "TASK-12" },
  { id: "integrate", label: "Merging and verifying", status: "pending" },
];

describe("TodoList", () => {
  it("renders the heading count, icons and task links", async () => {
    const onSelect = vi.fn();
    render(<TodoList todos={todos} onSelect={onSelect} />);
    expect(screen.getByRole("heading", { name: "Progress 1/3" })).toBeInTheDocument();
    const items = screen.getAllByRole("listitem").map((li) => li.textContent);
    expect(items).toEqual([
      "✓Completed: TASK-10 · Schema migration",
      "▶In progress: TASK-12 · Add login form",
      "○Pending: Merging and verifying",
    ]);
    for (const label of ["Completed:", "In progress:", "Pending:"]) {
      expect(screen.getByText(label)).toHaveClass("sr-only");
    }
    expect(screen.queryByRole("button", { name: "Merging and verifying" })).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "TASK-12 · Add login form" }));
    expect(onSelect).toHaveBeenCalledWith("TASK-12");
  });

  it("lets a long task label wrap inside a narrow Details sheet", () => {
    const label = "TASK-12 · Make the repository chooser work in a narrow mobile sheet";
    render(<TodoList todos={[{ ...todos[1]!, label }]} onSelect={vi.fn()} />);
    const task = screen.getByRole("button", { name: label });
    expect(task).toHaveClass("min-w-0", "flex-1", "whitespace-normal", "break-words");
    expect(task).not.toHaveClass("shrink-0", "whitespace-nowrap");
    expect(task.closest("li")).toHaveClass("min-w-0");
  });
});
