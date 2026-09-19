import { describe, expect, it } from "vitest";
import golden from "../../../testdata/kebab_cases.json";
import { defaultOrchestratorName, kebab } from "./kebab";

describe("kebab (§4 ids.Kebab)", () => {
  it.each([
    ["Investigate login crash", "investigate-login-crash"],
    ["Café Déjà vu", "cafe-deja-vu"],
    ["  --Hello__World!! ", "hello-world"],
    ["日本語", ""],
    ["UPPER 123 lower", "upper-123-lower"],
    ["a".repeat(50), "a".repeat(48)],
  ])("%s -> %s", (input, out) => {
    expect(kebab(input)).toBe(out);
  });

  it("truncates at a dash boundary", () => {
    expect(kebab("abcdefghij klmnop", 12)).toBe("abcdefghij");
    expect(kebab("abcdefghij-klm", 10)).toBe("abcdefghij");
  });

  it("builds default orchestrator names", () => {
    expect(defaultOrchestratorName("Authentication and authorisation overhaul")).toBe("authentication-and-orchestrator");
    expect(defaultOrchestratorName("Authentication")).toBe("authentication-orchestrator");
    expect(defaultOrchestratorName("???")).toBe("orchestrator");
  });

  // Shared golden cases (spec §4, testdata/kebab_cases.json): keeps this port in
  // step with internal/ids.KebabMax (P2) and apps/menubar's Kebab.make (P4). An
  // "error" case is where Go/Swift fail; this port has no error path and returns "".
  it.each((golden as { cases: { input: string; max: number; output?: string; error?: boolean }[] }).cases)(
    "golden %o",
    (c) => {
      expect(kebab(c.input, c.max)).toBe(c.error ? "" : c.output);
    },
  );
});
