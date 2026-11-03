import { describe, expect, it } from "vitest";
import type { CommandModule, Pipeline } from "./api";
import { connect, freshId, newStep, problems, sources } from "./pipeline";

const port = (name: string, types: string[], required = false) => ({
  name,
  title: name,
  types,
  required,
});
const mod = (
  id: string,
  inputs: ReturnType<typeof port>[],
  outputs: ReturnType<typeof port>[],
  effect: CommandModule["effect"] = "pure",
): CommandModule => ({
  id,
  version: 1,
  title: id,
  description: id,
  inputs,
  outputs,
  effect,
  retry_class: "none",
  resource_class: "local",
  settings: { type: "object", properties: {} },
});
const modules = [
  mod(
    "text.template",
    [port("input", ["text", "url"])],
    [port("text", ["text"])],
  ),
  mod(
    "text.extract_url",
    [port("text", ["text"], true)],
    [port("url", ["url"])],
  ),
  mod(
    "video.download",
    [port("url", ["url"], true)],
    [port("source", ["stored_video"])],
    "local",
  ),
  mod(
    "video.compress",
    [port("source", ["stored_video"], true)],
    [port("video", ["video"])],
    "local",
  ),
  mod(
    "ai.chat",
    [port("prompt", ["text"], true)],
    [port("text", ["text"])],
    "paid",
  ),
  mod("reply", [port("text", ["text"]), port("file", ["video"])], [], "sink"),
];
const grab: Pipeline = {
  steps: [
    {
      id: "link",
      module: "text.extract_url",
      inputs: { text: "trigger.args" },
    },
    { id: "source", module: "video.download", inputs: { url: "link.url" } },
    {
      id: "video",
      module: "video.compress",
      inputs: { source: "source.source" },
    },
    { id: "reply", module: "reply", inputs: { file: "video.video" } },
  ],
};

describe("pipeline helpers", () => {
  it("offers only the trigger and earlier outputs", () => {
    expect(sources(grab, 2, modules).map((s) => s.value)).toEqual([
      "trigger.args",
      "trigger.message",
      "link.url",
      "source.source",
    ]);
  });
  it("accepts a valid pipeline", () => {
    expect(problems(grab, modules)).toEqual({});
  });
  it("explains what the server would reject", () => {
    const bad: Pipeline = {
      steps: [
        {
          id: "video",
          module: "video.compress",
          inputs: { source: "link.url" },
        },
        { id: "link", module: "text.extract_url", inputs: {} },
        { id: "a", module: "ai.chat", inputs: { prompt: "trigger.args" } },
        { id: "b", module: "ai.chat", inputs: { prompt: "a.text" } },
        { id: "reply", module: "reply", inputs: {} },
      ],
    };
    const found = problems(bad, modules);
    expect(found.video.join()).toContain("no longer comes before");
    expect(found.link.join()).toContain("Connect");
    expect(found.b.join()).toContain("one AI step");
    expect(found.reply.join()).toContain("text or a file");
    const typed = problems(
      {
        steps: [
          {
            id: "v",
            module: "video.download",
            inputs: { url: "trigger.args" },
          },
        ],
      },
      modules,
    );
    expect(typed.v.join()).toContain("can't take a text");
    const gone = problems(
      {
        steps: [
          { id: "link", module: "text.template", inputs: {} },
          { id: "v", module: "video.download", inputs: { url: "link.url" } },
          { id: "reply", module: "reply", inputs: { text: "link.text" } },
        ],
      },
      modules,
    );
    expect(gone.v.join()).toContain("an output “link” no longer has");
    expect(typed[""].join()).toContain("Reply");
  });
  it("connects new steps to the nearest fitting output and names them uniquely", () => {
    const step = newStep(grab, 3, modules[0], modules);
    expect(step.inputs).toEqual({ input: "link.url" });
    expect(step.id).toBe("template");
    expect(freshId(grab, "video")).toBe("video_2");
    expect(freshId(grab, "9 Bad Name")).toBe("bad_name");
    expect(connect(grab, 3, modules[5], modules)).toEqual({
      file: "video.video",
    });
  });
});
