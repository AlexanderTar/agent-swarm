import { screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { createMockDaemon } from "../mock/daemon";
import { renderWithDaemon } from "../test/render";
import { RequestChanges } from "./RequestChanges";

describe("RequestChanges (§16.11)", () => {
  it("requires a comment and sends it", async () => {
    const d = createMockDaemon();
    const { user } = renderWithDaemon(<RequestChanges requestId="req_section" connected />, { daemon: d, events: false });
    await user.click(screen.getByRole("button", { name: "Request changes" }));
    await user.click(screen.getByRole("button", { name: "Send change request" }));
    expect(screen.getByText("Add a comment describing what to change.")).toBeInTheDocument();
    expect(d.calls.some((c) => c.path.endsWith("/request-changes"))).toBe(false);
    const box = screen.getByRole("textbox", { name: "Comment" });
    expect(box).toHaveAttribute("maxLength", "2000");
    await user.type(box, "Split the queue table");
    await user.click(screen.getByRole("button", { name: "Send change request" }));
    await waitFor(() => expect(d.calls.at(-1)).toMatchObject({ path: "/api/requests/req_section/request-changes", body: { comment: "Split the queue table", via: "board" } }));
  });

  it("keeps unsent text", async () => {
    const a = renderWithDaemon(<RequestChanges requestId="req_plan" connected />, { events: false });
    await a.user.click(screen.getByRole("button", { name: "Request changes" }));
    await a.user.type(screen.getByRole("textbox", { name: "Comment" }), "Draft");
    a.unmount();
    const b = renderWithDaemon(<RequestChanges requestId="req_plan" connected />, { events: false });
    await b.user.click(screen.getByRole("button", { name: "Request changes" }));
    expect(screen.getByRole("textbox", { name: "Comment" })).toHaveValue("Draft");
  });
});
