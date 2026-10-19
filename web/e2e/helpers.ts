import { type Locator, type Page, expect } from "@playwright/test";

// The page itself must never scroll sideways; wide content scrolls inside its own container.
export async function noHorizontalOverflow(page: Page) {
  const { scrollWidth, innerWidth } = await page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    innerWidth: window.innerWidth,
  }));
  expect(scrollWidth, "document scrollWidth vs viewport width").toBeLessThanOrEqual(innerWidth);
}

// Sheets and dialogs slide in over 500 ms. Their box is a float-rounded transform mid-animation (a 375 px
// sheet measures 375.00003), so wait for the enter animation to finish before measuring or hit-testing it.
export async function settled(locator: Locator) {
  await locator.evaluate((el) => Promise.all(el.getAnimations().map((a) => a.finished)));
}
