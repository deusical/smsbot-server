import { useEffect, useRef, useState } from "react";
import { api, ApiError, type Command, type Conversation } from "../lib/api";
import { GenerationEditor } from "../components/Generation";
import { Icon } from "../components/Icon";
import { Field, ModelPicker, modelName, Segmented, Switch } from "../components/ui";
import { defaultGeneration, failure, type Workspace } from "../lib/workspace";

const builtins = [
  { name: "ping", detail: "Check the bot is listening" },
  { name: "help", detail: "List available commands" },
  { name: "status", detail: "Show bot status" },
  { name: "new", detail: "Start a fresh memory" },
  { name: "more", detail: "Continue the last reply" },
];
const templates: { label: string; detail: string; command: Partial<Command> }[] = [
  {
    label: "Blank",
    detail: "Start from scratch",
    command: {},
  },
  {
    label: "Quick reply",
    detail: "Same text every time, no AI",
    command: {
      name: "hello",
      description: "A simple greeting",
      instructions: "Hello! {input}",
      type: "fixed",
    },
  },
  {
    label: "AI assistant",
    detail: "Answers questions, asks you first",
    command: {
      name: "ask",
      description: "A helpful assistant",
      instructions:
        "Answer the request clearly and concisely. Preserve exact facts and constraints.",
      type: "ai",
      approval: true,
    },
  },
  {
    label: "Translator",
    detail: "Translates into a requested language",
    command: {
      name: "translate",
      description: "Translate text",
      instructions:
        "Translate the input into the language requested. Return only the translation.",
      type: "ai",
      approval: true,
    },
  },
];

