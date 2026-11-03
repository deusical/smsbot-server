import { render, screen, fireEvent } from "@testing-library/react";
import { describe, it, expect, vi } from "vitest";
import { SchemaSettings, settingsForKind } from "./SchemaSettings";
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
    expect(screen.queryByText(/configured: hello/)).not.toBeInTheDocument();
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

it("kind switching clears incompatible fields", () => {
  const c = {
    instructions: "Reply",
    model: "old-model",
    memory: true,
    generation: { tokens: 2048 },
  } as Command;
  const k = {
    id: "fixed.reply",
 title:"Fixed text", version:1,threads:false,retry_class:"none",resource_class:"upload",
    legacy_type: "fixed",
    schema: {
      type: "object",
      properties: { instructions: { type: "string", default: "" } },
    },
  } as CommandKind;
  const changed = settingsForKind(c, k);
  expect(changed).toMatchObject({
    memory: false,
    model: "",
    instructions: "Reply",
    settings: { instructions: "Reply" },
  });
  expect(changed.generation).toBeUndefined();
});
