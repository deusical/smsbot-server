import { useEffect, useState, type ReactNode } from "react";
import { NotificationSettings } from "../components/NotificationSettings";
import {
  api,
  defaultAttachmentLimits,
  ApiError,
  type CompatibleProviderStatus,
  type Configuration,
  type Conversation,
  type Model,
  type Settings as BotSettings,
  type State,
} from "../lib/api";
import { Confirmation } from "../components/Confirmation";
import { GenerationEditor } from "../components/Generation";
import { Icon } from "../components/Icon";
import { Field, Group, ModelPicker, Segmented, Switch } from "../components/ui";
import { defaultGeneration, normalize, type Workspace } from "../lib/workspace";

const sections = [
  { id: "models", label: "AI models", icon: "cpu" },
  { id: "providers", label: "Providers", icon: "key" },
  { id: "limits", label: "Limits & quiet hours", icon: "gauge" },
  { id: "inbox", label: "Messages & privacy", icon: "inbox" },
  { id: "notifications", label: "Notifications", icon: "bell" },
  { id: "devices", label: "Devices", icon: "phone" },
  { id: "backup", label: "Import & export", icon: "download" },
] as const;
type Section = (typeof sections)[number]["id"];

export function SettingsPage({
  workspace,
  conversations,
  state,
  busy,
  act,
  notify,
  onSignOut,
}: {
  workspace: Workspace;
  conversations: Conversation[];
  state: State | null;
  busy: boolean;
  act: (fn: () => Promise<unknown>, refreshAfter?: boolean) => Promise<void>;
  notify: (text: string) => void;
  onSignOut: () => void;
}) {
  const [section, setSection] = useState<Section>("models");
  const { config } = workspace;
  if (!config)
    return (
      <div className="panel loading-panel">
        {workspace.loadError ? (
          <p role="alert" className="inline-alert">
            {workspace.loadError}
          </p>
        ) : (
          <p className="muted">Loading settings…</p>
        )}
      </div>
    );
  const shared = { workspace, busy, act, notify };
  return (
    <div className="settings">
      <nav className="settings-nav" aria-label="Settings sections">
        {sections.map((s) => (
          <button
            key={s.id}
            type="button"
            aria-current={section === s.id ? "page" : undefined}
            onClick={() => setSection(s.id)}
          >
            <Icon name={s.icon} size={17} />
            <span>{s.label}</span>
          </button>
        ))}
        <button
          type="button"
          className="signout"
          disabled={busy}
          onClick={onSignOut}
        >
          <Icon name="logout" size={17} />
          <span>Sign out</span>
        </button>
      </nav>
      <div className="settings-body">
        {section === "models" && <ModelsSection {...shared} />}
        {section === "providers" && <ProvidersSection {...shared} />}
        {section === "limits" && <LimitsSection {...shared} />}
        {section === "inbox" && (
          <InboxSection {...shared} conversations={conversations} />
        )}
        {section === "notifications" && (
          <NotificationSettings conversations={conversations} notify={notify} />
        )}
        {section === "devices" && <DevicesSection {...shared} state={state} />}
        {section === "backup" && <BackupSection {...shared} />}
      </div>
    </div>
  );
}

type SectionProps = {
  workspace: Workspace;
  busy: boolean;
  act: (fn: () => Promise<unknown>, refreshAfter?: boolean) => Promise<void>;
  notify: (text: string) => void;
};

function setSetting<K extends keyof BotSettings>(
  workspace: Workspace,
  key: K,
  value: BotSettings[K],
) {
  workspace.edit((c) => ({ ...c, settings: { ...c.settings, [key]: value } }));
}

