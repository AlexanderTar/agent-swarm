import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import Root from "./App";
import { createMockDaemon } from "./mock/daemon";
import { mockFetch } from "./mock/fetch";

// A tunnel/LAN daemon: /api/auth/me and the login routes sit in front of the mock board API, which
// accepts the cookie session in place of the Bearer token.
function remoteDaemon(o: { authenticated?: boolean; login?: () => Response } = {}) {
  const daemon = createMockDaemon();
  const board = mockFetch(daemon);
  let authed = o.authenticated ?? false;
  const calls: string[] = [];
  const fn = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    calls.push(`${init?.method ?? "GET"} ${path}`);
    const json = (b: unknown, status = 200) =>
      new Response(JSON.stringify(b), { status, headers: { "Content-Type": "application/json" } });
    if (path === "/api/auth/me")
      return json({
        origin: "tunnel",
        authenticated: authed,
        ...(authed ? { email: "me@example.com" } : {}),
      });
    if (path === "/api/auth/login") {
      const r = o.login?.() ?? new Response(null, { status: 204 });
      if (r.ok) authed = true;
      return r;
    }
    if (path === "/api/auth/logout") {
      authed = false;
      return new Response(null, { status: 204 });
    }
    if (!authed) return json({ error: { code: "unauthorized", message: "no" } }, 401);
    return board(input, {
      ...init,
      headers: {
        ...(init?.headers as Record<string, string>),
        Authorization: `Bearer ${daemon.db.token}`,
      },
    });
  }) as typeof fetch;
  vi.stubGlobal("fetch", fn);
  return {
    daemon,
    calls,
    expire: () => {
      authed = false;
    },
  };
}

afterEach(() => vi.unstubAllGlobals());

describe("Root auth", () => {
  it("shows Login off this Mac when there is no session, and loads the board after signing in", async () => {
    const { calls } = remoteDaemon();
    render(<Root />);
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Email"), "me@example.com");
    await user.type(screen.getByLabelText("Password"), "correct-horse-battery");
    expect(calls).not.toContain("GET /api/items?view=flat");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByTestId("view")).toBeInTheDocument();
    expect(screen.queryByLabelText("Email")).toBeNull();
    expect(calls).not.toContain("GET /api/bootstrap");
  });

  it("shows the signed-in email in the Account menu right after signing in", async () => {
    remoteDaemon();
    render(<Root />);
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Email"), "me@example.com");
    await user.type(screen.getByLabelText("Password"), "correct-horse-battery");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    await screen.findByTestId("view");
    screen.getByRole("button", { name: "Account" }).focus(); // keyboard: a pointer click right after Login unmounts doesn't open the menu in jsdom
    await user.keyboard("{Enter}");
    expect(await screen.findByText("me@example.com")).toBeInTheDocument();
  });

  it("skips Login when the session is already authenticated", async () => {
    remoteDaemon({ authenticated: true });
    render(<Root />);
    expect(await screen.findByTestId("view")).toBeInTheDocument();
    expect(screen.queryByLabelText("Email")).toBeNull();
  });

  it("goes back to Login when the session expires mid-use", async () => {
    const { daemon, expire } = remoteDaemon({ authenticated: true });
    render(<Root />);
    await screen.findByTestId("view");
    expire();
    act(() => daemon.emit("item.changed", { key: "EPIC-12", root_key: "EPIC-12" })); // the refetch it triggers 401s
    expect(await screen.findByLabelText("Email")).toBeInTheDocument();
  });

  it("signs out from the Account menu and returns to Login", async () => {
    const { calls } = remoteDaemon({ authenticated: true });
    render(<Root />);
    const user = userEvent.setup();
    await screen.findByTestId("view");
    await user.click(screen.getByRole("button", { name: "Account" }));
    await user.click(await screen.findByRole("menuitem", { name: "Sign out" }));
    expect(await screen.findByLabelText("Email")).toBeInTheDocument();
    expect(calls).toContain("POST /api/auth/logout");
  });

  it("has no Account menu on this Mac", async () => {
    vi.stubGlobal("fetch", mockFetch(createMockDaemon()));
    render(<Root />);
    await screen.findByTestId("view");
    expect(screen.queryByRole("button", { name: "Account" })).toBeNull();
  });

  it("stays in local mode without Login", async () => {
    vi.stubGlobal("fetch", mockFetch(createMockDaemon()));
    render(<Root />);
    expect(await screen.findByTestId("view")).toBeInTheDocument();
    expect(screen.queryByLabelText("Email")).toBeNull();
  });
});