export function CommandsPage({
  workspace,
  conversations,
  busy,
}: {
  workspace: Workspace;
  conversations: Conversation[];
  busy: boolean;
}) {
  const { config, saved, models, edit } = workspace;
  const [tab, setTab] = useState<"commands" | "profiles">("commands"),
    [selected, setSelected] = useState(0),
    [search, setSearch] = useState(""),
    [menu, setMenu] = useState(false),
    [mobileDetail, setMobileDetail] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!menu) return;
    const close = (e: MouseEvent) => {
      if (!menuRef.current?.contains(e.target as Node)) setMenu(false);
    };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [menu]);
  if (!config)
    return (
      <div className="panel loading-panel">
        {workspace.loadError ? (
          <p role="alert" className="inline-alert">
            {workspace.loadError}
          </p>
        ) : (
          <p className="muted">Loading commands…</p>
        )}
      </div>
    );
  const commands = config.commands;
  const index = Math.min(selected, commands.length - 1);
  const command = commands[index];
  function uniqueName(base: string) {
    const names = new Set(commands.map((c) => c.name));
    let name = base.slice(0, 24),
      suffix = 2;
    while (names.has(name)) {
      const end = "_" + suffix++;
      name = base.slice(0, 24 - end.length) + end;
    }
    return name;
  }
  function addCommand(template: Partial<Command> = {}) {
    setSearch("");
    setMenu(false);
    edit((c) => ({
      ...c,
      commands: [
        ...c.commands,
        {
          name: "",
          description: "",
          instructions: "",
          type: "fixed",
          model: "",
          profile_id: "",
          memory: false,
          approval: false,
          allowed_conversations: [],
          ...template,
          ...(template.name ? { name: uniqueName(template.name) } : {}),
          enabled: false,
        },
      ],
    }));
    setSelected(commands.length);
    setMobileDetail(true);
  }
  function change(i: number, values: Partial<Command>) {
    edit((c) => ({
      ...c,
      commands: c.commands.map((x, n) => (n === i ? { ...x, ...values } : x)),
    }));
  }
  const filtered = commands
    .map((c, i) => ({ c, i }))
    .filter(({ c }) =>
      `${c.name} ${c.description}`.toLowerCase().includes(search.toLowerCase()),
    );
  return (
    <div className="page-stack">
      <div className="page-tabs">
        <Segmented
          label="Command sections"
          value={tab}
          onChange={setTab}
          options={[
            { value: "commands", label: "Commands", count: commands.length },
            {
              value: "profiles",
              label: "Model profiles",
              count: config.profiles.length,
            },
          ]}
        />
      </div>
      {tab === "profiles" ? (
        <Profiles workspace={workspace} />
      ) : (
        <div className={"studio" + (mobileDetail ? " show-detail" : "")}>
          <aside className="studio-list panel">
            <div className="list-head">
              <label className="search">
                <Icon name="search" size={16} />
                <input
                  aria-label="Search commands"
                  type="search"
                  placeholder="Search"
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                />
              </label>
              <div className="menu-wrap" ref={menuRef}>
                <button
                  type="button"
                  className="primary"
                  aria-expanded={menu}
                  aria-haspopup="menu"
                  onClick={() => setMenu(!menu)}
                >
                  <Icon name="plus" size={16} />
                  New
                </button>
                {menu && (
                  <div className="menu" role="menu">
                    {templates.map((t) => (
                      <button
                        key={t.label}
                        type="button"
                        role="menuitem"
                        onClick={() => addCommand(structuredClone(t.command))}
                      >
                        <strong>{t.label}</strong>
                        <small>{t.detail}</small>
                      </button>
                    ))}
                  </div>
                )}
              </div>
            </div>
            <ul className="command-list">
              {filtered.map(({ c, i }) => (
                <li
                  key={i}
                  className={
                    (i === index ? "selected " : "") +
                    (c.enabled ? "live" : "off")
                  }
                >
                  <button
                    type="button"
                    className="command-item"
                    aria-current={i === index ? "true" : undefined}
                    onClick={() => {
                      setSelected(i);
                      setMobileDetail(true);
                    }}
                  >
                    <code className="bang">!{c.name || "untitled"}</code>
                    <small>
                      {c.description || (c.type === "ai" ? "AI command" : "Fixed reply")}
                    </small>
                  </button>
                  <span className={"type-tag " + c.type}>
                    {c.type === "ai" ? "AI" : "Text"}
                  </span>
                  <Switch
                    label={`Enable !${c.name || "untitled"}`}
                    hideLabel
                    checked={c.enabled}
                    onChange={(enabled) => change(i, { enabled })}
                  />
                </li>
              ))}
            </ul>
            {!commands.length && (
              <div className="empty small">
                <strong>No commands yet</strong>
                <p>
                  Pick a template from New. Commands start switched off until
                  you enable them.
                </p>
              </div>
            )}
            {commands.length > 0 && !filtered.length && (
              <p className="muted pad">No commands match “{search}”.</p>
            )}
            <div className="builtins">
              <span className="label">Built in</span>
              <ul>
                {builtins.map((b) => (
                  <li key={b.name} title={b.detail}>
                    <code className="bang quiet">!{b.name}</code>
                    <small>{b.detail}</small>
                  </li>
                ))}
              </ul>
            </div>
          </aside>
          <section className="studio-detail panel">
            {command ? (
              <CommandEditor
                key={index}
                index={index}
                command={command}
                workspace={workspace}
                conversations={conversations}
                busy={busy}
                savedCommand={saved?.commands.find((c) => c.name === command.name)}
                onChange={(values) => change(index, values)}
                onBack={() => setMobileDetail(false)}
                onDuplicate={() =>
                  addCommand({
                    ...structuredClone(command),
                    name: uniqueName((command.name || "command") + "_copy"),
                  })
                }
                onDelete={() => {
                  edit((c) => ({
                    ...c,
                    commands: c.commands.filter((_, n) => n !== index),
                  }));
                  setSelected(Math.max(0, index - 1));
                  setMobileDetail(false);
                }}
              />
            ) : (
              <div className="thread-placeholder">
                <Icon name="commands" size={30} />
                <strong>Create your first command</strong>
                <p>
                  Someone texts <code className="bang">!ask</code> followed by
                  a question, and the bot replies. Start with a template.
                </p>
                <button
                  type="button"
                  className="primary"
                  onClick={() => addCommand(structuredClone(templates[2].command))}
                >
                  <Icon name="plus" size={16} />
                  Add AI assistant
                </button>
              </div>
            )}
          </section>
        </div>
      )}
    </div>
  );
}

