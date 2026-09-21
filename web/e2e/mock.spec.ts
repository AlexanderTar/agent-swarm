import { expect, test } from "@playwright/test";

test.beforeEach(async ({ request }) => {
  await request.get("/__mock/reset");
});

test("hierarchy, filters and view switching keep the selection", async ({ page }) => {
  await page.goto("/#/hierarchy?item=TASK-102");
  await expect(page.getByRole("treeitem", { name: /^EPIC-12 / })).toBeVisible();
  await expect(page.getByTestId("details-panel")).toContainText("Persist session");
  await page.getByRole("radio", { name: "Kanban" }).click();
  await expect(page).toHaveURL(/#\/kanban\?item=TASK-102$/);
  await page.getByRole("searchbox", { name: "Search name or key…" }).fill("session");
  await expect(page.getByText("1 matches")).toBeVisible();
});

async function drag(page: import("@playwright/test").Page, from: string, to: string) {
  const a = await page.getByTestId(from).boundingBox();
  const b = await page.getByTestId(to).boundingBox();
  if (!a || !b) throw new Error("missing boxes");
  await page.mouse.move(a.x + 30, a.y + 12);
  await page.mouse.down();
  await page.mouse.move(a.x + 45, a.y + 20, { steps: 4 });
  await page.mouse.move(b.x + 60, b.y + 40, { steps: 12 });
  return async () => page.mouse.up();
}

test("dragging an allowed card shows Updating… and settles", async ({ page }) => {
  await page.goto("/#/kanban");
  const drop = await drag(page, "card-TASK-103", "cell-EPIC-12-blocked");
  await drop();
  await expect(page.getByTestId("cell-EPIC-12-blocked").getByTestId("card-TASK-103")).toBeVisible();
  await expect(page.getByTestId("card-TASK-103")).not.toContainText("Updating…");
});

test("dragging to a refused column shows the lock, returns the card and toasts", async ({ page }) => {
  await page.goto("/#/kanban");
  const drop = await drag(page, "card-TASK-101", "cell-EPIC-12-ready");
  await expect(page.getByTestId("cell-EPIC-12-ready")).toContainText("Couldn't update status. The item remains In progress.");
  await drop();
  // dnd-kit renders its own `role="status"` live region alongside the app's toast; scope to the toast text.
  await expect(page.getByRole("status").filter({ hasText: "Couldn't update status" })).toContainText(
    "Couldn't update status. The item remains In progress.",
  );
  await expect(page.getByTestId("cell-EPIC-12-in_progress").getByTestId("card-TASK-101")).toBeVisible();
});

test("Move to… works from the keyboard", async ({ page }) => {
  await page.goto("/#/kanban");
  // the card itself picks up "Move to… TASK-110" in its own accessible name (dnd-kit gives it
  // role="button", and the nested Move-to button's aria-label bleeds into name-from-content), so
  // scope to the card to get the actual Move-to button.
  await page.getByTestId("card-TASK-110").getByRole("button", { name: "Move to… TASK-110" }).focus();
  await page.keyboard.press("Enter");
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");
  await expect(page.getByTestId("card-TASK-110")).toHaveCount(0);
  // TASK-110 was the last open item in BUG-7: moving it to Cancelled makes the whole lane
  // "finished", which auto-collapses the lane itself (buildLanes' defaultCollapsed) on top of
  // the Cancelled column's own default collapse.
  await page.getByTestId("lane-BUG-7").getByRole("button").click();
  await page.getByTestId("col-cancelled").click();
  await expect(page.getByTestId("cell-BUG-7-cancelled").getByTestId("card-TASK-110")).toBeVisible();
});

test("the inbox shows a question read-only and approves a section", async ({ page }) => {
  await page.goto("/#/inbox?req=req_question");
  await expect(page.getByRole("textbox")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "CRDT" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Open orchestrator terminal" })).toBeVisible();
  // The question no longer resolves from the board, so the approval is opened directly
  // (it used to be auto-selected once the answered question left the list).
  await page.goto("/#/inbox?req=req_section");
  await page.getByRole("button", { name: /Approve "Data model"/ }).click();
  await expect(page.getByText("A local queue of pending messages.")).toBeVisible();
  await page.getByRole("button", { name: "Approve section" }).click();
  await expect(page.getByText("Already resolved.")).toBeVisible();
});

test("the dependency graph renders and expands a hop", async ({ page }) => {
  await page.goto("/#/dependencies?item=TASK-104");
  await expect(page.getByTestId("node-TASK-98")).toBeVisible();
  await page.getByRole("radio", { name: "Neighbourhood" }).click();
  await expect(page.getByTestId("node-TASK-98")).toHaveCount(0);
  await page.getByRole("button", { name: "Expand one hop" }).click();
  await expect(page.getByTestId("node-TASK-98")).toBeVisible();
});

test("losing the connection shows the banner and disables moves", async ({ page, request }) => {
  await page.goto("/#/kanban");
  // scope to the card, see the note in the keyboard Move-to test above
  const moveTo = page.getByTestId("card-TASK-103").getByRole("button", { name: "Move to… TASK-103" });
  await expect(moveTo).toBeEnabled();
  await request.get("/__mock/disconnect");
  await expect(page.getByRole("alert")).toHaveText(/Connection lost\. Status changes are unavailable\./);
  await expect(moveTo).toBeDisabled();
  await request.get("/__mock/reconnect");
  await page.getByRole("alert").getByRole("button", { name: "Retry" }).click();
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("a new spike is created and selected", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("button", { name: "New spike" }).click();
  await page.getByRole("textbox", { name: "Name" }).fill("Offline sync");
  await page.getByRole("button", { name: /orchestrator$/ }).click();
  await expect(page).toHaveURL(/item=SPIKE-\d+/);
});

test("narrow windows show details instead of the view", async ({ page }) => {
  await page.setViewportSize({ width: 900, height: 800 });
  await page.goto("/#/hierarchy?item=TASK-101");
  await expect(page.getByTestId("view")).toHaveCount(0);
  await page.getByRole("button", { name: "← Back" }).click();
  await expect(page.getByTestId("view")).toBeVisible();
});
