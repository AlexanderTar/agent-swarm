import { act, renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { useMediaQuery } from "./media";

describe("useMediaQuery", () => {
  it("follows matchMedia changes", () => {
    let listener: (() => void) | undefined;
    const mql = { matches: true, addEventListener: (_: string, l: () => void) => { listener = l; }, removeEventListener: vi.fn() };
    vi.spyOn(window, "matchMedia").mockReturnValue(mql as unknown as MediaQueryList);
    const { result, unmount } = renderHook(() => useMediaQuery("(max-width: 1099px)"));
    expect(result.current).toBe(true);
    mql.matches = false;
    act(() => listener?.());
    expect(result.current).toBe(false);
    unmount();
    expect(mql.removeEventListener).toHaveBeenCalled();
  });
});
