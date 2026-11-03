import type { CommandModule, Pipeline, PipelineStep } from "./api";

/** What the command message provides to the first steps. */
export const triggerOutputs = [
  { name: "args", title: "Text after the command", types: ["text"] },
  { name: "message", title: "The command message", types: ["message"] },
];

export const maxSteps = 8;

export type Source = {
  /** The binding, "trigger.args" or "<step>.<output>". */
  value: string;
  label: string;
  type: string;
};

/** Outputs available to the step at index, from the trigger and earlier steps. */
export function sources(
  pipeline: Pipeline,
  index: number,
  modules: CommandModule[],
): Source[] {
  const out: Source[] = triggerOutputs.map((o) => ({
    value: "trigger." + o.name,
    label: "Command · " + o.title,
    type: o.types[0],
  }));
  pipeline.steps.slice(0, index).forEach((step) => {
    const m = modules.find((m) => m.id === step.module);
    for (const o of m?.outputs ?? [])
      out.push({
        value: step.id + "." + o.name,
        label: `${step.id} · ${o.title}`,
        type: o.types[0],
      });
  });
  return out;
}

/** A step id not yet used in the pipeline, derived from base. */
export function freshId(pipeline: Pipeline, base: string): string {
  const clean =
    base
      .toLowerCase()
      .replace(/[^a-z0-9_]/g, "_")
      .replace(/^[^a-z]+/, "")
      .slice(0, 20) || "step";
  const used = new Set(pipeline.steps.map((s) => s.id));
  let id = clean,
    n = 2;
  while (used.has(id) || id === "trigger") id = `${clean}_${n++}`;
  return id;
}

/**
 * Problems the server would reject, keyed by step id ("" for the pipeline).
 * The server stays authoritative; this only explains them before saving.
 */
export function problems(
  pipeline: Pipeline,
  modules: CommandModule[],
): Record<string, string[]> {
  const found: Record<string, string[]> = {};
  const add = (step: string, text: string) =>
    (found[step] = [...(found[step] ?? []), text]);
  const steps = pipeline.steps;
  if (!steps.length) add("", "Add at least a reply step.");
  if (steps.length > maxSteps) add("", `Use at most ${maxSteps} steps.`);
  const ids = new Set<string>();
  steps.forEach((step, i) => {
    const m = modules.find((m) => m.id === step.module);
    if (!/^[a-z][a-z0-9_]{0,23}$/.test(step.id) || step.id === "trigger")
      add(step.id, "Step names are 1–24 lowercase letters, digits or _.");
    if (ids.has(step.id)) add(step.id, "Two steps have this name.");
    ids.add(step.id);
    if (!m) {
      add(step.id, "Unknown module.");
      return;
    }
    if (m.effect === "sink" && i !== steps.length - 1)
      add(step.id, "The reply must be the last step.");
    const available = sources(pipeline, i, modules);
    for (const port of m.inputs) {
      const binding = step.inputs?.[port.name];
      if (!binding) {
        if (port.required) add(step.id, `Connect “${port.title}”.`);
        continue;
      }
      const source = available.find((s) => s.value === binding);
      if (!source) {
        const from = binding.split(".")[0];
        add(
          step.id,
          steps.slice(0, i).some((s) => s.id === from)
            ? `“${port.title}” is connected to an output “${from}” no longer has.`
            : `“${port.title}” is connected to a step that no longer comes before it.`,
        );
      } else if (!port.types.includes(source.type))
        add(
          step.id,
          `“${port.title}” can't take a ${source.type.replace("_", " ")}.`,
        );
    }
  });
  const last = steps[steps.length - 1];
  if (last && modules.find((m) => m.id === last.module)?.effect !== "sink")
    add("", "End with a Reply step.");
  if (last?.module === "reply" && !last.inputs?.text && !last.inputs?.file)
    add(last.id, "Connect text or a file to send.");
  return found;
}

/** A new step for module, with its inputs connected where only one source fits. */
export function newStep(
  pipeline: Pipeline,
  index: number,
  module: CommandModule,
  modules: CommandModule[],
): PipelineStep {
  return {
    id: freshId(pipeline, module.id.split(".").pop() || "step"),
    module: module.id,
    inputs: connect(pipeline, index, module, modules),
  };
}

/** Connects each input to the nearest earlier output of a fitting type. */
export function connect(
  pipeline: Pipeline,
  index: number,
  module: CommandModule,
  modules: CommandModule[],
): Record<string, string> {
  const available = sources(pipeline, index, modules);
  const inputs: Record<string, string> = {};
  for (const port of module.inputs) {
    // Optional inputs connect only to a step's output, never straight to the
    // command text, so a reply doesn't echo the message by default.
    const fits = available.filter(
      (s) =>
        port.types.includes(s.type) &&
        (port.required || !s.value.startsWith("trigger.")),
    );
    if (fits.length) inputs[port.name] = fits[fits.length - 1].value;
  }
  return inputs;
}

/**
 * Whether the steps can outlast the two-minute window, so the command uses its
 * execution deadline: a long-running step, or several AI requests.
 */
export function needsDeadline(
  pipeline: Pipeline,
  modules: CommandModule[],
): boolean {
  const used = pipeline.steps.map((s) =>
    modules.find((m) => m.id === s.module),
  );
  return (
    used.some((m) => m?.long_running) ||
    used.filter((m) => m?.effect === "paid").length > 1
  );
}

/** The steps a new custom command starts with. */
export function starterPipeline(): Pipeline {
  return {
    steps: [
      {
        id: "text",
        module: "text.template",
        inputs: { input: "trigger.args" },
        settings: { template: "You said: {input}" },
      },
      { id: "reply", module: "reply", inputs: { text: "text.text" } },
    ],
  };
}

/** A short "Find video link → Download video → …" summary of the steps. */
export function flowSummary(
  pipeline: Pipeline | undefined,
  modules: CommandModule[],
): string {
  if (!pipeline) return "";
  return pipeline.steps
    .filter((s) => s.module !== "context.none")
    .map((s) => modules.find((m) => m.id === s.module)?.title ?? s.module)
    .join(" → ");
}

const question = (require: boolean): PipelineStep => ({
  id: "question",
  module: "message.question",
  inputs: { text: "trigger.args", message: "trigger.message" },
  settings: { require_question: require },
});
const answer = (
  instructions: string,
  context: string | null,
): PipelineStep => ({
  id: "answer",
  module: "ai.chat",
  inputs: {
    prompt: "question.prompt",
    file: "question.file",
    ...(context ? { context: context + ".context" } : {}),
  },
  settings: { instructions },
});

/** Starting points for new commands; each is ordinary steps. */
export const starters = {
  quickReply: (text: string): Pipeline => ({
    steps: [
      {
        id: "text",
        module: "text.template",
        inputs: { input: "trigger.args" },
        settings: { template: text },
      },
      { id: "reply", module: "reply", inputs: { text: "text.text" } },
    ],
  }),
  assistant: (instructions: string): Pipeline => ({
    steps: [
      question(true),
      { id: "conversation", module: "context.thread" },
      answer(instructions, "conversation"),
      {
        id: "reply",
        module: "reply",
        inputs: { text: "answer.text", continues: "answer.context" },
      },
    ],
  }),
  oneShot: (instructions: string): Pipeline => ({
    steps: [
      question(false),
      answer(instructions, null),
      { id: "reply", module: "reply", inputs: { text: "answer.text" } },
    ],
  }),
  download: (): Pipeline => ({
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
  }),
};
