import type { CommandModule, Pipeline, PipelineStep } from "../lib/api";
import type { Workspace } from "../lib/workspace";
import {
  connect,
  maxSteps,
  needsDeadline,
  newStep,
  problems,
  sources,
  triggerOutputs,
} from "../lib/pipeline";
import { SchemaFields, VideoCost } from "./SchemaSettings";
import { moduleGroup } from "../lib/modules";
import { Field } from "./ui";
import { Icon } from "./Icon";

const effects: Record<CommandModule["effect"], string> = {
  pure: "",
  local: "Runs on this server",
  paid: "Paid AI request",
  sink: "Sends the reply",
};

/**
 * Edits a command's steps: each step runs one module, and each of its inputs
 * is connected to the command message or an earlier step's output.
 */
export function PipelineEditor({
  pipeline,
  modules,
  workspace,
  onChange,
}: {
  pipeline: Pipeline;
  modules: CommandModule[];
  workspace: Workspace;
  onChange: (pipeline: Pipeline) => void;
}) {
  const issues = problems(pipeline, modules);
  const longRunning = needsDeadline(pipeline, modules);
  const minutes = pipeline.execution_minutes || 15;
  function setSteps(steps: PipelineStep[]) {
    onChange({ ...pipeline, steps });
  }
  function update(i: number, step: PipelineStep) {
    setSteps(pipeline.steps.map((s, n) => (n === i ? step : s)));
  }
  function rename(i: number, id: string) {
    const old = pipeline.steps[i].id;
    // Keep later connections pointing at the renamed step.
    setSteps(
      pipeline.steps.map((s, n) =>
        n === i
          ? { ...s, id }
          : {
              ...s,
              inputs: Object.fromEntries(
                Object.entries(s.inputs ?? {}).map(([k, v]) => [
                  k,
                  v.startsWith(old + ".") ? id + v.slice(old.length) : v,
                ]),
              ),
            },
      ),
    );
  }
  function move(i: number, by: number) {
    const steps = [...pipeline.steps];
    [steps[i], steps[i + by]] = [steps[i + by], steps[i]];
    setSteps(steps);
  }
  function add(id: string) {
    const module = modules.find((m) => m.id === id);
    if (!module) return;
    const last = pipeline.steps[pipeline.steps.length - 1];
    // New steps go before the reply, which stays last.
    const at =
      last?.module === "reply" && module.effect !== "sink"
        ? pipeline.steps.length - 1
        : pipeline.steps.length;
    const steps = [...pipeline.steps];
    steps.splice(at, 0, newStep(pipeline, at, module, modules));
    setSteps(steps);
  }
  const addable = modules.filter(
    (m) =>
      m.effect !== "sink" || !pipeline.steps.some((s) => s.module === m.id),
  );
  const groups = ["Text", "AI", "Memory", "Video", "Send"];
  const anchor = (i: number) => `step-${i + 1}`;
  return (
    <div className="pipeline" aria-label="Steps">
      <ol className="flow-strip" aria-label="Step flow">
        <li>
          <span className="flow-chip trigger">Command</span>
        </li>
        {pipeline.steps.map((step, i) => {
          const module = modules.find((m) => m.id === step.module);
          return (
            <li key={i}>
              <span className="flow-arrow" aria-hidden="true">
                →
              </span>
              <a
                href={"#" + anchor(i)}
                className={
                  "flow-chip effect-" +
                  (module?.effect ?? "pure") +
                  ((issues[step.id] ?? []).length ? " has-problem" : "")
                }
                onClick={(e) => {
                  e.preventDefault();
                  document
                    .getElementById(anchor(i))
                    ?.scrollIntoView({ behavior: "smooth", block: "start" });
                }}
              >
                {module?.title ?? step.module}
              </a>
            </li>
          );
        })}
      </ol>
      <ol className="pipeline-steps">
        <li className="pipeline-step trigger">
          <div className="step-head">
            <span className="step-number">0</span>
            <strong>Command message</strong>
          </div>
          <p className="muted">
            Provides{" "}
            {triggerOutputs.map((o) => o.title.toLowerCase()).join(" and ")}.
          </p>
        </li>
        {pipeline.steps.map((step, i) => {
          const module = modules.find((m) => m.id === step.module);
          const available = sources(pipeline, i, modules);
          const stepIssues = issues[step.id] ?? [];
          return (
            <li
              key={i}
              id={anchor(i)}
              className="pipeline-step"
              aria-label={`Step ${i + 1}: ${step.id}`}
            >
              <div className="step-head">
                <span className="step-number">{i + 1}</span>
                <select
                  aria-label={`Module for step ${i + 1}`}
                  value={step.module}
                  onChange={(e) => {
                    const next = modules.find((m) => m.id === e.target.value);
                    if (!next) return;
                    update(i, {
                      id: step.id,
                      module: next.id,
                      inputs: connect(pipeline, i, next, modules),
                    });
                  }}
                >
                  {!module && (
                    <option value={step.module}>{step.module}</option>
                  )}
                  {groups.map((g) => (
                    <optgroup key={g} label={g}>
                      {modules
                        .filter((m) => moduleGroup(m.id) === g)
                        .map((m) => (
                          <option key={m.id} value={m.id}>
                            {m.title}
                          </option>
                        ))}
                    </optgroup>
                  ))}
                </select>
                <input
                  className="step-id"
                  aria-label={`Name of step ${i + 1}`}
                  maxLength={24}
                  spellCheck={false}
                  value={step.id}
                  onChange={(e) =>
                    rename(i, e.target.value.toLowerCase().replace(/\s/g, "_"))
                  }
                />
                <div className="step-actions">
                  <button
                    type="button"
                    className="icon ghost"
                    aria-label={`Move step ${i + 1} up`}
                    disabled={i === 0}
                    onClick={() => move(i, -1)}
                  >
                    <span className="flip">
                      <Icon name="arrowdown" size={15} />
                    </span>
                  </button>
                  <button
                    type="button"
                    className="icon ghost"
                    aria-label={`Move step ${i + 1} down`}
                    disabled={i === pipeline.steps.length - 1}
                    onClick={() => move(i, 1)}
                  >
                    <Icon name="arrowdown" size={15} />
                  </button>
                  <button
                    type="button"
                    className="icon ghost danger"
                    aria-label={`Remove step ${i + 1}`}
                    onClick={() =>
                      setSteps(pipeline.steps.filter((_, n) => n !== i))
                    }
                  >
                    <Icon name="trash" size={15} />
                  </button>
                </div>
              </div>
              {module && (
                <>
                  <p className="muted">
                    {module.description}
                    {effects[module.effect] && (
                      <span className={"type-tag effect-" + module.effect}>
                        {effects[module.effect]}
                      </span>
                    )}
                  </p>
                  {module.inputs.map((port) => {
                    const fits = available.filter((s) =>
                      port.types.includes(s.type),
                    );
                    const value = step.inputs?.[port.name] ?? "";
                    return (
                      <Field
                        key={port.name}
                        label={`${port.title}${port.required ? "" : " (optional)"}`}
                      >
                        <select
                          aria-label={`${port.title} for step ${i + 1}`}
                          value={value}
                          onChange={(e) => {
                            const inputs = { ...step.inputs };
                            if (e.target.value)
                              inputs[port.name] = e.target.value;
                            else delete inputs[port.name];
                            update(i, { ...step, inputs });
                          }}
                        >
                          <option value="">Not connected</option>
                          {value && !fits.some((s) => s.value === value) && (
                            <option value={value}>
                              {value} (not available)
                            </option>
                          )}
                          {fits.map((s) => (
                            <option key={s.value} value={s.value}>
                              {s.label}
                            </option>
                          ))}
                        </select>
                      </Field>
                    );
                  })}
                  {module.settings.ui === "video-cost" && (
                    <VideoCost
                      attempts={Number(
                        step.settings?.max_attempts ??
                          module.settings.properties?.max_attempts?.default,
                      )}
                      passes={Number(
                        step.settings?.passes ??
                          module.settings.properties?.passes?.default,
                      )}
                      minutes={minutes}
                    />
                  )}
                  <SchemaFields
                    schema={module.settings}
                    values={step.settings ?? {}}
                    workspace={workspace}
                    onChange={(key, value) =>
                      update(i, {
                        ...step,
                        settings: { ...step.settings, [key]: value },
                      })
                    }
                  />
                </>
              )}
              {stepIssues.map((text) => (
                <p key={text} role="alert" className="inline-alert">
                  {text}
                </p>
              ))}
            </li>
          );
        })}
      </ol>
      {(issues[""] ?? []).map((text) => (
        <p key={text} role="alert" className="inline-alert">
          {text}
        </p>
      ))}
      <Field label="Add a step">
        <select
          aria-label="Add a step"
          value=""
          disabled={pipeline.steps.length >= maxSteps}
          onChange={(e) => add(e.target.value)}
        >
          <option value="">Choose a module…</option>
          {groups.map((g) => (
            <optgroup key={g} label={g}>
              {addable
                .filter((m) => moduleGroup(m.id) === g)
                .map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.title}
                  </option>
                ))}
            </optgroup>
          ))}
        </select>
      </Field>
      {longRunning && (
        <Field
          label="Execution deadline (minutes)"
          hint="Accepted requests have this long to finish every step and send. Defaults to 15; maximum 60."
        >
          <input
            type="number"
            min={1}
            max={60}
            value={pipeline.execution_minutes || ""}
            placeholder="15"
            onChange={(e) =>
              onChange({
                ...pipeline,
                execution_minutes: Number(e.target.value) || undefined,
              })
            }
          />
        </Field>
      )}
    </div>
  );
}
