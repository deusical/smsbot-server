import type { Generation, Model } from "./api";
import { Field, Segmented } from "./ui";

export function GenerationEditor({
  label,
  value,
  model,
  onChange,
}: {
  label: string;
  value: Generation;
  model?: Model;
  onChange: (g: Generation) => void;
}) {
  const efforts = model?.reasoning_efforts ?? [
    "minimal",
    "low",
    "medium",
    "high",
    "xhigh",
    "max",
  ];
  const reasoning = [
    "default",
    ...(model?.has_reasoning && !model.reasoning_mandatory ? ["off"] : []),
    ...(model?.has_reasoning ? efforts : []),
  ];
  const unsupportedTemperature =
    value.temperature !== undefined &&
    !model?.parameters.includes("temperature");
  return (
    <div className="generation" role="group" aria-label={label}>
      <div className="form-grid">
        <Field label="Reasoning">
          <select
            value={value.reasoning}
            onChange={(e) => onChange({ ...value, reasoning: e.target.value })}
          >
            {!reasoning.includes(value.reasoning) && (
              <option value={value.reasoning}>{value.reasoning} (saved)</option>
            )}
            {reasoning.map((r) => (
              <option key={r} value={r}>
                {r === "default" ? "Model default" : r}
              </option>
            ))}
          </select>
        </Field>
        <div className="field">
          <span className="label">Answer length</span>
          <Segmented
            label="Answer length"
            value={value.answer_length}
            onChange={(answer_length) => onChange({ ...value, answer_length })}
            options={["short", "standard", "detailed"].map((v) => ({
              value: v,
              label: v[0].toUpperCase() + v.slice(1),
            }))}
          />
        </div>
        <Field label="Token limit">
          <input
            type="number"
            min={128}
            max={Math.min(model?.context_length || 8192, 8192)}
            value={value.tokens}
            onChange={(e) =>
              onChange({ ...value, tokens: Number(e.target.value) })
            }
          />
        </Field>
        {model?.parameters.includes("temperature") && (
          <Field label="Temperature" hint="Leave empty for the model default.">
            <input
              type="number"
              min={0}
              max={2}
              step={0.1}
              placeholder="Default"
              value={value.temperature ?? ""}
              onChange={(e) =>
                onChange({
                  ...value,
                  temperature:
                    e.target.value === "" ? undefined : Number(e.target.value),
                })
              }
            />
          </Field>
        )}
      </div>
      {unsupportedTemperature && (
        <p className="inline-note warn">
          This model doesn’t support the saved temperature.
          <button
            type="button"
            className="small ghost"
            onClick={() => onChange({ ...value, temperature: undefined })}
          >
            Clear temperature
          </button>
        </p>
      )}
    </div>
  );
}
