import {
  cloneElement,
  isValidElement,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type ReactElement,
  type ReactNode,
} from "react";
import type { Model } from "./api";
import { Icon } from "./Icon";

export function Field({
  label,
  hint,
  children,
  className = "",
  unit,
}: {
  label: string;
  hint?: ReactNode;
  children: ReactNode;
  className?: string;
  unit?: string;
}) {
  const id = useId();
  const control = isValidElement(children)
    ? cloneElement(children as ReactElement<{ id: string }>, { id })
    : children;
  return (
    <div className={"field " + className}>
      <label htmlFor={id}>{label}</label>
      {unit ? (
        <span className="unit-input" data-unit={unit}>
          {control}
        </span>
      ) : (
        control
      )}
      {hint && <small className="hint">{hint}</small>}
    </div>
  );
}

export function Switch({
  label,
  checked,
  onChange,
  disabled = false,
  hint,
  hideLabel = false,
}: {
  label: string;
  checked: boolean;
  onChange: (checked: boolean) => void;
  disabled?: boolean;
  hint?: ReactNode;
  hideLabel?: boolean;
}) {
  const id = useId();
  return (
    <label className={"switch-row" + (hideLabel ? " bare" : "")}>
      <input
        type="checkbox"
        role="switch"
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
        aria-label={hideLabel ? label : undefined}
        aria-labelledby={hideLabel ? undefined : id + "-label"}
        aria-describedby={hint && !hideLabel ? id + "-hint" : undefined}
      />
      <span className="track" aria-hidden="true" />
      {!hideLabel && (
        <span className="switch-text">
          <span id={id + "-label"}>{label}</span>
          {hint && <small id={id + "-hint"}>{hint}</small>}
        </span>
      )}
    </label>
  );
}

export function Segmented<T extends string>({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: T;
  options: { value: T; label: ReactNode; count?: number }[];
  onChange: (value: T) => void;
}) {
  return (
    <div className="segmented" role="group" aria-label={label}>
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          aria-pressed={value === o.value}
          onClick={() => onChange(o.value)}
        >
          {o.label}
          {o.count !== undefined && <span className="count">{o.count}</span>}
        </button>
      ))}
    </div>
  );
}

export function modelName(model: Model) {
  return model.provider === "compatible"
    ? `${model.name} · Compatible API`
    : model.name;
}

