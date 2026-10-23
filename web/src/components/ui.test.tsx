import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import type { Model } from "../lib/api";
import { ModelPicker } from "./ui";

const models: Model[] = ["Alpha", "Beta"].map(name => ({
  id: name.toLowerCase(), name, context_length: 32000, parameters: [],
  has_reasoning: false, reasoning_mandatory: false, reasoning_efforts: null,
}));

it("filters models and selects the matching result with the keyboard", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<ModelPicker label="Model" value="" models={models} onChange={onChange} />);
  await user.click(screen.getByRole("button", { name: /Model Choose a model/ }));
  await user.type(screen.getByRole("searchbox", { name: "Search model" }), "BETA");
  expect(screen.queryByRole("option", { name: /Alpha/ })).not.toBeInTheDocument();
  expect(screen.getByRole("option", { name: /Beta/ })).toBeVisible();
  await user.keyboard("{Enter}");
  expect(onChange).toHaveBeenCalledExactlyOnceWith("beta");
  expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
});

it("puts favorites first and lets the owner favorite a model without selecting it", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn(), onToggleFavorite = vi.fn();
  render(<ModelPicker label="Model" value="alpha" models={models} favorites={["beta"]}
    onChange={onChange} onToggleFavorite={onToggleFavorite} />);
  await user.click(screen.getByRole("button", { name: "Model Alpha" }));
  const options = within(screen.getByRole("listbox")).getAllByRole("option");
  expect(options[0]).toHaveTextContent("Beta");
  expect(options[1]).toHaveAttribute("aria-selected", "true");
  await user.click(screen.getByRole("button", { name: "Favorite Alpha" }));
  expect(onToggleFavorite).toHaveBeenCalledExactlyOnceWith("alpha");
  expect(onChange).not.toHaveBeenCalled();
  expect(screen.getByRole("listbox")).toBeVisible();
});

it("leaves the selection unchanged when a search has no matches", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<ModelPicker label="Model" value="alpha" models={models} onChange={onChange} />);
  await user.click(screen.getByRole("button", { name: "Model Alpha" }));
  await user.type(screen.getByRole("searchbox"), "no-such-model");
  expect(screen.queryAllByRole("option")).toHaveLength(0);
  await user.keyboard("{Enter}{Escape}");
  expect(onChange).not.toHaveBeenCalled();
  expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
});
