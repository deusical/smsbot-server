import type { Command, CommandKind, KindSchema, Generation } from "../lib/api";
import type { Workspace } from "../lib/workspace";
import { defaultGeneration } from "../lib/workspace";
import { Field, ModelPicker, Switch } from "./ui";
import { GenerationEditor } from "./Generation";

export function commandValues(command: Command): Record<string, unknown> {
  return {
    instructions: command.instructions,
    model: command.model,
    profile_id: command.profile_id,
    memory: command.memory,
    generation: command.generation,
    execution_minutes: command.execution_minutes,
    ...command.settings,
  };
}
export function SchemaSettings({
  definition,
  command,
  workspace,
  onChange,
}: {
  definition: CommandKind;
  command: Command;
  workspace: Workspace;
  onChange: (value: Partial<Command>) => void;
}) {
  const values = commandValues(command),
    config = workspace.config!;
  const model = workspace.models.find(
    (m) =>
      m.id ===
      (String(values.model || "") ||
        config.profiles.find((p) => p.id === values.profile_id)?.model ||
        config.settings.default_model),
  );
  function change(key: string, value: unknown) {
    onChange({
      [key]: value,
      settings: { ...values, [key]: value },
    } as Partial<Command>);
  }
  function render(
    key: string,
    schema: KindSchema,
    value: unknown,
    update: (value: unknown) => void,
  ): React.ReactNode {
    const label = schema.title || key;
    if (schema.ui === "model")
      return (
        <div key={key}>
          <ModelPicker
            label={label}
            value={String(value || "")}
            models={workspace.models}
            favorites={config.settings.favorites}
            defaultLabel="Default model"
            onChange={update}
          />
          <small>
            Default: {config.settings.default_model || "None"}; configured:{" "}
            {String(value || "Inherit")}; effective: {model?.id || "None"}
          </small>
        </div>
      );
    if (schema.ui === "profile")
      return (
        <Field key={key} label={label}>
          <select
            value={String(value || "")}
            onChange={(e) => update(e.target.value)}
          >
            <option value="">None</option>
            {config.profiles.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name || "Untitled profile"}
              </option>
            ))}
          </select>
        </Field>
      );
    if (schema.ui === "generation")
      return (
        <div key={key}>
          <Switch
            label="Custom generation settings"
            checked={!!value}
            onChange={(on) => update(on ? { ...defaultGeneration } : undefined)}
          />
          {!!value && (
            <GenerationEditor
              label="Command generation"
              value={value as Generation}
              model={model}
              onChange={update}
            />
          )}
        </div>
      );
    if (schema.type === "object")
      return (
        <fieldset key={key}>
          <legend>{label}</legend>
          {Object.entries(schema.properties || {}).map(([k, s]) =>
            render(
              k,
              s,
              (value as Record<string, unknown> | undefined)?.[k],
              (v) => update({ ...(value as object), [k]: v }),
            ),
          )}
        </fieldset>
      );
    if (schema.type === "boolean")
      return (
        <Switch
          key={key}
          label={label}
          hint={
            schema.default !== undefined &&
            value !== undefined &&
            value !== schema.default
              ? `Default: ${String(schema.default)}; configured: ${String(value)}`
              : undefined
          }
          checked={Boolean(value ?? schema.default)}
          onChange={update}
        />
      );
    const configured = value ?? schema.default ?? "";
    let hint =
      schema.ui !== "multiline" &&
      schema.default !== undefined &&
      value !== undefined &&
      JSON.stringify(value) !== JSON.stringify(schema.default)
        ? `Default: ${String(schema.default)}${schema.unit ? ` ${schema.unit}` : ""}; configured: ${String(value)}`
        : schema.unit;
    if (
      schema.effective_maximum !== undefined &&
      Number(configured) > schema.effective_maximum
    ) {
      hint = `Default: ${String(schema.default)}; configured: ${String(configured)}; effective: ${schema.effective_maximum} ${schema.unit || ""} (host attachment limit)`;
    }
    return (
      <Field key={key} label={label} hint={hint}>
        {schema.enum ? (
          <select
            value={String(configured)}
            onChange={(e) => update(e.target.value)}
          >
            {schema.enum.map((v) => (
              <option key={v} value={v}>
                {v}
              </option>
            ))}
          </select>
        ) : schema.ui === "multiline" ? (
          <textarea
            rows={4}
            maxLength={schema.maximum}
            value={String(configured)}
            onChange={(e) => update(e.target.value)}
          />
        ) : (
          <input
            type={schema.type === "number" ? "number" : "text"}
            min={schema.minimum}
            max={schema.maximum}
            step={schema.type === "number" ? "any" : undefined}
            value={String(configured)}
            onChange={(e) =>
              update(
                schema.type === "number"
                  ? Number(e.target.value)
                  : e.target.value,
              )
            }
          />
        )}
      </Field>
    );
  }
  const mediaCost = definition.schema.ui === "video-cost";
  const setting = (key: string) =>
    Number(values[key] ?? definition.schema.properties?.[key]?.default);
  return (
    <>
      {mediaCost && (
        <div className="muted">
          <p>
            Up to {setting("max_attempts")} attempts &times; {setting("passes")}{" "}
            passes = {setting("max_attempts") * setting("passes")} encode passes
            plus a verification decode. A long video can take tens of minutes;
            deadline {setting("execution_minutes") || 15} min.
          </p>
          {(setting("execution_minutes") || 15) <
            setting("max_attempts") * setting("passes") * 5 && (
            <p role="note">
              This deadline may be short for the selected encoding work.
            </p>
          )}
        </div>
      )}
      {Object.entries(definition.schema.properties || {}).map(([key, s]) =>
        render(key, s, values[key], (value) => change(key, value)),
      )}
    </>
  );
}

// A kind switch is an explicit reset of kind-specific state. Common command
// metadata remains in the caller, while only target-schema defaults survive.
export function settingsForKind(
  command: Command,
  kind: CommandKind,
): Partial<Command> {
  const settings: Record<string, unknown> = {};
  for (const [key, schema] of Object.entries(kind.schema.properties || {})) {
    if (schema.default !== undefined) settings[key] = schema.default;
  }
  if (kind.schema.properties?.instructions)
    settings.instructions = command.instructions;
  return {
    kind: kind.id,
    type: kind.legacy_type,
    settings,
    instructions: String(settings.instructions || ""),
    model: "",
    profile_id: "",
    memory: false,
    generation: undefined,
    execution_minutes: undefined,
  };
}