/** Searchable model combobox; favorites float to the top and can be starred inline. */
export function ModelPicker({
  label,
  value,
  models,
  onChange,
  favorites = [],
  onToggleFavorite,
  defaultLabel,
  hint,
}: {
  label: string;
  value: string;
  models: Model[];
  onChange: (id: string) => void;
  favorites?: string[];
  onToggleFavorite?: (id: string) => void;
  defaultLabel?: string;
  hint?: ReactNode;
}) {
  const id = useId();
  const [open, setOpen] = useState(false),
    [search, setSearch] = useState(""),
    [active, setActive] = useState(0);
  const root = useRef<HTMLDivElement>(null),
    input = useRef<HTMLInputElement>(null);
  const selected = models.find((m) => m.id === value);
  const matches = useMemo(() => {
    const q = search.toLowerCase();
    const found = models.filter((m) =>
      `${m.name} ${m.id} ${m.provider ?? ""}`.toLowerCase().includes(q),
    );
    found.sort(
      (a, b) => +favorites.includes(b.id) - +favorites.includes(a.id),
    );
    return found.slice(0, 80);
  }, [models, search, favorites]);
  const options: { id: string; label: string; model?: Model }[] = [
    ...(defaultLabel !== undefined ? [{ id: "", label: defaultLabel }] : []),
    ...matches.map((m) => ({ id: m.id, label: modelName(m), model: m })),
  ];
  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", close);
    input.current?.focus();
    return () => document.removeEventListener("mousedown", close);
  }, [open]);
  useEffect(() => setActive(0), [search]);
  function choose(next: string) {
    onChange(next);
    setOpen(false);
    setSearch("");
  }
  const display = selected
    ? modelName(selected)
    : value
      ? `${value} (saved)`
      : (defaultLabel ?? "Choose a model");
  return (
    <div className="field model-picker" ref={root}>
      <span className="label" id={id + "-label"}>
        {label}
      </span>
      <button
        type="button"
        className="picker-trigger"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-labelledby={id + "-label " + id + "-value"}
        onClick={() => setOpen(!open)}
      >
        <span id={id + "-value"} className={value ? "" : "placeholder"}>
          {display}
        </span>
        <Icon name="chevron" size={16} />
      </button>
      {hint && <small className="hint">{hint}</small>}
      {open && (
        <div className="picker-pop">
          <label className="picker-search">
            <Icon name="search" size={16} />
            <input
              ref={input}
              type="search"
              aria-label={`Search ${label.toLowerCase()}`}
              aria-controls={id + "-list"}
              placeholder="Search models or providers"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "ArrowDown") {
                  e.preventDefault();
                  setActive((a) => Math.min(a + 1, options.length - 1));
                } else if (e.key === "ArrowUp") {
                  e.preventDefault();
                  setActive((a) => Math.max(a - 1, 0));
                } else if (e.key === "Enter") {
                  e.preventDefault();
                  if (options[active]) choose(options[active].id);
                } else if (e.key === "Escape") {
                  e.stopPropagation();
                  setOpen(false);
                }
              }}
            />
          </label>
          <ul role="listbox" id={id + "-list"} aria-label={label}>
            {options.map((o, i) => (
              <li
                key={o.id || "default"}
                role="option"
                aria-selected={o.id === value}
                className={i === active ? "active" : ""}
                onMouseEnter={() => setActive(i)}
                onClick={() => choose(o.id)}
              >
                <span className="option-name">{o.label}</span>
                {o.model && (
                  <span className="option-meta">
                    {o.model.has_reasoning && <span>reasoning</span>}
                    {o.model.context_length > 0 && (
                      <span>
                        {Math.round(o.model.context_length / 1000)}k
                      </span>
                    )}
                  </span>
                )}
                {o.model && onToggleFavorite && (
                  <button
                    type="button"
                    className={
                      "star" + (favorites.includes(o.id) ? " on" : "")
                    }
                    aria-label={
                      (favorites.includes(o.id) ? "Unfavorite " : "Favorite ") +
                      o.label
                    }
                    onClick={(e) => {
                      e.stopPropagation();
                      onToggleFavorite(o.id);
                    }}
                  >
                    <Icon
                      name="star"
                      size={15}
                      filled={favorites.includes(o.id)}
                    />
                  </button>
                )}
              </li>
            ))}
            {!matches.length && (
              <li className="picker-empty">No models match “{search}”.</li>
            )}
          </ul>
          {models.length > 80 && matches.length === 80 && (
            <small className="picker-foot">
              Showing 80 of {models.length}. Keep typing to narrow.
            </small>
          )}
        </div>
      )}
    </div>
  );
}

export function Avatar({ label, tone }: { label: string; tone?: string }) {
  let hash = 0;
  for (const ch of label) hash = (hash * 31 + ch.charCodeAt(0)) % 360;
  return (
    <span
      className={"avatar " + (tone ?? "")}
      aria-hidden="true"
      style={{ "--hue": hash } as React.CSSProperties}
    >
      {label.trim().slice(0, 1).toUpperCase() || "?"}
    </span>
  );
}

export function timeAgo(iso: string) {
  const seconds = (Date.now() - Date.parse(iso)) / 1000;
  if (seconds < 45) return "just now";
  if (seconds < 3600) return `${Math.round(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.round(seconds / 3600)}h ago`;
  return new Date(iso).toLocaleDateString([], {
    month: "short",
    day: "numeric",
  });
}

export function Group({
  title,
  description,
  aside,
  children,
  className = "",
}: {
  title: string;
  description?: ReactNode;
  aside?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={"panel group " + className}>
      <div className="panel-head">
        <div>
          <h2>{title}</h2>
          {description && <p className="muted">{description}</p>}
        </div>
        {aside}
      </div>
      {children}
    </section>
  );
}

export type Tone = "ok" | "busy" | "wait" | "bad" | "idle";

export function StatusPill({ tone, children }: { tone: Tone; children: ReactNode }) {
  return (
    <span className={"pill status-pill tone-" + tone}>
      <span className="state-dot" aria-hidden="true" />
      {children}
    </span>
  );
}

export function Notice({
  tone = "wait",
  role,
  children,
}: {
  tone?: "wait" | "bad" | "ok";
  role?: "alert" | "status";
  children: ReactNode;
}) {
  return (
    <div className={"banner notice-" + tone} role={role}>
      {children}
    </div>
  );
}
