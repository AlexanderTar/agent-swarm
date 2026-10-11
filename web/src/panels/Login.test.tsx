import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ApiError } from "../api";
import { Login } from "./Login";

function setup(login: (email: string, password: string) => Promise<void>) {
  const onSignedIn = vi.fn();
  render(<Login login={login} onSignedIn={onSignedIn} />);
  const user = userEvent.setup();
  const fill = async () => {
    await user.type(screen.getByLabelText("Email"), "me@example.com");
    await user.type(screen.getByLabelText("Password"), "correct-horse-battery");
  };
  return { onSignedIn, user, fill };
}

describe("Login", () => {
  it("has labelled email and password fields and no signup or forgot-password links", () => {
    setup(vi.fn());
    expect(screen.getByLabelText("Email")).toHaveAttribute("type", "email");
    expect(screen.getByLabelText("Email")).toHaveAttribute("autocomplete", "username");
    expect(screen.getByLabelText("Password")).toHaveAttribute("type", "password");
    expect(screen.getByLabelText("Password")).toHaveAttribute("autocomplete", "current-password");
    expect(screen.getByRole("button", { name: "Sign in" })).toBeEnabled();
    expect(screen.queryByRole("link")).toBeNull();
  });

  it("signs in with the typed credentials", async () => {
    const login = vi.fn(async () => {});
    const { onSignedIn, user, fill } = setup(login);
    await fill();
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    await waitFor(() => expect(onSignedIn).toHaveBeenCalledTimes(1));
    expect(login).toHaveBeenCalledWith("me@example.com", "correct-horse-battery");
  });

  it("shows the wrong-password copy and keeps the form", async () => {
    const login = vi.fn(async () => { throw new ApiError(401, "unauthorized", "Wrong email or password."); });
    const { onSignedIn, user, fill } = setup(login);
    await fill();
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Wrong email or password.");
    expect(onSignedIn).not.toHaveBeenCalled();
    expect(screen.getByLabelText("Password")).toHaveValue("correct-horse-battery");
  });

  it.each([
    [540, "Too many attempts. Try again in 9 minutes."],
    [61, "Too many attempts. Try again in 2 minutes."],
    [30, "Too many attempts. Try again in 1 minute."],
  ])("shows the rate-limit copy for Retry-After %i s", async (retryAfter, copy) => {
    const login = vi.fn(async () => { throw new ApiError(429, "rate_limited", "Too many login attempts. Try again later.", undefined, retryAfter); });
    const { user, fill } = setup(login);
    await fill();
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(copy);
  });

  it("falls back to the server message for any other failure", async () => {
    const login = vi.fn(async () => { throw new TypeError("Failed to fetch"); });
    const { user, fill } = setup(login);
    await fill();
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Failed to fetch");
  });

  it("disables the button while the request is in flight", async () => {
    let done!: () => void;
    const login = vi.fn(() => new Promise<void>((r) => { done = r; }));
    const { user, fill } = setup(login);
    await fill();
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect(screen.getByRole("button", { name: "Signing in…" })).toBeDisabled();
    done();
    await waitFor(() => expect(screen.getByRole("button", { name: "Sign in" })).toBeEnabled());
  });
});