function ModelsSection({ workspace, busy, act, notify }: SectionProps) {
  const { config, models, setModels, compatible } = workspace;
  const settings = config!.settings;
  const [tuning, setTuning] = useState(settings.default_model);
  useEffect(() => {
    if (!tuning && settings.default_model) setTuning(settings.default_model);
  }, [settings.default_model]);
  const toggleFavorite = (id: string) =>
    setSetting(
      workspace,
      "favorites",
      settings.favorites.includes(id)
        ? settings.favorites.filter((f) => f !== id)
        : [...settings.favorites, id],
    );
  const defaultModel = models.find((m) => m.id === settings.default_model);
  const tuned = models.find((m) => m.id === tuning);
  return (
    <>
      <Group
        title="Default model"
        description="Used by AI commands that don’t pick their own model. Star models to keep them at the top of every list."
        aside={
          <div className="button-row">
            <button
              type="button"
              className="ghost small"
              disabled={busy}
              onClick={() =>
                void act(async () => {
                  setModels(await api<Model[]>("/models/refresh", "POST"));
                  notify("OpenRouter models refreshed.");
                }, false)
              }
            >
              <Icon name="refresh" size={14} />
              Refresh OpenRouter
            </button>
            {compatible.configured && (
              <button
                type="button"
                className="ghost small"
                disabled={busy}
                onClick={() =>
                  void act(async () => {
                    setModels(
                      await api<Model[]>("/models/refresh-compatible", "POST"),
                    );
                    notify("Compatible models refreshed.");
                  }, false)
                }
              >
                <Icon name="refresh" size={14} />
                Refresh compatible
              </button>
            )}
          </div>
        }
      >
        <ModelPicker
          label="Default model"
          value={settings.default_model}
          models={models}
          favorites={settings.favorites}
          onToggleFavorite={toggleFavorite}
          onChange={(id) => {
            setSetting(workspace, "default_model", id);
            setTuning(id);
          }}
          hint={`${models.length} model${models.length === 1 ? "" : "s"} available${settings.favorites.length ? `, ${settings.favorites.length} starred` : ""}.`}
        />
        {settings.daily_budget > 0 &&
          defaultModel?.provider === "compatible" &&
          defaultModel.pricing_known === false && (
            <p className="inline-note warn">
              This provider doesn’t report prices, so the daily AI budget will
              block it. Set the budget to 0 in Limits to rely on the provider’s
              own spending controls.
            </p>
          )}
      </Group>
      <Group
        title="Model tuning"
        description="Reasoning and length for each model. Commands can override these."
      >
        <ModelPicker
          label="Tune model"
          value={tuning}
          models={models}
          favorites={settings.favorites}
          onToggleFavorite={toggleFavorite}
          onChange={setTuning}
        />
        {tuning && (
          <GenerationEditor
            label="Model generation"
            value={settings.model_generation[tuning] ?? defaultGeneration}
            model={tuned}
            onChange={(generation) =>
              setSetting(workspace, "model_generation", {
                ...settings.model_generation,
                [tuning]: generation,
              })
            }
          />
        )}
      </Group>
    </>
  );
}

function StatusPill({ on, children }: { on: boolean; children: ReactNode }) {
  return <span className={"pill " + (on ? "ok" : "off")}>{children}</span>;
}

