import { useCallback, useEffect, useRef, useState } from "react";
import {
  api,
  ApiError,
  type CompatibleProviderStatus,
  type Configuration,
  type Generation,
  type InboxSettings,
  type Model,
} from "./api";

export const defaultGeneration: Generation = {
  reasoning: "default",
  answer_length: "short",
  tokens: 2048,
};

export function normalize(c: Configuration): Configuration {
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

export function failure(e: unknown) {
  return e instanceof ApiError
    ? e.message.replaceAll("_", " ")
    : "Could not reach the server.";
}

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);

/** Returns a reason the draft cannot be saved, or "" when it looks valid. */
export function validate(c: Configuration): string {
  const names = new Set<string>();
  for (const [i, command] of c.commands.entries()) {
    const label = command.name ? `!${command.name}` : `Command ${i + 1}`;
    if (!/^[a-z][a-z0-9_-]{0,23}$/.test(command.name))
      return `${label}: names start with a lowercase letter and use a–z, 0–9, _ or - (24 max).`;
    if (names.has(command.name)) return `${label} is defined twice.`;
    names.add(command.name);
    if (!command.instructions.trim())
      return `${label} needs ${command.type === "fixed" ? "a reply" : "instructions"}.`;
  }
  for (const [i, p] of c.profiles.entries())
    if (!p.name.trim()) return `Profile ${i + 1} needs a name.`;
  const s = c.settings;
  if (!s.timezone.trim()) return "Time zone is required.";
  for (const [key, label] of [
    ["hourly_limit", "Hourly limit"],
    ["daily_limit", "Daily limit"],
    ["memory_hours", "Memory window"],
    ["retention_days", "History retention"],
    ["max_chars", "Reply length"],
  ] as const)
    if (!(s[key] >= 1)) return `${label} must be at least 1.`;
  if (!(s.daily_budget >= 0)) return "Daily AI budget cannot be negative.";
  return "";
}

/**
 * Configuration, provider status and the opt-in inbox draft live here so that
 * drafts survive navigation; one save bar commits everything explicitly.
 */
export function useWorkspace(
  signedIn: boolean,
  inboxSaved: InboxSettings | null,
  onInboxSaved: (s: InboxSettings) => void,
) {
  const [config, setConfig] = useState<Configuration | null>(null),
    [saved, setSaved] = useState<Configuration | null>(null),
    [models, setModels] = useState<Model[]>([]),
    [openRouterConfigured, setOpenRouterConfigured] = useState(false),
    [compatible, setCompatible] = useState<CompatibleProviderStatus>({
      base_url: "https://api.openai.com/v1",
      configured: false,
    }),
    [loadError, setLoadError] = useState(""),
    [saveError, setSaveError] = useState(""),
    [saving, setSaving] = useState(false);
  const [inboxDraft, setInboxDraft] = useState<InboxSettings | null>(
    inboxSaved,
  );
  const lastInbox = useRef(inboxSaved);
  const [loads, setLoads] = useState(0);
  const savedRef = useRef(saved);
  savedRef.current = saved;
  useEffect(() => {
    if (!signedIn) {
      setConfig(null);
      setSaved(null);
      setInboxDraft(null);
      lastInbox.current = null;
      return;
    }
    let active = true;
    Promise.all([
      api<Configuration>("/config"),
      api<Model[]>("/models"),
      api<{ configured: boolean }>("/provider-key"),
      api<CompatibleProviderStatus>("/compatible-provider"),
    ])
      .then(([c, m, k, p]) => {
        if (!active) return;
        const value = normalize(c);
        // A reload that lands after the user starts typing must not erase the draft.
        const previous = savedRef.current;
        setSaved(value);
        setConfig((draft) =>
          draft && previous && !same(draft, previous) ? draft : value,
        );
        setModels(m ?? []);
        setOpenRouterConfigured(k.configured);
        setCompatible(p);
        setLoadError("");
      })
      .catch((e) => active && setLoadError(failure(e)));
    return () => {
      active = false;
    };
  }, [signedIn, loads]);
  // Polling may update the saved inbox version, but must not erase an open draft.
  useEffect(() => {
    const previous = lastInbox.current;
    if (same(previous, inboxSaved)) return;
    lastInbox.current = inboxSaved;
    setInboxDraft((draft) =>
      !draft || same(draft, previous) ? inboxSaved : draft,
    );
  }, [inboxSaved]);
  const configDirty = !!config && !!saved && !same(config, saved);
  const inboxDirty = !!inboxDraft && !!inboxSaved && !same(inboxDraft, inboxSaved);
  const edit = useCallback((fn: (c: Configuration) => Configuration) => {
    setSaveError("");
    setConfig((c) => (c ? fn(c) : c));
  }, []);
  const editInbox = useCallback((p: Partial<InboxSettings>) => {
    setSaveError("");
    setInboxDraft((s) => (s ? { ...s, ...p } : s));
  }, []);
  function discard() {
    if (saved) setConfig(structuredClone(saved));
    setInboxDraft(inboxSaved);
    setSaveError("");
  }
  /** Picks up changes made elsewhere (for example another browser session). */
  function reload() {
    setLoads((n) => n + 1);
  }
  function replaceSaved(value: Configuration) {
    setSaved(value);
    setConfig(value);
  }
  async function save(): Promise<boolean> {
    if (saving || !config) return false;
    if (configDirty) {
      const problem = validate(config);
      if (problem) {
        setSaveError(problem);
        return false;
      }
    }
    setSaving(true);
    setSaveError("");
    try {
      if (configDirty) {
        const value = normalize(
          await api<Configuration>("/config", "PUT", config),
        );
        setSaved(value);
        setConfig(value);
      }
      if (inboxDirty && inboxDraft) {
        const s = await api<InboxSettings>("/inbox/settings", "PUT", inboxDraft);
        lastInbox.current = s;
        setInboxDraft(s);
        onInboxSaved(s);
      }
      return true;
    } catch (e) {
      setSaveError(failure(e));
      return false;
    } finally {
      setSaving(false);
    }
  }
  return {
    config,
    saved,
    models,
    setModels,
    openRouterConfigured,
    setOpenRouterConfigured,
    compatible,
    setCompatible,
    loadError,
    saveError,
    saving,
    configDirty,
    inboxDraft,
    inboxDirty,
    dirty: configDirty || inboxDirty,
    edit,
    editInbox,
    discard,
    save,
    reload,
    replaceSaved,
  };
}
export type Workspace = ReturnType<typeof useWorkspace>;