function CommandEditor({
  index,
  command,
  savedCommand,
  workspace,
  conversations,
  busy,
  onChange,
  onDuplicate,
  onDelete,
  onBack,
}: {
  index: number;
  command: Command;
  savedCommand?: Command;
  workspace: Workspace;
  conversations: Conversation[];
  busy: boolean;
  onChange: (values: Partial<Command>) => void;
  onDuplicate: () => void;
  onDelete: () => void;
  onBack: () => void;
}) {
  const { config, models } = workspace;
  const settings = config!.settings;
  const enrolled = conversations.filter((c) => c.enrolled);
  const defaultModel = models.find((m) => m.id === settings.default_model);
  const effectiveModel = models.find(
    (m) =>
      m.id ===
      (command.model ||
        config!.profiles.find((p) => p.id === command.profile_id)?.model ||
        settings.default_model),
  );
  const unsaved = JSON.stringify(savedCommand) !== JSON.stringify(command);
  return (
    <div className="editor" aria-label={`Edit command ${index + 1}`}>
      <div className="editor-head">
        <button
          type="button"
          className="icon ghost only-mobile"
          aria-label="Back to commands"
          onClick={onBack}
        >
          <Icon name="back" />
        </button>
        <label className="bang-input">
          <span aria-hidden="true">!</span>
          <input
            aria-label="Command name"
            maxLength={24}
            spellCheck={false}
            placeholder="name"
            value={command.name}
            onChange={(e) =>
              onChange({ name: e.target.value.toLowerCase().replace(/\s/g, "") })
            }
          />
        </label>
        <div className="editor-actions">
          <Switch
            label="Enabled"
            checked={command.enabled}
            onChange={(enabled) => onChange({ enabled })}
          />
          <button
            type="button"
            className="icon ghost"
            title="Duplicate"
            aria-label="Duplicate command"
            disabled={busy}
            onClick={onDuplicate}
          >
            <Icon name="copy" size={17} />
          </button>
          <button
            type="button"
            className="icon ghost danger"
            title="Delete"
            aria-label="Delete command"
            disabled={busy}
            onClick={onDelete}
          >
            <Icon name="trash" size={17} />
          </button>
        </div>
      </div>
      <Field label="Description">
        <input
          placeholder="What it does, shown in !help"
          value={command.description}
          onChange={(e) => onChange({ description: e.target.value })}
        />
      </Field>
      <div className="field">
        <span className="label">Reply with</span>
        <Segmented
          label="Command type"
          value={command.type}
          onChange={(type) => onChange({ type })}
          options={[
            { value: "fixed", label: "Fixed text" },
            { value: "ai", label: "AI" },
          ]}
        />
      </div>
      <Field
        label={command.type === "fixed" ? "Reply" : "Instructions"}
        hint={
          command.type === "fixed" ? (
            <>
              <code>{"{input}"}</code> is replaced with whatever follows the
              command.
            </>
          ) : (
            "Tell the model how to answer. The text after the command is the request."
          )
        }
      >
        <textarea
          rows={command.type === "fixed" ? 3 : 5}
          value={command.instructions}
          onChange={(e) => onChange({ instructions: e.target.value })}
        />
      </Field>
      <div className="editor-section">
        <h3>Behavior</h3>
        <Switch
          label="Ask me before replying"
          hint="The reply waits in Activity until you approve it. Expires after two minutes."
          checked={command.approval}
          onChange={(approval) => onChange({ approval })}
        />
        {command.type === "ai" && (
          <Switch
            label="Remember the conversation"
            hint={command.name === "ask" ? `Reply to an AI answer to continue its thread (up to ${settings.memory_hours}h inactive). A new !ask starts fresh.` : `Sends recent messages with this command (last ${settings.memory_hours}h).`}
            checked={command.memory}
            onChange={(memory) => onChange({ memory })}
          />
        )}
      </div>
      {command.type === "ai" && (
        <div className="editor-section">
          <h3>Model</h3>
          <div className="form-grid">
            <ModelPicker
              label="Model"
              value={command.model}
              models={models}
              favorites={settings.favorites}
              defaultLabel={`Default${defaultModel ? ` (${modelName(defaultModel)})` : ""}`}
              onChange={(model) => onChange({ model })}
            />
            <Field label="Profile">
              <select
                value={command.profile_id}
                onChange={(e) => onChange({ profile_id: e.target.value })}
              >
                <option value="">None</option>
                {config!.profiles.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name || "Untitled profile"}
                  </option>
                ))}
              </select>
            </Field>
          </div>
          <Switch
            label="Custom generation settings"
            hint="Otherwise uses the model’s settings from Settings → AI models."
            checked={!!command.generation}
            onChange={(on) =>
              onChange({ generation: on ? { ...defaultGeneration } : undefined })
            }
          />
          {command.generation && (
            <GenerationEditor
              label="Command generation"
              value={command.generation}
              model={effectiveModel}
              onChange={(generation) => onChange({ generation })}
            />
          )}
        </div>
      )}
      <div className="editor-section">
        <h3>Who can use it</h3>
        <p className="muted">
          {command.allowed_conversations.length
            ? "Only the selected conversations."
            : "Everyone with bot replies on. Pick people to limit it."}
        </p>
        <div className="chips" role="group" aria-label="Allowed conversations">
          {enrolled.map((c) => {
            const on = command.allowed_conversations.includes(c.id);
            return (
              <button
                key={c.id}
                type="button"
                className={"chip" + (on ? " on" : "")}
                aria-pressed={on}
                onClick={() =>
                  onChange({
                    allowed_conversations: on
                      ? command.allowed_conversations.filter((id) => id !== c.id)
                      : [...command.allowed_conversations, c.id],
                  })
                }
              >
                {on && <Icon name="check" size={13} />}
                <span className="chip-label">{c.label}</span>
              </button>
            );
          })}
          {!enrolled.length && (
            <small className="muted">
              No conversations have bot replies on yet.
            </small>
          )}
        </div>
      </div>
      <TryIt command={command} saved={!!savedCommand} unsaved={unsaved} />
    </div>
  );
}

