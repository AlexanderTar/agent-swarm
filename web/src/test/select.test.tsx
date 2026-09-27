import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { comboText, optionTexts, pickOption } from "./select";

function Host() {
  const [v, setV] = useState("a");
  return (
    <Select value={v} onValueChange={setV}>
      <SelectTrigger aria-label="Letter"><SelectValue /></SelectTrigger>
      <SelectContent>
        <SelectItem value="a">Alpha</SelectItem>
        <SelectItem value="b">Beta</SelectItem>
      </SelectContent>
    </Select>
  );
}

describe("Radix Select test helpers", () => {
  it("reads, lists and picks options", async () => {
    const user = userEvent.setup();
    render(<Host />);
    expect(comboText("Letter")).toBe("Alpha");
    expect(await optionTexts(user, "Letter")).toEqual(["Alpha", "Beta"]);
    await pickOption(user, "Letter", "Beta");
    expect(comboText("Letter")).toBe("Beta");
  });
});