function ProvidersSection({ workspace, busy, act, notify }: SectionProps) {
  const {
    openRouterConfigured,
    setOpenRouterConfigured,
    compatible,
    setCompatible,
  } = workspace;
  const [key, setKey] = useState(""),
    [url, setUrl] = useState(compatible.base_url),
    [compatibleKey, setCompatibleKey] = useState("");
  return (
    <>
      <Group
        title="OpenRouter"
        description="Access to hundreds of models with one key. AI requests and opted-in memory go to OpenRouter when you use its models."
        aside={
          <StatusPill on={openRouterConfigured}>
            {openRouterConfigured ? "Key saved" : "No key"}
          </StatusPill>
        }
      >
        <form
          className="inline-form"
          onSubmit={(e) => {
            e.preventDefault();
            void act(async () => {
              await api("/provider-key", "PUT", { key });
              setKey("");
              setOpenRouterConfigured(!!key.trim());
              notify("OpenRouter key saved.");
            }, false);
          }}
        >
          <Field label="OpenRouter API key">
            <input
              type="password"
              autoComplete="off"
              placeholder={
                openRouterConfigured
                  ? "Paste a new key to replace it"
                  : "sk-or-…"
              }
              value={key}
              onChange={(e) => setKey(e.target.value)}
            />
          </Field>
          <button className="primary" disabled={busy || !key}>
            Save key
          </button>
        </form>
      </Group>
      <Group
        title="OpenAI-compatible API"
        description="OpenAI directly, or any gateway with /models and /chat/completions. Plain HTTP is allowed only for localhost."
        aside={
          <StatusPill on={compatible.configured}>
            {compatible.configured ? "Key saved" : "No key"}
          </StatusPill>
        }
      >
        <form
          onSubmit={(e) => {
            e.preventDefault();
            void act(async () => {
              const status = await api<CompatibleProviderStatus>(
                "/compatible-provider",
                "PUT",
                { base_url: url, key: compatibleKey },
              );
              setCompatible(status);
              setUrl(status.base_url);
              setCompatibleKey("");
              notify("Compatible API saved.");
            }, false);
          }}
        >
          <div className="form-grid">
            <Field label="Base URL">
              <input
                type="url"
                required
                autoComplete="url"
                placeholder="http://127.0.0.1:8317/v1"
                value={url}
                onChange={(e) => setUrl(e.target.value)}
              />
            </Field>
            <Field label="API key">
              <input
                type="password"
                autoComplete="off"
                placeholder={
                  compatible.configured
                    ? "Leave blank to keep the saved key"
                    : "Enter an API key"
                }
                value={compatibleKey}
                onChange={(e) => setCompatibleKey(e.target.value)}
              />
            </Field>
          </div>
          <div className="button-row">
            <button className="primary" disabled={busy || !url.trim()}>
              Save
            </button>
            {compatible.configured && (
              <button
                type="button"
                className="ghost danger"
                disabled={busy}
                onClick={() =>
                  void act(async () => {
                    const status = await api<CompatibleProviderStatus>(
                      "/compatible-provider",
                      "PUT",
                      { base_url: url, key: "", clear_key: true },
                    );
                    setCompatible(status);
                    setCompatibleKey("");
                    notify("Compatible API key removed.");
                  }, false)
                }
              >
                Remove key
              </button>
            )}
          </div>
        </form>
      </Group>
      <p className="footnote">
        <Icon name="shield" size={14} />
        Keys are encrypted on this server and never sent back to the browser.
      </p>
    </>
  );
}

const hours = Array.from({ length: 24 }, (_, h) => (
  <option key={h} value={h}>
    {String(h).padStart(2, "0")}:00
  </option>
));

function NumberField({
  workspace,
  field,
  label,
  hint,
  min = 1,
  step = 1,
  unit,
}: {
  workspace: Workspace;
  field:
    | "hourly_limit"
    | "daily_limit"
    | "daily_budget"
    | "memory_hours"
    | "retention_days"
    | "max_chars";
  label: string;
  hint?: string;
  min?: number;
  step?: number;
  unit?: string;
}) {
  return (
    <Field label={label} hint={hint} unit={unit}>
      <input
        type="number"
        min={min}
        step={step}
        value={workspace.config!.settings[field]}
        onChange={(e) => setSetting(workspace, field, Number(e.target.value))}
      />
    </Field>
  );
}

function LimitsSection({ workspace }: SectionProps) {
  const settings = workspace.config!.settings;
  return (
    <>
      <Group
        title="Limits"
        description="Counted across every conversation, including messages you send by hand."
      >
        <div className="form-grid three">
          <NumberField
            workspace={workspace}
            field="hourly_limit"
            label="Replies per hour"
          />
          <NumberField
            workspace={workspace}
            field="daily_limit"
            label="Replies per day"
          />
          <NumberField
            workspace={workspace}
            field="max_chars"
            label="Longest reply"
            unit="chars"
          />
          <NumberField
            workspace={workspace}
            field="daily_budget"
            label="Daily AI budget"
            unit="USD"
            min={0}
            step={0.01}
            hint="Uses provider-reported costs. 0 turns it off."
          />
        </div>
      </Group>
      <Group
        title="Quiet hours"
        description="Commands that arrive during quiet hours are dropped, not saved for later."
        aside={
          <Switch
            label="Quiet hours"
            hideLabel
            checked={settings.quiet_enabled}
            onChange={(on) => setSetting(workspace, "quiet_enabled", on)}
          />
        }
      >
        <div
          className={"form-grid three" + (settings.quiet_enabled ? "" : " dim")}
        >
          <Field label="From">
            <select
              value={settings.quiet_start}
              onChange={(e) =>
                setSetting(workspace, "quiet_start", Number(e.target.value))
              }
            >
              {hours}
            </select>
          </Field>
          <Field label="Until">
            <select
              value={settings.quiet_end}
              onChange={(e) =>
                setSetting(workspace, "quiet_end", Number(e.target.value))
              }
            >
              {hours}
            </select>
          </Field>
          <Field label="Time zone">
            <input
              placeholder="America/Los_Angeles"
              list="timezones"
              value={settings.timezone}
              onChange={(e) =>
                setSetting(workspace, "timezone", e.target.value)
              }
            />
          </Field>
          <datalist id="timezones">
            {(Intl.supportedValuesOf?.("timeZone") ?? []).map((z) => (
              <option key={z} value={z} />
            ))}
          </datalist>
        </div>
      </Group>
      <Group
        title="Commands from your phone"
        description="Run commands you send yourself, from your phone or from Conversations, in a conversation with bot replies on. The reply goes to that conversation, so the other person sees both. Limits and quiet hours still apply."
        aside={
          <Switch
            label="Commands from your phone"
            hideLabel
            checked={!!settings.owner_commands}
            onChange={(on) => setSetting(workspace, "owner_commands", on)}
          />
        }
      />
    </>
  );
}

