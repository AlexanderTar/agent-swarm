import { describe, expect, it } from "vitest";
import { cn } from "@/lib/utils";

describe("cn", () => {
  it("merges conflicting tailwind classes, last wins", () => {
    expect(cn("px-2 h-8", false && "hidden", "px-4")).toBe("h-8 px-4");
  });
});
