import { expect, test } from "@playwright/test";

test.beforeEach(async ({ request }) => {
  await request.get("/__mock/reset");
});

test("Details and form sheets use their separate narrow breakpoints", async ({ page }) => {
  for (const width of [1000, 800, 600]) {
    await page.setViewportSize({ width, height: 800 });
    await page.goto("/#/hierarchy?item=TASK-102");
    const details = page.getByRole("dialog", { name: "TASK-102" });
    await expect(details).toBeVisible();
    expect(Math.round((await details.boundingBox())?.width ?? 0)).toBe(width);

    await page.goto("/");
    await page.getByRole("button", { name: "New item" }).click();
    await page.getByRole("menuitem", { name: "Task" }).click();
    const form = page.getByRole("dialog", { name: "New item" });
    await expect(form).toBeVisible();
    expect(Math.round((await form.boundingBox())?.width ?? 0)).toBe(width < 640 ? width : 480);
  }
});

test("form sheet controls fit a narrow viewport", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/");
  const newItem = await page.getByRole("button", { name: "New item" }).boundingBox();
  expect(newItem && newItem.x + newItem.width).toBeLessThanOrEqual(390);
  await page.getByRole("button", { name: "New orchestrator" }).click();
  const sheet = page.getByRole("dialog", { name: "New orchestrator" });
  await page.screenshot({ animations: "disabled" });
  const model = sheet.getByRole("combobox", { name: "Model", exact: true });
  const modelBox = await model.boundingBox();
  const rescanBox = await sheet.getByRole("button", { name: "Rescan" }).boundingBox();
  expect(modelBox?.width).toBeGreaterThanOrEqual(160);
  expect(rescanBox && rescanBox.x + rescanBox.width).toBeLessThanOrEqual(390);
});

test("foundation controls keep dark palette, contrast and density under a light OS", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "light" });
  await page.goto("/");
  await expect(page.locator("body")).toHaveCSS("background-color", "rgb(11, 11, 14)");
  await expect(page.locator("body")).toHaveCSS("font-family", /^"?Onest Variable"?,/);
  await page.addScriptTag({
    type: "module",
    content: `
      const [React, ReactDOM, button, badge, input, select, tabs] = await Promise.all([
        import('/node_modules/.vite/deps/react.js'),
        import('/node_modules/.vite/deps/react-dom_client.js'),
        import('/src/components/ui/button.tsx'),
        import('/src/components/ui/badge.tsx'),
        import('/src/components/ui/input.tsx'),
        import('/src/components/ui/select.tsx'),
        import('/src/components/ui/tabs.tsx'),
      ]);
      const e = React.createElement ?? React.default.createElement;
      const createRoot = ReactDOM.createRoot ?? ReactDOM.default.createRoot;
      const host = document.createElement('div');
      host.id = 'foundation-fixture';
      document.body.append(host);
      createRoot(host).render(e(React.Fragment ?? React.default.Fragment, null,
        e(button.Button, { 'data-testid': 'button-default' }, 'Default'),
        e(button.Button, { 'data-testid': 'button-sm', size: 'sm' }, 'Small'),
        e(button.Button, { 'data-testid': 'button-icon', size: 'icon' }, '+'),
        e(button.Button, { 'data-testid': 'button-destructive', variant: 'destructive' }, 'Delete'),
        e(button.Button, { 'data-testid': 'button-outline', variant: 'outline' }, 'Outline'),
        e(button.Button, { 'data-testid': 'button-link', variant: 'link' }, 'Link'),
        e(badge.Badge, { 'data-testid': 'badge-destructive', variant: 'destructive' }, 'Error'),
        e(input.Input, { 'data-testid': 'input' }),
        e(select.Select, null, e(select.SelectTrigger, { 'data-testid': 'select' }, e(select.SelectValue, { placeholder: 'Choose' }))),
        e(tabs.Tabs, { defaultValue: 'one' }, e(tabs.TabsList, null, e(tabs.TabsTrigger, { value: 'one', 'data-testid': 'tab' }, 'One')))
      ));
    `,
  });
  const fixture = page.locator("#foundation-fixture");
  await expect(fixture.getByTestId("button-default")).toHaveCSS("height", "32px");
  await expect(fixture.getByTestId("button-sm")).toHaveCSS("height", "28px");
  await expect(fixture.getByTestId("button-icon")).toHaveCSS("width", "32px");
  await expect(fixture.getByTestId("input")).toHaveCSS("height", "32px");
  await expect(fixture.getByTestId("select")).toHaveCSS("height", "32px");
  await expect(fixture.getByTestId("button-destructive")).toHaveCSS("color", "rgb(11, 11, 14)");
  await expect(fixture.getByTestId("badge-destructive")).toHaveCSS("color", "rgb(11, 11, 14)");
  await expect(fixture.getByTestId("button-link")).toHaveCSS("color", "rgb(162, 152, 255)");
  await expect(fixture.getByTestId("button-outline")).toHaveCSS("background-color", /^oklab\(.* \/ 0\.3\)$/);
  await expect(fixture.getByTestId("input")).toHaveCSS("background-color", /^oklab\(.* \/ 0\.3\)$/);
  await expect(fixture.getByTestId("button-default")).not.toHaveCSS("transition-property", "all");
  await expect(fixture.getByTestId("tab")).not.toHaveCSS("transition-property", "all");
  const lightColors = await fixture.locator("[data-testid]").evaluateAll((nodes) =>
    nodes.map((node) => [getComputedStyle(node).color, getComputedStyle(node).backgroundColor]),
  );
  await page.emulateMedia({ colorScheme: "dark" });
  const darkColors = await fixture.locator("[data-testid]").evaluateAll((nodes) =>
    nodes.map((node) => [getComputedStyle(node).color, getComputedStyle(node).backgroundColor]),
  );
  expect(darkColors).toEqual(lightColors);
});

