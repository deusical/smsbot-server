import { useRef, useState } from "react";
import { api, type Conversation } from "../lib/api";
import { Switch } from "./ui";

/**
 * Who may use the bot in a group. The owner picks people, and the picks are
 * approved for the group as it is now: if anyone joins or leaves, the bot goes
 * quiet there until the owner has looked at who is in it and approves again.
 *
 * Member switches flip at once and their saves run one after another in the
 * background, so several people (or everyone) can be switched without waiting
 * for each save; the dashboard refreshes once when the last save finishes.
 */
export function GroupBot({
  conversation: c,
  busy,
  act,
}: {
  conversation: Conversation;
  busy: boolean;
  act: (fn: () => Promise<unknown>, refreshAfter?: boolean) => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  // Switch positions chosen but not yet confirmed by a refresh.
  const [wanted, setWanted] = useState<Record<string, boolean>>({});
  const queue = useRef<Promise<void>>(Promise.resolve());
  const pending = useRef(0);
  const failure = useRef<unknown>(null);
  const members = c.members ?? [];
  const enrolled = c.enrolled_members ?? [];
  const needsApproval = enrolled.length > 0 && (c.bot_roster ?? "") !== (c.roster ?? "");
  const roster = c.roster ?? "";
  const name = (id: string) => members.find((m) => m.id === id)?.name || "Someone";
  const isOn = (id: string) => wanted[id] ?? enrolled.includes(id);
  const allOn = members.length > 0 && members.every((m) => isOn(m.id));

  const save = (id: string, on: boolean) =>
    on
      ? api("/enrollment", "POST", { conversation_id: c.id, participant_id: id, roster })
      : api(`/enrollment/${encodeURIComponent(c.id)}/members/${encodeURIComponent(id)}`, "DELETE");

  const setMany = (changes: [string, boolean][]) => {
    if (!changes.length) return;
    setWanted((w) => ({ ...w, ...Object.fromEntries(changes) }));
    for (const [id, on] of changes) {
      pending.current++;
      queue.current = queue.current.then(async () => {
        try {
          await save(id, on);
        } catch (e) {
          failure.current ??= e;
        }
        if (--pending.current > 0) return;
        const err = failure.current;
        failure.current = null;
        await act(async () => {
          if (err) throw err;
        });
        // A switch flipped during the refresh keeps its own position.
        if (pending.current === 0) setWanted({});
      });
    }
  };

  return (
    <div className="group-bot">
      <button
        type="button"
        className="chip"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
      >
        {needsApproval
          ? "Bot paused: people changed"
          : enrolled.length
            ? `Bot on for ${enrolled.map(name).join(", ")}`
            : "Bot off"}
      </button>
      {open && (
        <div className="group-bot-panel" role="group" aria-label="Who can use the bot in this group">
          {needsApproval && (
            <p role="alert">
              The people in this group changed, so the bot is silent here. Check who is in it,
              then approve.{" "}
              <button
                type="button"
                disabled={busy}
                onClick={() => void act(() => api(`/enrollment/${encodeURIComponent(c.id)}/approve`, "POST", { roster }))}
              >
                Approve these people
              </button>
            </p>
          )}
          {members.length > 1 && (
            <Switch
              label="Bot replies for everyone"
              checked={allOn}
              disabled={needsApproval && !allOn}
              onChange={(on) => setMany(members.filter((m) => isOn(m.id) !== on).map((m) => [m.id, on]))}
            />
          )}
          {members.map((m) => (
            <Switch
              key={m.id}
              label={`Bot replies for ${m.name || "Someone"}`}
              checked={isOn(m.id)}
              disabled={needsApproval && !enrolled.includes(m.id)}
              onChange={(on) => setMany([[m.id, on]])}
            />
          ))}
          <Switch
            label="One AI conversation for everyone"
            checked={c.shared_ai ?? false}
            disabled={busy}
            onChange={(on) => void act(() => api(`/enrollment/${encodeURIComponent(c.id)}/shared`, "PUT", { shared: on }))}
          />
          <small>
            {c.shared_ai
              ? "Everyone here, you included, continues the same AI conversation, and each question is marked with who asked it. Only you can clear it with !new."
              : "Each person has their own AI conversation."}{" "}
            Changing this starts the group's AI conversations over.
          </small>
          <small>Only the people you switch on can run commands here. Answers go to the whole group.</small>
        </div>
      )}
    </div>
  );
}
