import { expect, test } from "@playwright/test";
import { noHorizontalOverflow } from "./helpers";

// The mock daemon plays a tunnel/LAN origin while /__mock/remote is on: sessions ride a cookie,
// and the board API answers 401 without it.
const EMAIL = "me@example.com";
const PASSWORD = "correct-horse-battery";

test.beforeEach(async ({ request }) => {
  await request.get("/__mock/reset");
  await request.get("/__mock/remote?on=1");
});

test.afterEach(async ({ request }) => {
  await request.get("/__mock/remote?on=0");
});

async function signIn(page: import("@playwright/test").Page, email = EMAIL, password = PASSWORD) {
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
}

test("a visitor without a session sees Login, not the board", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByLabel("Email")).toBeVisible();
  await expect(page.getByTestId("view")).toHaveCount(0);
  await noHorizontalOverflow(page);
});

test("a wrong password shows the error and stays on Login", async ({ page }) => {
  await page.goto("/");
  await signIn(page, EMAIL, "nope");
  await expect(page.getByRole("alert")).toHaveText("Wrong email or password.");
  await expect(page.getByLabel("Email")).toBeVisible();
});

test("too many attempts shows the wait", async ({ page }) => {
  await page.goto("/");
  await signIn(page, "limited@example.com", "whatever");
  await expect(page.getByRole("alert")).toHaveText("Too many attempts. Try again in 9 minutes.");
});

test("signing in loads the board, hides Open terminal, and Sign out returns to Login", async ({
  page,
}) => {
  await page.goto("/#/hierarchy?item=EPIC-12");
  await signIn(page);
  await expect(page.getByTestId("view")).toBeVisible();
  await page.getByRole("tab", { name: "Agents" }).click();
  const row = page.getByTestId("agent-auth-epic-orchestrator");
  await expect(row.getByRole("button", { name: "Pause group", exact: true })).toBeVisible();
  await expect(row.getByRole("button", { name: "Terminal", exact: true })).toHaveCount(0);

  await page.goto("/#/hierarchy"); // closes the Details sheet, which covers the header's right edge
  await page.reload();
  await expect(page.getByTestId("view")).toBeVisible(); // the cookie outlives a reload

  await page.getByRole("button", { name: "Account" }).click();
  await page.getByRole("menuitem", { name: "Sign out" }).click();
  await expect(page.getByLabel("Email")).toBeVisible();
  await page.reload();
  await expect(page.getByLabel("Email")).toBeVisible();
});
