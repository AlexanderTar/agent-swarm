import { expect, test } from "@playwright/test";

test("the embedded board loads and shows seeded work", async ({ page }) => {
  await page.goto("/#/hierarchy");
  await expect(page.getByRole("heading", { name: "Agent Swarm" })).toBeVisible();
  await expect(page.getByRole("treeitem", { name: /^EPIC-12 / })).toBeVisible();
  await expect(page.getByRole("button", { name: /^Needs you \d+$/ })).toBeVisible();
});

test("kanban moves a card through the real daemon", async ({ page }) => {
  await page.goto("/#/kanban");
  // The draggable card itself is role=button too (dnd-kit), and its accessible name
  // absorbs this nested button's text, so the query must be scoped by testid (see mock.spec.ts).
  await page.getByTestId("card-TASK-103").getByRole("button", { name: "Move to… TASK-103" }).click();
  await page.getByRole("menuitem", { name: /^Blocked/ }).click();
  await expect(page.getByTestId("cell-EPIC-12-blocked").getByTestId("card-TASK-103")).toBeVisible();
});

test("a blocked item's lock reason comes from the daemon's status_before_block, and unblocking it round-trips", async ({ page }) => {
  // Leaving Blocked is only allowed to the item's saved status (§10.1). checkMove (Task 32) mirrors
  // internal/items/transition.go's check() exactly using Item.status_before_block (a real field the
  // daemon returns, per D-2/A4), so every other target stays locked with the daemon's own reason —
  // there is no live scenario where this menu lets a request through that the daemon then refuses.
  await page.goto("/#/kanban");
  await page.getByTestId("card-TASK-102").getByRole("button", { name: "Move to… TASK-102" }).click();
  const inReview = page.getByRole("menuitem", { name: /^In review/ });
  await expect(inReview).toBeDisabled();
  await expect(inReview).toContainText("Couldn't update status. The item remains Blocked.");
  await page.getByRole("menuitem", { name: /^In progress/ }).click();
  await expect(page.getByTestId("cell-EPIC-12-in_progress").getByTestId("card-TASK-102")).toBeVisible();
});

test("the inbox shows a section snapshot and approves it", async ({ page }) => {
  await page.goto("/#/inbox?filter=approvals");
  await page.getByRole("button", { name: /Approve "/ }).first().click();
  await expect(page.getByText(/Spec revision \d+ · Section/)).toBeVisible();
  await page.getByRole("button", { name: "Approve section" }).click();
  await expect(page.getByText("Already resolved.")).toBeVisible();
});

test("SSE updates the board after a change from another client", async ({ page, request }) => {
  await page.goto("/#/kanban");
  const token = ((await (await request.get("/api/bootstrap")).json()) as { token: string }).token;
  const item = await (await request.get("/api/items/TASK-110", { headers: { Authorization: `Bearer ${token}` } })).json();
  await request.patch("/api/items/TASK-110", {
    headers: { Authorization: `Bearer ${token}` },
    data: { status: "blocked", revision: item.item.revision },
  });
  await expect(page.getByTestId("cell-BUG-7-blocked").getByTestId("card-TASK-110")).toBeVisible();
});

test("dependencies render from the real graph endpoint", async ({ page }) => {
  await page.goto("/#/dependencies?item=TASK-102");
  await expect(page.getByTestId("node-TASK-104")).toBeVisible();
});
