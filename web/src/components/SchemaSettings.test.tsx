import { render, screen, fireEvent } from "@testing-library/react";
import { describe, it, expect, vi } from "vitest";
import { SchemaSettings } from "./SchemaSettings";
import type { Workspace } from "../lib/workspace";
import type { Command, CommandKind } from "../lib/api";

describe("schema settings", () => {
  it("renders controls from hints and preserves settings outside edited fields", () => {
    const command = {
      name: "sample",
      type: "fixed",
      instructions: "hello",
      settings: { instructions: "hello", future: "preserve" },
    } as unknown as Command;
    const definition = {
      id: "fixed.reply",
      schema: {
        type: "object",
        properties: {
          instructions: {
            type: "string",
            title: "Reply",
            ui: "multiline",
            default: "",
          },
          attempts: {
            type: "number",
            title: "Attempts",
            minimum: 1,
            maximum: 3,
            default: 3,
            unit: "attempts",
          },
          mode: {
            type: "string",
            title: "Mode",
            enum: ["fast", "slow"],
            default: "slow",
          },
          enabled: { type: "boolean", title: "Toggle", default: true },
        },
      },
    } as unknown as CommandKind;
    const workspace = {
      config: { settings: { favorites: [] }, profiles: [] },
      models: [],
    } as unknown as Workspace;
    const onChange = vi.fn();
    render(
      <SchemaSettings
        definition={definition}
        command={command}
        workspace={workspace}
        onChange={onChange}
      />,
    );
    expect(screen.getByLabelText("Attempts")).toHaveAttribute("max", "3");
    expect(screen.getByLabelText("Toggle")).toBeChecked();
    fireEvent.change(screen.getByLabelText("Reply"), {
      target: { value: "edited" },
    });
    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({
        instructions: "edited",
        settings: expect.objectContaining({
          future: "preserve",
          instructions: "edited",
        }),
      }),
    );
  });
});
