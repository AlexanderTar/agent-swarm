import { screen } from "@testing-library/react";
import type { UserEvent } from "@testing-library/user-event";

type Name = string | RegExp;

export const comboText = (name: Name) => screen.getByRole("combobox", { name }).textContent ?? "";

export async function optionTexts(user: UserEvent, name: Name): Promise<string[]> {
  await user.click(screen.getByRole("combobox", { name }));
  const texts = (await screen.findAllByRole("option")).map((o) => o.textContent ?? "");
  await user.keyboard("{Escape}");
  return texts;
}

export async function pickOption(user: UserEvent, name: Name, option: Name): Promise<void> {
  await user.click(screen.getByRole("combobox", { name }));
  await user.click(await screen.findByRole("option", { name: option }));
}