function InboxSection({
  workspace,
  conversations,
}: SectionProps & { conversations: Conversation[] }) {
  const draft = workspace.inboxDraft;
  const eligible = conversations.filter((c) => !c.group && c.writable);
  return (
    <>
      {draft && (
        <Group
          title="Full inbox"
          description="Keep messages and files from your phone, not just commands, so you can read and reply here. Groups are never included."
        >
          <Switch
            label="Show all conversation messages"
            hint="Stored encrypted on this server. Doesn’t give anyone bot access or turn on AI memory."
            checked={draft.enabled}
            onChange={(enabled) => workspace.editInbox({ enabled })}
          />
          <div className={"nested" + (draft.enabled ? "" : " dim")}>
            <div className="field">
              <span className="label">Which conversations</span>
              <Segmented
                label="Capture scope"
                value={draft.scope}
                onChange={(scope) => workspace.editInbox({ scope })}
                options={[
                  { value: "all", label: "All individual" },
                  { value: "selected", label: "Only selected" },
                ]}
              />
            </div>
            {draft.scope === "selected" && (
              <div
                className="chips"
                role="group"
                aria-label="Captured conversations"
              >
                {eligible.map((c) => {
                  const on = draft.conversations.some(
                    (b) =>
                      b.conversation === c.id &&
                      b.participant === c.participant_id,
                  );
                  const others = draft.conversations.filter(
                    (b) => b.conversation !== c.id,
                  );
                  return (
                    <button
                      key={c.id}
                      type="button"
                      className={"chip" + (on ? " on" : "")}
                      aria-pressed={on}
                      onClick={() =>
                        workspace.editInbox({
                          conversations: on
                            ? others
                            : [
                                ...others,
                                {
                                  conversation: c.id,
                                  participant: c.participant_id,
                                },
                              ],
                        })
                      }
                    >
                      {on && <Icon name="check" size={13} />}
                      {c.label}
                    </button>
                  );
                })}
              </div>
            )}
            <Switch
              label="Load older phone history when scrolling up"
              hint="Fetches older texts in small pages. Commands in old messages never run."
              checked={draft.load_older}
              onChange={(load_older) => workspace.editInbox({ load_older })}
            />
          </div>
          <Switch
            label="Mark messages read when I view them"
            hint="While this tab is open and focused, a conversation you are looking at is marked read on your phone. The other person may see a read receipt if your phone sends them."
            checked={draft.mark_read_when_viewed !== false}
            onChange={(mark_read_when_viewed) =>
              workspace.editInbox({ mark_read_when_viewed })
            }
          />
          <Switch
            label="Automatic command replies"
            hint="Turn off to only reply by hand. Pausing the bot still stops all sending."
            checked={draft.automatic_replies}
            onChange={(automatic_replies) =>
              workspace.editInbox({ automatic_replies })
            }
          />
        </Group>
      )}
      {draft && (
        <Group
          title="Attachment limits"
          description="Set any limit to 0 for Unlimited. Google and carrier restrictions still apply; large files use server memory and storage."
        >
          <div className="form-grid">
            {(
              [
                ["max_files", "Files per message"],
                ["max_file_mb", "Per-file limit (MB)"],
                ["max_total_mb", "Combined file limit (MB)"],
              ] as const
            ).map(([key, label]) => {
              const limits = draft.attachment_limits ?? defaultAttachmentLimits;
              return (
                <div className="field" key={key}>
                  <label>
                    <span className="label">{label}</span>
                    <input
                      type="number"
                      min={0}
                      step={1}
                      value={limits[key]}
                      onChange={(event) =>
                        workspace.editInbox({
                          attachment_limits: {
                            ...limits,
                            [key]: Number(event.target.value),
                          },
                        })
                      }
                    />
                  </label>
                  <div className="field-foot">
                    <small>
                      {limits[key] === 0 ? "Unlimited" : "0 means Unlimited"}
                    </small>
                    <button
                      type="button"
                      className="link"
                      aria-label={`Unlimited ${label}`}
                      onClick={() =>
                        workspace.editInbox({
                          attachment_limits: { ...limits, [key]: 0 },
                        })
                      }
                    >
                      Unlimited
                    </button>
                  </div>
                </div>
              );
            })}
          </div>
        </Group>
      )}
      <Group title="Retention & memory">
        <div className="form-grid">
          <NumberField
            workspace={workspace}
            field="retention_days"
            label="Keep messages for"
            unit="days"
            hint="Older messages are deleted from this server."
          />
          <NumberField
            workspace={workspace}
            field="memory_hours"
            label="AI memory window"
            unit="hours"
            hint="How far back commands with memory can look."
          />
        </div>
      </Group>
    </>
  );
}