test("hierarchy, filters and view switching keep the selection", async ({ page }) => {
  await page.goto("/#/hierarchy?item=TASK-102");
  await expect(page.getByRole("treeitem", { name: /^EPIC-12 / })).toBeVisible();
  await expect(page.getByTestId("details-panel")).toContainText("Persist session");
  await page.getByRole("tab", { name: "Kanban" }).click();
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
  await expect(page.getByText("Moved TASK-103 to Blocked")).toBeVisible();
});

test("dragging to a refused column shows the lock, returns the card and toasts", async ({ page }) => {
  await page.goto("/#/kanban");
  const drop = await drag(page, "card-TASK-101", "cell-EPIC-12-ready");
  await expect(page.getByTestId("cell-EPIC-12-ready")).toContainText("Couldn't update status. The item remains In progress.");
  await drop();
  await expect(page.getByText("Couldn't update status. The item remains In progress.").last()).toBeVisible();
  await expect(page.getByTestId("cell-EPIC-12-in_progress").getByTestId("card-TASK-101")).toBeVisible();
});

test("Move to… works from the keyboard", async ({ page }) => {
  await page.goto("/#/kanban");
  // the card itself picks up "Move to… TASK-110" in its own accessible name (dnd-kit gives it
  // role="button", and the nested Move-to button's aria-label bleeds into name-from-content), so
  // scope to the card to get the actual Move-to button.
  await page.getByTestId("card-TASK-110").getByRole("button", { name: "Move to… TASK-110" }).focus();
  await page.keyboard.press("Enter");
  await page.getByRole("menuitem", { name: /^Cancelled/ }).focus();
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
  await expect(page.getByText("A local queue of pending messages.")).toBeVisible();
  await page.getByRole("button", { name: "Approve section" }).click();
  await expect(page.getByText("Already resolved.")).toBeVisible();
});

test("epic review fits and works at 390px", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/#/inbox?req=req_accept");
  const title = page.getByRole("heading", { name: "Accept epic · EPIC-12 › Authentication" });
  const accept = page.getByRole("button", { name: "Accept epic", exact: true });
  const changes = page.getByRole("button", { name: "Request changes" });
  await expect(title).toBeVisible();
  for (const control of [title, accept, changes]) {
    const box = await control.boundingBox();
    expect(box?.width).toBeGreaterThan(0);
    expect(box?.x).toBeGreaterThanOrEqual(0);
    expect(box && box.x + box.width).toBeLessThanOrEqual(390);
  }
  expect((await title.boundingBox())?.width).toBeGreaterThanOrEqual(300);
  await page.screenshot({ path: "test-results/needs-you-mobile.png", animations: "disabled" });
  await changes.focus();
  await expect(changes).toBeFocused();
  await changes.click();
  await expect(page.getByRole("textbox", { name: "Comment" })).toBeVisible();
  await accept.click();
  await expect(page.getByText("Already resolved.")).toBeVisible();
});

