import { screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { createMockDaemon } from "../mock/daemon";
import { renderWithDaemon } from "../test/render";
import { ConfirmRepos } from "./ConfirmRepos";

const setup = (d = createMockDaemon()) => {
  const request = d.db.requests.find((r) => r.id === "req_repos")!;
  return renderWithDaemon(<ConfirmRepos request={request} connected />, { daemon: d, events: false });
};

describe("ConfirmRepos (§16.11)", () => {
  it("starts with proposed checked and additions unchecked, with reasons", async () => {
    setup();
    const proposed = await screen.findByRole("group", { name: "Proposed" });
    expect(await within(proposed).findByRole("checkbox", { name: /endurio-chat/ })).toBeChecked();
    expect(within(proposed).getByRole("checkbox", { name: /endurio-app/ })).toBeChecked();
    expect(within(proposed).getByText("You selected")).toBeInTheDocument();
    expect(within(proposed).getByText("Reason: the sync queue lives in the app's data layer.")).toBeInTheDocument();
    const additions = screen.getByRole("group", { name: "Suggested additions" });
    expect(within(additions).getByRole("checkbox", { name: /endurio-landing/ })).not.toBeChecked();
    expect(screen.getByText("“Offline sync needs the chat API and the app client.”")).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Add another" })).toBeInTheDocument();
  });

  it("requires at least one repository", async () => {
    const { user, daemon } = setup();
    const proposed = await screen.findByRole("group", { name: "Proposed" });
    await user.click(await within(proposed).findByRole("checkbox", { name: /endurio-chat/ }));
    await user.click(within(proposed).getByRole("checkbox", { name: /endurio-app/ }));
    await user.click(screen.getByRole("button", { name: "Confirm repositories" }));
    expect(screen.getByText("Choose at least one repository.")).toBeInTheDocument();
    expect(daemon.calls.some((c) => c.path.endsWith("/confirm-repos"))).toBe(false);
  });

  it("sends the checked set with additions, picker choices and the comment", async () => {
    const { user, daemon } = setup();
    await user.click(await within(screen.getByRole("group", { name: "Suggested additions" })).findByRole("checkbox", { name: /endurio-landing/ }));
    const picker = screen.getByRole("group", { name: "Add another" });
    await user.click(within(await within(picker).findByRole("group", { name: "All" })).getByRole("checkbox", { name: /agent-swarm/ }));
    await user.type(screen.getByRole("textbox", { name: "Comment (optional)" }), "Landing too");
    await user.click(screen.getByRole("button", { name: "Confirm repositories" }));
    await waitFor(() => expect(daemon.calls.at(-1)).toMatchObject({
      path: "/api/requests/req_repos/confirm-repos",
      body: { repos: ["repo_chat", "repo_app", "repo_landing", "repo_swarm"], comment: "Landing too", repos_version: 0, via: "board" },
    }));
  });

  it("shows the stale-version banner", async () => {
    const d = createMockDaemon();
    d.override("POST /api/requests/req_repos/confirm-repos", { status: 409, body: { error: { code: "conflict", message: "This request changed. Review the latest version." } } });
    const { user } = setup(d);
    await user.click(await screen.findByRole("button", { name: "Confirm repositories" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("This request changed. Review the latest version.");
  });
});
