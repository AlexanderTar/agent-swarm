import { expect, type Page } from "@playwright/test";

// The page itself must never scroll sideways; wide content scrolls inside its own container.
export async function noHorizontalOverflow(page: Page) {
  const { scrollWidth, innerWidth } = await page.evaluate(() => ({ scrollWidth: document.documentElement.scrollWidth, innerWidth: window.innerWidth }));
  expect(scrollWidth, "document scrollWidth vs viewport width").toBeLessThanOrEqual(innerWidth);
}
