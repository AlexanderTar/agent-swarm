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
    expect(items).toEqual(["✓TASK-10 · Schema migration", "▶TASK-12 · Add login form", "○Merging and verifying"]);
    expect(screen.queryByRole("button", { name: "Merging and verifying" })).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "TASK-12 · Add login form" }));
    expect(onSelect).toHaveBeenCalledWith("TASK-12");
  });
});