function DevicesSection({
  busy,
  act,
  notify,
  state,
}: SectionProps & { state: State | null }) {
  return (
    <>
      <Group
        title="Phone connection"
        aside={
          <StatusPill on={!!state?.transport_ready}>
            {state?.transport_ready ? "Connected" : "Not connected"}
          </StatusPill>
        }
      >
        <dl className="facts">
          <dt>Transport</dt>
          <dd>{state?.fixture ? "Offline demo" : "Google Messages"}</dd>
          <dt>API version</dt>
          <dd>v{state?.api_version}</dd>
        </dl>
      </Group>
      <Group
        title="Browser sessions"
        description="Signed-in browsers and installed phone apps stay signed in until they sign out. If a phone is lost, sign out everywhere, including here."
      >
        <button
          type="button"
          className="danger"
          disabled={busy}
          onClick={() =>
            void act(async () => {
              await api("/sessions/revoke", "POST");
              window.location.reload();
            }, false)
          }
        >
          <Icon name="logout" size={16} />
          Sign out everywhere
        </button>
      </Group>
    </>
  );
}

function BackupSection({ workspace, busy, act, notify }: SectionProps) {
  const [pending, setPending] = useState<File | null>(null);
  async function importFile(file: File) {
    await act(async () => {
      if (file.size > 1024 * 1024) throw new ApiError(400, "recipe_too_large");
      const configuration = JSON.parse(await file.text());
      const imported = normalize(
        await api<Configuration>("/recipes/import", "POST", { configuration }),
      );
      workspace.replaceSaved(imported);
      notify(
        "Recipe imported. Commands are disabled; review them before enabling.",
      );
    }, false);
  }
  return (
    <Group
      title="Recipes"
      description="Share your commands, profiles and settings as a file. Recipes never include keys, devices or who can use each command."
    >
      <div className="recipe-actions">
        <button
          type="button"
          className="tile"
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
          <Icon name="download" />
          <strong>Export recipe</strong>
          <small>Download your saved setup</small>
        </button>
        <label className={"tile" + (busy ? " disabled" : "")}>
          <Icon name="refresh" />
          <strong>Import recipe</strong>
          <small>
            Replaces commands and settings with a recipe file you exported earlier.
          </small>
          <input
            type="file"
            className="sr-only"
            accept="application/json,.json"
            disabled={busy}
            onChange={(e) => {
              const file = e.target.files?.[0];
              e.target.value = "";
              if (!file) return;
              if (workspace.dirty) setPending(file);
              else void importFile(file);
            }}
          />
        </label>
      </div>
      {pending && (
        <Confirmation
          title="Replace unsaved changes?"
          description="Importing replaces your saved setup and throws away your unsaved edits."
          confirmLabel="Import anyway"
          cancelLabel="Cancel"
          onCancel={() => setPending(null)}
          onConfirm={() => {
            const file = pending;
            setPending(null);
            void importFile(file);
          }}
        />
      )}
    </Group>
  );
}
