import { useEffect, useRef, useState } from "react";
import { api, type InboxSettings as Settings, type Conversation } from "./api";

export function InboxSettings({
  saved,
  conversations,
  onSaved,
  onDirtyChange,
}: {
  saved: Settings;
  conversations: Conversation[];
  onSaved: (s: Settings) => void;
  onDirtyChange: (dirty: boolean) => void;
}) {
  const [draft, setDraft] = useState(saved),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const saving = useRef(false);
  const lastSaved = useRef(saved);
  useEffect(() => {
    if (JSON.stringify(lastSaved.current) === JSON.stringify(saved)) return;
    if (JSON.stringify(draft) === JSON.stringify(lastSaved.current))
      setDraft(saved);
    lastSaved.current = saved;
  }, [saved, draft]);
  const dirty = JSON.stringify(draft) !== JSON.stringify(saved);
  useEffect(() => {
    onDirtyChange(dirty);
    return () => onDirtyChange(false);
  }, [dirty, onDirtyChange]);
  // Polling may update the saved version, but it must not erase an open draft.
  function edit(p: Partial<Settings>) {
    setDraft((s) => ({ ...s, ...p }));
    setError("");
  }
  async function save() {
    if (saving.current) return;
    saving.current = true;
    setBusy(true);
    try {
      const s = await api<Settings>("/inbox/settings", "PUT", draft);
      setDraft(s);
      onSaved(s);
      setError("");
    } catch (e) {
      setError(
        e instanceof Error
          ? e.message.replaceAll("_", " ")
          : "Could not save conversation settings",
      );
    } finally {
      saving.current = false;
      setBusy(false);
    }
  }
  return (
    <section className="card">
      <div className="section-heading">
        <h2>Conversations &amp; history</h2>
      </div>
      <p className="muted">
        Owner-only message access. Messages are encrypted on this server and use
        the existing retention period. These settings are excluded from recipes
        and never enable AI memory.
      </p>
      <label className="check">
        <input
          type="checkbox"
          checked={draft.enabled}
          onChange={(e) => edit({ enabled: e.target.checked })}
        />
        Show all conversation messages
      </label>
      <label className="field">
        Capture conversations
        <select
          value={draft.scope}
          onChange={(e) => edit({ scope: e.target.value as Settings["scope"] })}
        >
          <option value="all">All individual conversations</option>
          <option value="selected">Selected conversations</option>
        </select>
      </label>
      {draft.scope === "selected" && (
        <fieldset>
          <legend>Conversations to capture</legend>
          {conversations
            .filter((c) => !c.group && c.writable)
            .map((c) => (
              <label className="check" key={c.id}>
                <input
                  type="checkbox"
                  checked={draft.conversations.some(
                    (b) =>
                      b.conversation === c.id &&
                      b.participant === c.participant_id,
                  )}
                  onChange={(e) =>
                    edit({
                      conversations: e.target.checked
                        ? [
                            ...draft.conversations.filter(
                              (b) => b.conversation !== c.id,
                            ),
                            {
                              conversation: c.id,
                              participant: c.participant_id,
                            },
                          ]
                        : draft.conversations.filter(
                            (b) => b.conversation !== c.id,
                          ),
                    })
                  }
                />
                {c.label}
              </label>
            ))}
        </fieldset>
      )}
      <label className="check">
        <input
          type="checkbox"
          checked={draft.load_older}
          onChange={(e) => edit({ load_older: e.target.checked })}
        />
        Load older phone history when scrolling up
      </label>
      <p className="muted">
        Fetch available text in small pages. Attachments and groups are
        excluded. Retention applies; available history may be incomplete.
        Historical commands never run.
      </p>
      <label className="check">
        <input
          type="checkbox"
          checked={draft.automatic_replies}
          onChange={(e) => edit({ automatic_replies: e.target.checked })}
        />
        Automatic command replies
      </label>
      <p className="muted">
        Turning automatic replies off still allows manual replies in accessible
        conversations. The global Pause control stops all sending. Turning
        capture off stops collection; it does not delete retained messages.
      </p>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      <div className="actions">
        <button disabled={busy || !dirty} onClick={() => void save()}>
          {busy ? "Saving…" : "Save conversation settings"}
        </button>
        <button
          className="secondary"
          disabled={busy}
          onClick={() => {
            setDraft(saved);
            setError("");
          }}
        >
          Reload saved conversation settings
        </button>
      </div>
    </section>
  );
}