test("desktop inbox keeps its list beside the selected review", async ({ page }) => {
  await page.goto("/#/inbox?req=req_accept");
  await page.getByRole("radio", { name: "Approvals" }).click();
  const list = page.getByRole("list", { name: "Needs you" });
  const title = page.getByRole("heading", { name: "Accept epic · EPIC-12 › Authentication" });
  await expect(title).toBeVisible();
  const listBox = await list.boundingBox();
  const titleBox = await title.boundingBox();
  expect(listBox && titleBox && listBox.x + listBox.width).toBeLessThan(titleBox?.x ?? 0);
  await page.screenshot({ path: "test-results/needs-you-desktop.png", animations: "disabled" });
});

test("the dependency graph renders and expands a hop", async ({ page }) => {
  await page.goto("/#/dependencies?item=TASK-104");
  await expect(page.getByTestId("node-TASK-98")).toBeVisible();
  await page.getByRole("radio", { name: "Neighbourhood" }).click();
  await expect(page.getByTestId("node-TASK-98")).toHaveCount(0);
  await page.getByRole("combobox", { name: "Hops" }).click();
  await page.getByRole("option", { name: "2" }).click();
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
  await page.getByRole("button", { name: "New orchestrator" }).click();
  await page.getByRole("textbox", { name: "Name" }).fill("Offline sync");
  await page.getByRole("button", { name: /orchestrator$/ }).click();
  await expect(page).toHaveURL(/item=SPIKE-\d+/);
});

test("creating an item confirms success", async ({ page }) => {
  await page.goto("/#/hierarchy");
  await page.getByRole("button", { name: "New item" }).click();
  await page.getByRole("menuitem", { name: "Story" }).click();
  const sheet = page.getByRole("dialog", { name: "New item" });
  await sheet.getByRole("combobox", { name: "Parent" }).click();
  await page.getByRole("option", { name: /EPIC-12/ }).click();
  await sheet.getByRole("textbox", { name: "Title" }).fill("Two-factor login");
  await sheet.getByRole("button", { name: "Create item" }).click();
  await expect(page.getByText(/Created STORY-\d+/)).toBeVisible();
});

test("toast uses the popover surface in the browser", async ({ page }) => {
  await page.goto("/#/hierarchy");
  await page.getByRole("button", { name: "New item" }).click();
  await page.getByRole("menuitem", { name: "Story" }).click();
  const sheet = page.getByRole("dialog", { name: "New item" });
  await sheet.getByRole("combobox", { name: "Parent" }).click();
  await page.getByRole("option", { name: /EPIC-12/ }).click();
  await sheet.getByRole("textbox", { name: "Title" }).fill("Toast surface check");
  await sheet.getByRole("button", { name: "Create item" }).click();
  const toast = page.locator("[data-sonner-toast]").last();
  await expect(toast).toBeVisible();
  expect(await toast.evaluate((el) => getComputedStyle(el).backgroundColor)).toBe("rgb(23, 23, 29)");
});

test("Hierarchy icon controls have 32px hit areas within 30px rows", async ({ page }) => {
  await page.goto("/#/hierarchy");
  const row = page.getByRole("treeitem", { name: /^EPIC-12 / });
  const expand = row.getByRole("button", { name: "Collapse EPIC-12" });
  const add = row.getByRole("button", { name: "Add child to EPIC-12" });
  for (const control of [expand, add]) {
    const box = await control.boundingBox();
    expect(box?.width).toBe(32);
    expect(box?.height).toBe(32);
  }
  expect((await row.boundingBox())?.height).toBe(30);
  await expand.focus();
  await expect(expand).toBeFocused();
});

test("narrow windows show a details sheet above the view", async ({ page }) => {
  await page.setViewportSize({ width: 900, height: 800 });
  await page.goto("/#/hierarchy?item=TASK-101");
  await expect(page.getByRole("dialog", { name: "TASK-101" })).toBeVisible();
  await page.getByRole("dialog", { name: "TASK-101" }).getByRole("button", { name: "Close" }).click();
  await expect(page.getByTestId("view")).toBeVisible();
});
