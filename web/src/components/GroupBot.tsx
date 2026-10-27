import { useState } from "react";
import { api, type Conversation } from "../lib/api";
import { Switch } from "./ui";

/**
 * Who may use the bot in a group. The owner picks people, and the picks are
 * approved for the group as it is now: if anyone joins or leaves, the bot goes
 * quiet there until the owner has looked at who is in it and approves again.
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
  const members = c.members ?? [];
  const enrolled = c.enrolled_members ?? [];
  const needsApproval = enrolled.length > 0 && (c.bot_roster ?? "") !== (c.roster ?? "");
  const roster = c.roster ?? "";
  const name = (id: string) => members.find((m) => m.id === id)?.name || "Someone";
  const toggle = (id: string, on: boolean) =>
    act(() =>
      on
        ? api("/enrollment", "POST", { conversation_id: c.id, participant_id: id, roster })
        : api(`/enrollment/${encodeURIComponent(c.id)}/members/${encodeURIComponent(id)}`, "DELETE"),
    );
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
          {members.map((m) => (
            <Switch
              key={m.id}
              label={`Bot replies for ${m.name || "Someone"}`}
              checked={enrolled.includes(m.id)}
              disabled={busy || (needsApproval && !enrolled.includes(m.id))}
              onChange={(on) => void toggle(m.id, on)}
            />
          ))}
          <small>Only the people you switch on can run commands here. Answers go to the whole group.</small>
        </div>
      )}
    </div>
  );
}
