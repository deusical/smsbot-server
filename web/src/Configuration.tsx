import {
  useEffect,
  useRef,
  useState,
  cloneElement,
  isValidElement,
} from "react";
import {
  api,
  ApiError,
  type Configuration,
  type Generation,
  type Model,
  type Conversation,
  type Command,
} from "./api";
import { Icon } from "./Icon";

const defaultGeneration: Generation = {
  reasoning: "default",
  answer_length: "short",
  tokens: 2048,
};
type Props = {
  page: "Commands" | "Settings";
  conversations: Conversation[];
  busy: boolean;
  act: (fn: () => Promise<unknown>, refreshAfter?: boolean) => Promise<void>;
  onDirtyChange: (dirty: boolean) => void;
};
function normalize(c: Configuration): Configuration {
  return {
    ...c,
    commands: (c.commands ?? []).map((command) => ({
      ...command,
      allowed_conversations: command.allowed_conversations ?? [],
    })),
    profiles: c.profiles ?? [],
    settings: {
      ...c.settings,
      favorites: c.settings.favorites ?? [],
      model_generation: c.settings.model_generation ?? {},
    },
  };
}
function failure(e: unknown) {
  return e instanceof ApiError
    ? e.message.replaceAll("_", " ")
    : "Could not connect to the server.";
}
export function ConfigurationEditor({
  page,
  conversations,
  busy,
  act,
  onDirtyChange,
}: Props) {
  const [config, setConfig] = useState<Configuration | null>(null),
    [models, setModels] = useState<Model[]>([]),
    [error, setError] = useState(""),
    [notice, setNotice] = useState("");
  const [key, setKey] = useState(""),
    [configured, setConfigured] = useState(false),
    [selectedModel, setSelectedModel] = useState("");
  const [playCommand, setPlayCommand] = useState(""),
    [playInput, setPlayInput] = useState(""),
    [playResult, setPlayResult] = useState("");
  const [dirty, setDirty] = useState(false),
    [commandSearch, setCommandSearch] = useState(""),
    [commandFilter, setCommandFilter] = useState("all"),
    [modelSearch, setModelSearch] = useState("");
  const saved = useRef<Configuration | null>(null);
  useEffect(() => {
    onDirtyChange(dirty);
  }, [dirty, onDirtyChange]);
  useEffect(() => () => onDirtyChange(false), [onDirtyChange]);
  useEffect(() => {
    let active = true;
    Promise.all([
      api<Configuration>("/config"),
      api<Model[]>("/models"),
      api<{ configured: boolean }>("/provider-key"),
    ])
      .then(([c, m, k]) => {
        if (active) {
          saved.current = normalize(c);
          setConfig(saved.current);
          setModels(m ?? []);
          setConfigured(k.configured);
          setSelectedModel(c.settings.default_model);
        }
      })
      .catch((e) => {
        if (active) setError(failure(e));
      });
    return () => {
      active = false;
    };
  }, []);
  function edit(fn: (c: Configuration) => Configuration) {
    setNotice("");
    setDirty(true);
    setConfig((c) => (c ? fn(c) : c));
  }
  async function save() {
    if (!config) return;
    await act(async () => {
      const value = normalize(
        await api<Configuration>("/config", "PUT", config),
      );
      saved.current = value;
      setConfig(value);
      setDirty(false);
      setNotice("Configuration saved.");
    }, false);
  }
  function uniqueName(base: string) {
    const names = new Set(config?.commands.map((c) => c.name));
    let name = base.slice(0, 24),
      suffix = 2;
    while (names.has(name)) {
      const end = "_" + suffix++;
      name = base.slice(0, 24 - end.length) + end;
    }
    return name;
  }
  function addCommand(template?: Partial<Command>) {
    setCommandSearch("");
    setCommandFilter("all");
    edit((c) => ({
      ...c,
      commands: [
        ...c.commands,
        {
          name: template?.name ? uniqueName(template.name) : "",
          description: "",
          instructions: "",
          type: "fixed",
          enabled: false,
          model: "",
          profile_id: "",
          memory: false,
          approval: false,
          allowed_conversations: [],
          ...template,
          ...(template?.name ? { name: uniqueName(template.name) } : {}),
        },
      ],
    }));
  }
  function changeCommand(i: number, values: Partial<Command>) {
    edit((c) => ({
      ...c,
      commands: c.commands.map((command, index) =>
        index === i ? { ...command, ...values } : command,
      ),
    }));
  }
  if (!config)
    return (
      <section className="card">
        {error ? (
          <p role="alert" className="error">
            {error}
          </p>
        ) : (
          <p>Loading configuration…</p>
        )}
      </section>
    );
  const settings = config.settings;
  return (
    <>
      {error && (
        <p className="error" role="alert">
          {error}
        </p>
      )}
      {notice && (
        <p role="status" className="banner">
          {notice}
        </p>
      )}
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        <fieldset className="config-fields" disabled={busy}>
          {page === "Commands" ? (
            <>
              <section className="card">
                <div className="section-heading">
                  <div>
                    <p className="eyebrow">COMMAND LIBRARY</p>
                    <h2>Your shortcuts to useful.</h2>
                  </div>
                  <span className="count-badge">
                    {config.commands.length} custom
                  </span>
                </div>
                <p className="muted">
                  Fixed replies and AI instructions run only in enrolled
                  conversations. Queued requests keep the configuration used
                  when accepted.
                </p>
                <div className="template-grid">
                  <button
                    type="button"
                    className="template"
                    onClick={() =>
                      addCommand({
                        name: "hello",
                        description: "A simple greeting",
                        instructions: "Hello! {input}",
                        type: "fixed",
                      })
                    }
                  >
                    <Icon name="conversations" />
                    <strong>Quick reply</strong>
                    <small>A reliable response, no AI needed.</small>
                  </button>
                  <button
                    type="button"
                    className="template"
                    onClick={() =>
                      addCommand({
                        name: "ask",
                        description: "A helpful assistant",
                        instructions:
                          "Answer the request clearly and concisely. Preserve exact facts and constraints.",
                        type: "ai",
                        approval: true,
                      })
                    }
                  >
                    <Icon name="spark" />
                    <strong>AI assistant</strong>
                    <small>Your instructions, with approval first.</small>
                  </button>
                  <button
                    type="button"
                    className="template"
                    onClick={() =>
                      addCommand({
                        name: "translate",
                        description: "Translate text",
                        instructions:
                          "Translate the input into the language requested. Return only the translation.",
                        type: "ai",
                        approval: true,
                      })
                    }
                  >
                    <Icon name="commands" />
                    <strong>Translator</strong>
                    <small>Make a language barrier a little smaller.</small>
                  </button>
                </div>
                <div className="command-overview">
                  <span>
                    <strong>
                      {config.commands.filter((c) => c.enabled).length}
                    </strong>{" "}
                    enabled
                  </span>
                  <span>
                    <strong>
                      {config.commands.filter((c) => c.approval).length}
                    </strong>{" "}
                    require approval
                  </span>
                  <span>
                    <strong>{config.profiles.length}</strong> model profiles
                  </span>
                </div>
                <div className="toolbar">
                  <label className="search-control">
                    <Icon name="search" />
                    <input
                      aria-label="Search commands"
                      type="search"
                      placeholder="Find a command"
                      value={commandSearch}
                      onChange={(e) => setCommandSearch(e.target.value)}
                    />
                  </label>
                  <div
                    className="filter-tabs"
                    role="group"
                    aria-label="Filter commands"
                  >
                    {["all", "ai", "fixed"].map((f) => (
                      <button
                        key={f}
                        type="button"
                        className={commandFilter === f ? "selected" : ""}
                        aria-pressed={commandFilter === f}
                        onClick={() => setCommandFilter(f)}
                      >
                        {f === "all"
                          ? "All commands"
                          : f === "ai"
                            ? "AI"
                            : "Fixed"}
                      </button>
                    ))}
                  </div>
                </div>
                {config.commands.map((command, i) =>
                  (commandFilter === "all" || command.type === commandFilter) &&
                  `${command.name} ${command.description}`
                    .toLowerCase()
                    .includes(commandSearch.toLowerCase()) ? (
                    <details className="editor" key={i} open>
                      <summary>
                        <code>!{command.name || "new-command"}</code>
                        <span>
                          {command.enabled ? "Enabled" : "Disabled"} ·{" "}
                          {command.type.toUpperCase()}
                        </span>
                      </summary>
                      <div className="form-grid">
                        <Field label={`Command ${i + 1} name`}>
                          <input
                            required
                            pattern="[a-z][a-z0-9_-]{0,23}"
                            maxLength={24}
                            value={command.name}
                            onChange={(e) =>
                              changeCommand(i, { name: e.target.value })
                            }
                          />
                        </Field>
                        <Field label={`Command ${i + 1} description`}>
                          <input
                            value={command.description}
                            onChange={(e) =>
                              changeCommand(i, { description: e.target.value })
                            }
                          />
                        </Field>
                        <Field label={`Command ${i + 1} type`}>
                          <select
                            value={command.type}
                            onChange={(e) =>
                              changeCommand(i, {
                                type: e.target.value as "ai" | "fixed",
                              })
                            }
                          >
                            <option value="fixed">Fixed reply</option>
                            <option value="ai">AI command</option>
                          </select>
                        </Field>
                        <Check
                          label={`Enable command ${i + 1}`}
                          checked={command.enabled}
                          onChange={(enabled) => changeCommand(i, { enabled })}
                        />
                      </div>
                      <Field
                        label={
                          command.type === "fixed"
                            ? `Command ${i + 1} reply`
                            : `Command ${i + 1} instructions`
                        }
                      >
                        <textarea
                          required
                          value={command.instructions}
                          onChange={(e) =>
                            changeCommand(i, { instructions: e.target.value })
                          }
                        />
                      </Field>
                      <Check
                        label={`Command ${i + 1} approval`}
                        checked={command.approval}
                        onChange={(approval) => changeCommand(i, { approval })}
                      />
                      {command.type === "ai" && (
                        <>
                          <div className="form-grid">
                            <ModelSelect
                              label={`Command ${i + 1} model`}
                              value={command.model}
                              models={models}
                              allowDefault
                              onChange={(model) => changeCommand(i, { model })}
                            />
                            <Field label={`Command ${i + 1} profile`}>
                              <select
                                value={command.profile_id}
                                onChange={(e) =>
                                  changeCommand(i, {
                                    profile_id: e.target.value,
                                  })
                                }
                              >
                                <option value="">No profile</option>
                                {config.profiles.map((p) => (
                                  <option key={p.id} value={p.id}>
                                    {p.name}
                                  </option>
                                ))}
                              </select>
                            </Field>
                            <Check
                              label={`Command ${i + 1} memory`}
                              checked={command.memory}
                              onChange={(memory) =>
                                changeCommand(i, { memory })
                              }
                            />
                          </div>
                          <p className="muted">
                            Memory is opt-in. Approvals wait in Activity before
                            submission.
                          </p>
                          <Check
                            label={`Override command ${i + 1} generation`}
                            checked={!!command.generation}
                            onChange={(override) =>
                              changeCommand(i, {
                                generation: override
                                  ? { ...defaultGeneration }
                                  : undefined,
                              })
                            }
                          />
                          {command.generation && (
                            <GenerationEditor
                              label={`Command ${i + 1}`}
                              value={command.generation}
                              model={models.find(
                                (m) =>
                                  m.id ===
                                  (command.model ||
                                    config.profiles.find(
                                      (p) => p.id === command.profile_id,
                                    )?.model ||
                                    settings.default_model),
                              )}
                              onChange={(generation) =>
                                changeCommand(i, { generation })
                              }
                            />
                          )}
                        </>
                      )}
                      <fieldset>
                        <legend>
                          Restrict command {i + 1} to enrolled conversations
                        </legend>
                        <p className="muted">
                          No selection allows all enrolled conversations.
                        </p>
                        {conversations
                          .filter((c) => c.enrolled)
                          .map((c) => (
                            <Check
                              key={c.id}
                              label={`${command.name}: ${c.label}`}
                              checked={command.allowed_conversations.includes(
                                c.id,
                              )}
                              onChange={(checked) =>
                                changeCommand(i, {
                                  allowed_conversations: checked
                                    ? [...command.allowed_conversations, c.id]
                                    : command.allowed_conversations.filter(
                                        (id) => id !== c.id,
                                      ),
                                })
                              }
                            />
                          ))}
                      </fieldset>
                      <div className="actions">
                        <button
                          type="button"
                          className="secondary"
                          disabled={busy}
                          onClick={() =>
                            addCommand({
                              ...structuredClone(command),
                              name: uniqueName(
                                (command.name || "command") + "_copy",
                              ),
                              enabled: false,
                            })
                          }
                        >
                          <Icon name="copy" size={15} />
                          Duplicate command {i + 1}
                        </button>
                        <button
                          type="button"
                          className="secondary"
                          disabled={busy}
                          onClick={() =>
                            edit((c) => ({
                              ...c,
                              commands: c.commands.filter(
                                (_, index) => index !== i,
                              ),
                            }))
                          }
                        >
                          Delete command {i + 1}
                        </button>
                      </div>
                    </details>
                  ) : null,
                )}
                {config.commands.length === 0 && (
                  <div className="empty">
                    <Icon name="commands" size={26} />
                    <strong>A command is a small superpower.</strong>
                    <p>
                      Start with a template above, or write your own. New
                      commands start disabled.
                    </p>
                  </div>
                )}
                <button
                  type="button"
                  className="secondary"
                  onClick={() => addCommand()}
                >
                  <Icon name="plus" size={16} />
                  Add command
                </button>
                <div className="builtin-section">
                  <p className="muted">
                    Built-in commands are always managed by the server.
                  </p>
                  <div className="builtin-list">
                    {["ping", "help", "status", "new", "more"].map((name) => (
                      <code key={name}>!{name}</code>
                    ))}
                  </div>
                </div>
              </section>
              <section className="card">
                <h2>Model profiles</h2>
                {config.profiles.map((profile, i) => (
                  <div className="editor" key={profile.id}>
                    <div className="form-grid">
                      <Field label={`Profile ${i + 1} name`}>
                        <input
                          required
                          value={profile.name}
                          onChange={(e) =>
                            edit((c) => ({
                              ...c,
                              profiles: c.profiles.map((p, index) =>
                                index === i
                                  ? { ...p, name: e.target.value }
                                  : p,
                              ),
                            }))
                          }
                        />
                      </Field>
                      <ModelSelect
                        label={`Profile ${i + 1} model`}
                        value={profile.model}
                        models={models}
                        allowDefault
                        onChange={(model) =>
                          edit((c) => ({
                            ...c,
                            profiles: c.profiles.map((p, index) =>
                              index === i ? { ...p, model } : p,
                            ),
                          }))
                        }
                      />
                    </div>
                    <Field label={`Profile ${i + 1} instructions`}>
                      <textarea
                        value={profile.instructions}
                        onChange={(e) =>
                          edit((c) => ({
                            ...c,
                            profiles: c.profiles.map((p, index) =>
                              index === i
                                ? { ...p, instructions: e.target.value }
                                : p,
                            ),
                          }))
                        }
                      />
                    </Field>
                    <button
                      type="button"
                      className="secondary"
                      onClick={() =>
                        edit((c) => ({
                          ...c,
                          profiles: c.profiles.filter(
                            (p) => p.id !== profile.id,
                          ),
                          commands: c.commands.map((command) =>
                            command.profile_id === profile.id
                              ? { ...command, profile_id: "" }
                              : command,
                          ),
                        }))
                      }
                    >
                      Delete profile {i + 1}
                    </button>
                  </div>
                ))}
                <button
                  type="button"
                  className="secondary"
                  onClick={() =>
                    edit((c) => ({
                      ...c,
                      profiles: [
                        ...c.profiles,
                        {
                          id: crypto.randomUUID(),
                          name: "",
                          model: "",
                          instructions: "",
                        },
                      ],
                    }))
                  }
                >
                  Add profile
                </button>
              </section>
            </>
          ) : (
            <>
              <section className="card">
                <h2>Models & generation</h2>
                <ModelSelect
                  label="Default model"
                  value={settings.default_model}
                  models={models}
                  onChange={(default_model) =>
                    edit((c) => ({
                      ...c,
                      settings: { ...c.settings, default_model },
                    }))
                  }
                />
                <button
                  type="button"
                  className="secondary"
                  disabled={busy}
                  onClick={() =>
                    void act(async () => {
                      setModels(await api<Model[]>("/models/refresh", "POST"));
                      setNotice("Model catalog refreshed.");
                    }, false)
                  }
                >
                  Refresh model catalog
                </button>
                <p className="muted">
                  Refreshing contacts OpenRouter for current model capabilities.
                  Saved preferences remain per model.
                </p>
                <ModelSelect
                  label="Generation model"
                  value={selectedModel}
                  models={models}
                  onChange={setSelectedModel}
                />
                {selectedModel && (
                  <GenerationEditor
                    label="Model"
                    value={
                      settings.model_generation[selectedModel] ??
                      defaultGeneration
                    }
                    model={models.find((m) => m.id === selectedModel)}
                    onChange={(generation) =>
                      edit((c) => ({
                        ...c,
                        settings: {
                          ...c.settings,
                          model_generation: {
                            ...c.settings.model_generation,
                            [selectedModel]: generation,
                          },
                        },
                      }))
                    }
                  />
                )}
                <fieldset>
                  <legend>Favorite models</legend>
                  <label className="search-control">
                    <Icon name="search" />
                    <input
                      aria-label="Search favorite models"
                      type="search"
                      placeholder="Search by model or provider"
                      value={modelSearch}
                      onChange={(e) => setModelSearch(e.target.value)}
                    />
                  </label>
                  <div className="favorite-list">
                    {models
                      .filter((m) =>
                        `${m.name} ${m.id}`
                          .toLowerCase()
                          .includes(modelSearch.toLowerCase()),
                      )
                      .slice(0, 60)
                      .map((m) => (
                        <Check
                          key={m.id}
                          label={m.name}
                          checked={settings.favorites.includes(m.id)}
                          onChange={(checked) =>
                            edit((c) => ({
                              ...c,
                              settings: {
                                ...c.settings,
                                favorites: checked
                                  ? [...c.settings.favorites, m.id]
                                  : c.settings.favorites.filter(
                                      (id) => id !== m.id,
                                    ),
                              },
                            }))
                          }
                        />
                      ))}
                  </div>
                  <small>
                    Showing up to 60 matches. Narrow your search to find more
                    models.
                  </small>
                </fieldset>
              </section>
              <section className="card">
                <h2>Limits, memory & retention</h2>
                <div className="form-grid">
                  {(
                    [
                      {
                        key: "hourly_limit",
                        label: "Hourly reply limit",
                        min: 1,
                      },
                      {
                        key: "daily_limit",
                        label: "Daily reply limit",
                        min: 1,
                      },
                      {
                        key: "daily_budget",
                        label: "Daily AI budget (USD)",
                        min: 0,
                        step: 0.01,
                      },
                      {
                        key: "memory_hours",
                        label: "Memory window (hours)",
                        min: 1,
                      },
                      {
                        key: "retention_days",
                        label: "Retain history (days)",
                        min: 1,
                      },
                      {
                        key: "max_chars",
                        label: "Maximum reply characters",
                        min: 1,
                      },
                    ] as const
                  ).map((field) => (
                    <Field key={field.key} label={field.label}>
                      <input
                        type="number"
                        required
                        min={field.min}
                        step={"step" in field ? field.step : 1}
                        value={settings[field.key]}
                        onChange={(e) =>
                          edit((c) => ({
                            ...c,
                            settings: {
                              ...c.settings,
                              [field.key]: Number(e.target.value),
                            },
                          }))
                        }
                      />
                    </Field>
                  ))}
                </div>
                <Check
                  label="Enable quiet hours"
                  checked={settings.quiet_enabled}
                  onChange={(quiet_enabled) =>
                    edit((c) => ({
                      ...c,
                      settings: { ...c.settings, quiet_enabled },
                    }))
                  }
                />
                <div className="form-grid">
                  <Field label="Quiet hours start">
                    <select
                      value={settings.quiet_start}
                      onChange={(e) =>
                        edit((c) => ({
                          ...c,
                          settings: {
                            ...c.settings,
                            quiet_start: Number(e.target.value),
                          },
                        }))
                      }
                    >
                      {Array.from({ length: 24 }, (_, h) => (
                        <option key={h} value={h}>
                          {String(h).padStart(2, "0")}:00
                        </option>
                      ))}
                    </select>
                  </Field>
                  <Field label="Quiet hours end">
                    <select
                      value={settings.quiet_end}
                      onChange={(e) =>
                        edit((c) => ({
                          ...c,
                          settings: {
                            ...c.settings,
                            quiet_end: Number(e.target.value),
                          },
                        }))
                      }
                    >
                      {Array.from({ length: 24 }, (_, h) => (
                        <option key={h} value={h}>
                          {String(h).padStart(2, "0")}:00
                        </option>
                      ))}
                    </select>
                  </Field>
                  <Field label="Time zone">
                    <input
                      required
                      placeholder="America/Los_Angeles"
                      value={settings.timezone}
                      onChange={(e) =>
                        edit((c) => ({
                          ...c,
                          settings: { ...c.settings, timezone: e.target.value },
                        }))
                      }
                    />
                  </Field>
                </div>
              </section>
            </>
          )}
        </fieldset>
        <div className="save-row">
          <small>
            {dirty
              ? "Unsaved changes · New requests use the last saved configuration."
              : "Configuration is up to date."}
          </small>
          {dirty && (
            <button
              disabled={busy}
              type="button"
              className="secondary"
              onClick={() => {
                if (saved.current) setConfig(structuredClone(saved.current));
                setDirty(false);
                setNotice("Draft discarded.");
              }}
            >
              Discard draft
            </button>
          )}
          <button disabled={busy} type="submit">
            <Icon name="check" size={16} />
            Save configuration
          </button>
        </div>
      </form>
      {page === "Commands" && (
        <section className="card">
          <h2>Offline playground</h2>
          <p className="muted">
            Preview saved commands without sending a message or calling a
            provider.
          </p>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              void act(async () => {
                const result = await api<{
                  text: string;
                  model: string;
                  simulated: boolean;
                }>("/playground", "POST", {
                  command: playCommand,
                  input: playInput,
                });
                if (!result.simulated)
                  throw new ApiError(502, "simulation_not_confirmed");
                setPlayResult(result.text);
              }, false);
            }}
          >
            <Field label="Playground command">
              <select
                required
                value={playCommand}
                onChange={(e) => setPlayCommand(e.target.value)}
              >
                <option value="">Choose a saved command</option>
                {config.commands.map((c) => (
                  <option key={c.name} value={c.name}>
                    !{c.name}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="Playground input">
              <textarea
                value={playInput}
                onChange={(e) => setPlayInput(e.target.value)}
              />
            </Field>
            <button disabled={busy || !playCommand}>Preview command</button>
          </form>
          {playResult && <pre role="status">{playResult}</pre>}
        </section>
      )}
      {page === "Settings" && (
        <>
          <section className="card">
            <h2>OpenRouter key</h2>
            <p>
              {configured
                ? "A provider key is configured."
                : "No provider key is configured."}
            </p>
            <p className="muted">
              The key is encrypted on the server and is never returned. AI
              command input and opted-in memory are sent to OpenRouter.
            </p>
            <form
              onSubmit={(e) => {
                e.preventDefault();
                void act(async () => {
                  await api("/provider-key", "PUT", { key });
                  setKey("");
                  setConfigured(!!key.trim());
                  setNotice("Provider key saved.");
                }, false);
              }}
            >
              <Field label="OpenRouter API key">
                <input
                  type="password"
                  autoComplete="off"
                  required
                  value={key}
                  onChange={(e) => setKey(e.target.value)}
                />
              </Field>
              <button disabled={busy || !key}>Save provider key</button>
            </form>
          </section>
          <section className="card">
            <h2>Recipes</h2>
            <p className="muted">
              Recipes exclude conversation restrictions, sender identities and
              keys. Imported commands start disabled.
            </p>
            <div className="actions">
              <button
                className="secondary"
                disabled={busy}
                onClick={() =>
                  void act(async () => {
                    const recipe = await api<Configuration>("/recipes/export");
                    const url = URL.createObjectURL(
                      new Blob([JSON.stringify(recipe, null, 2)], {
                        type: "application/json",
                      }),
                    );
                    const link = document.createElement("a");
                    link.href = url;
                    link.download = "smsbot-recipe.json";
                    link.click();
                    setTimeout(() => URL.revokeObjectURL(url), 1000);
                  }, false)
                }
              >
                Export recipe
              </button>
              <label className="file-label">
                Import recipe
                <input
                  type="file"
                  accept="application/json,.json"
                  disabled={busy}
                  onChange={(e) => {
                    const file = e.target.files?.[0];
                    e.target.value = "";
                    if (file)
                      void act(async () => {
                        if (file.size > 1024 * 1024)
                          throw new ApiError(400, "recipe_too_large");
                        if (
                          dirty &&
                          !window.confirm(
                            "Importing replaces your saved configuration and unsaved draft. Continue?",
                          )
                        )
                          return;
                        const configuration = JSON.parse(await file.text());
                        const imported = await api<Configuration>(
                          "/recipes/import",
                          "POST",
                          { configuration },
                        );
                        const value = normalize(imported);
                        saved.current = value;
                        setConfig(value);
                        setDirty(false);
                        setSelectedModel(value.settings.default_model);
                        setNotice(
                          "Recipe imported. Commands are disabled; review before enabling.",
                        );
                      }, false);
                  }}
                />
              </label>
            </div>
          </section>
        </>
      )}
    </>
  );
}
function Field({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <label className="field">
      {label}
      {isValidElement(children)
        ? cloneElement(
            children as React.ReactElement<{ "aria-label": string }>,
            { "aria-label": label },
          )
        : children}
    </label>
  );
}
function Check({
  label,
  checked,
  onChange,
}: {
  label: string;
  checked: boolean;
  onChange: (checked: boolean) => void;
}) {
  return (
    <label className="check">
      <input
        type="checkbox"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
      />
      {label}
    </label>
  );
}
function ModelSelect({
  label,
  value,
  models,
  onChange,
  allowDefault = false,
}: {
  label: string;
  value: string;
  models: Model[];
  onChange: (id: string) => void;
  allowDefault?: boolean;
}) {
  const [search, setSearch] = useState("");
  const matches = models
    .filter((m) =>
      `${m.name} ${m.id}`.toLowerCase().includes(search.toLowerCase()),
    )
    .slice(0, 60);
  const selected = models.find((m) => m.id === value);
  return (
    <div className="model-search">
      <Field label={`Search ${label.toLowerCase()}`}>
        <input
          type="search"
          placeholder="Search model or provider"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
      </Field>
      <Field label={label}>
        <select value={value} onChange={(e) => onChange(e.target.value)}>
          <option value="">
            {allowDefault ? "Use default model" : "Choose a model"}
          </option>
          {value && !matches.some((m) => m.id === value) && (
            <option value={value}>
              {selected?.name ?? value}
              {selected ? "" : " (saved)"}
            </option>
          )}
          {matches.map((m) => (
            <option key={m.id} value={m.id}>
              {m.name}
            </option>
          ))}
        </select>
      </Field>
      {models.length > 60 && (
        <small>Search to find models beyond the first 60 results.</small>
      )}
    </div>
  );
}
function GenerationEditor({
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
  return (
    <div className="form-grid">
      <Field label={`${label} reasoning`}>
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
      <Field label={`${label} answer length`}>
        <select
          value={value.answer_length}
          onChange={(e) =>
            onChange({ ...value, answer_length: e.target.value })
          }
        >
          {["short", "standard", "detailed"].map((v) => (
            <option key={v} value={v}>
              {v}
            </option>
          ))}
        </select>
      </Field>
      <Field label={`${label} token limit`}>
        <input
          type="number"
          min={128}
          max={Math.min(model?.context_length || 8192, 8192)}
          required
          value={value.tokens}
          onChange={(e) =>
            onChange({ ...value, tokens: Number(e.target.value) })
          }
        />
      </Field>
      {model?.parameters.includes("temperature") && (
        <Field label={`${label} temperature (optional)`}>
          <input
            type="number"
            min={0}
            max={2}
            step={0.1}
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
      {value.temperature !== undefined &&
        !model?.parameters.includes("temperature") && (
          <div>
            <p className="muted">
              The saved temperature is unsupported. Clear it or choose a
              compatible model.
            </p>
            <button
              type="button"
              className="secondary"
              onClick={() => onChange({ ...value, temperature: undefined })}
            >
              Clear unsupported temperature
            </button>
          </div>
        )}
    </div>
  );
}
