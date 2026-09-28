import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

describe("document theme", () => {
  it("advertises the dark palette to browser controls", () => {
    const html = readFileSync("index.html", "utf8");
    const document = new DOMParser().parseFromString(html, "text/html");
    expect(document.querySelector('meta[name="color-scheme"]')?.getAttribute("content")).toBe("dark");
    expect(document.querySelector('meta[name="theme-color"]')?.getAttribute("content")).toBe("#0B0B0E");
  });
});