function TryIt({
  command,
  saved,
  unsaved,
}: {
  command: Command;
  saved: boolean;
  unsaved: boolean;
}) {
  const [input, setInput] = useState(""),
    [result, setResult] = useState(""),
    [error, setError] = useState(""),
    [running, setRunning] = useState(false);
  return (
    <form
      className="try-it"
      onSubmit={(e) => {
        e.preventDefault();
        setRunning(true);
        setError("");
        api<{ text: string; simulated: boolean }>("/playground", "POST", {
          command: command.name,
          input,
        })
          .then((r) => {
            if (!r.simulated) throw new ApiError(502, "simulation_not_confirmed");
            setResult(r.text);
          })
          .catch((e) => setError(failure(e)))
          .finally(() => setRunning(false));
      }}
    >
      <div className="try-head">
        <h3>Try it</h3>
        <small>
          {saved
            ? unsaved
              ? "Runs the saved version. Save to try your edits."
              : "Offline preview. Nothing is sent and no AI is called."
            : "Save this command to try it."}
        </small>
      </div>
      <div className="try-row">
        <code className="bang">!{command.name || "name"}</code>
        <input
          aria-label="Preview input"
          placeholder="Text after the command"
          value={input}
          onChange={(e) => setInput(e.target.value)}
        />
        <button className="ghost" disabled={!saved || running}>
          {running ? "Running…" : "Preview"}
        </button>
      </div>
      {error && (
        <p role="alert" className="inline-alert">
          {error}
        </p>
      )}
      {result && (
        <div className="bubble outgoing from-bot preview" role="status">
          <p>{result}</p>
          <small>
            <span className="sender">Preview</span>
          </small>
        </div>
      )}
    </form>
  );
}

function Profiles({ workspace }: { workspace: Workspace }) {
  const { config, models, edit } = workspace;
  const profiles = config!.profiles;
  const update = (id: string, values: Partial<(typeof profiles)[number]>) =>
    edit((c) => ({
      ...c,
      profiles: c.profiles.map((p) => (p.id === id ? { ...p, ...values } : p)),
    }));
  return (
    <section className="panel">
      <div className="panel-head">
        <div>
          <h2>Model profiles</h2>
          <p className="muted">
            Reusable personas: a model plus standing instructions that AI
            commands can share.
          </p>
        </div>
        <button
          type="button"
          className="primary"
          onClick={() =>
            edit((c) => ({
              ...c,
              profiles: [
                ...c.profiles,
                { id: crypto.randomUUID(), name: "", model: "", instructions: "" },
              ],
            }))
          }
        >
          <Icon name="plus" size={16} />
          New profile
        </button>
      </div>
      {!profiles.length && (
        <div className="empty small">
          <strong>No profiles</strong>
          <p>Commands use the default model unless you pick one.</p>
        </div>
      )}
      <div className="profile-grid">
        {profiles.map((p, i) => (
          <article
            className="profile"
            key={p.id}
            aria-label={`Profile ${i + 1}`}
          >
            <div className="profile-head">
              <input
                className="title-input"
                aria-label={`Profile ${i + 1} name`}
                placeholder="Profile name"
                value={p.name}
                onChange={(e) => update(p.id, { name: e.target.value })}
              />
              <button
                type="button"
                className="icon ghost danger"
                aria-label={`Delete profile ${i + 1}`}
                onClick={() =>
                  edit((c) => ({
                    ...c,
                    profiles: c.profiles.filter((x) => x.id !== p.id),
                    commands: c.commands.map((command) =>
                      command.profile_id === p.id
                        ? { ...command, profile_id: "" }
                        : command,
                    ),
                  }))
                }
              >
                <Icon name="trash" size={16} />
              </button>
            </div>
            <ModelPicker
              label="Model"
              value={p.model}
              models={models}
              favorites={config!.settings.favorites}
              defaultLabel="Default model"
              onChange={(model) => update(p.id, { model })}
            />
            <Field label="Instructions">
              <textarea
                rows={4}
                value={p.instructions}
                onChange={(e) => update(p.id, { instructions: e.target.value })}
              />
            </Field>
          </article>
        ))}
      </div>
    </section>
  );
}
