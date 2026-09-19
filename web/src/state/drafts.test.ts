import { act, renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { clearDraft, readDraft, useDraft, writeDraft } from "./drafts";

describe("drafts (§16.11 unsent text)", () => {
  it("stores text per request and field", () => {
    writeDraft("req_1", "answer", "hello");
    expect(readDraft("req_1", "answer")).toBe("hello");
    expect(readDraft("req_2", "answer")).toBe("");
    clearDraft("req_1", "answer");
    expect(readDraft("req_1", "answer")).toBe("");
  });

  it("keeps text when switching requests", () => {
    const { result, rerender } = renderHook(({ id }) => useDraft(id, "changes"), { initialProps: { id: "req_a" } });
    act(() => result.current[1]("fix the title"));
    rerender({ id: "req_b" });
    expect(result.current[0]).toBe("");
    rerender({ id: "req_a" });
    expect(result.current[0]).toBe("fix the title");
    act(() => result.current[2]());
    expect(result.current[0]).toBe("");
  });
});
