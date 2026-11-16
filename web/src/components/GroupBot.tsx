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
  onPatchConversation,
  onRefresh,
  onError,
}: {
  conversation: Conversation;
  onPatchConversation: (id: string, patch: Partial<Conversation>) => void;
  onRefresh: () => void;
  onError: (e: unknown) => void;
}) {
  const [open, setOpen] = useState(false);
  const [approving, setApproving] = useState(false);
  const [savingShared, setSavingShared] = useState(false);
  // Switch positions chosen but not yet confirmed by the server.
  const [wanted, setWanted] = useState<Record<string, boolean>>({});
  const queue = useRef<Promise<void>>(Promise.resolve());
  const pending = useRef(0);
  const failure = useRef<unknown>(null);
  const confirmed = useRef<{ enrolled_members: string[]; bot_roster: string } | null>(null);
  const latestConv = useRef(c);
  latestConv.current = c;
  const members = c.members ?? [];
  const enrolled = c.enrolled_members ?? [];
  const needsApproval = enrolled.length > 0 && (c.bot_roster ?? "") !== (c.roster ?? "");
  const roster = c.roster ?? "";
  const name = (id: string) => members.find((m) => m.id === id)?.name || "Someone";
  const isOn = (id: string) => wanted[id] ?? enrolled.includes(id);
  const allOn = members.length > 0 && members.every((m) => isOn(m.id));

  type GroupStateReply = {
    enrolled?: boolean;
    enrolled_members?: string[];
    bot_roster?: string;
  };

  const runSave = (
    changes: [string, boolean][],
    task: (conv: Conversation) => Promise<GroupStateReply>,
  ) => {
    if (!changes.length) return;
    setWanted((w) => ({ ...w, ...Object.fromEntries(changes) }));
    pending.current++;
    queue.current = queue.current.then(async () => {
      try {
        const res = await task(latestConv.current);
        if (res.enrolled_members) {
          confirmed.current = {
            enrolled_members: res.enrolled_members,
            bot_roster: res.bot_roster ?? "",
          };
        }
      } catch (e) {
        failure.current ??= e;
      }
      if (--pending.current > 0) return;
      const err = failure.current;
      const saved = confirmed.current;
      failure.current = null;
      confirmed.current = null;
      if (saved) {
        onPatchConversation(latestConv.current.id, {
          enrolled: saved.enrolled_members.length > 0,
          enrolled_members: saved.enrolled_members,
          bot_roster: saved.bot_roster,
        });
      }
      setWanted({});
      if (err) {
        onError(err);
        onRefresh();
      } else if (!saved) {
        onRefresh();
      }
    });
  };

  const setOne = (id: string, on: boolean) => {
    if (isOn(id) === on) return;
    runSave([[id, on]], (conv) =>
      on
        ? api<GroupStateReply>("/enrollment", "POST", {
            conversation_id: conv.id,
            participant_id: id,
            roster: conv.roster ?? "",
          })
        : api<GroupStateReply>(
            `/enrollment/${encodeURIComponent(conv.id)}/members/${encodeURIComponent(id)}`,
            "DELETE",
          ),
    );
  };

  const setEveryone = (on: boolean) => {
    const knownIds = [...new Set([...members.map((m) => m.id), ...(on ? [] : enrolled)])];
    const changes: [string, boolean][] = knownIds
      .filter((id) => isOn(id) !== on)
      .map((id) => [id, on]);
    if (!changes.length) return;
    const targetIds = on ? members.map((m) => m.id) : [];
    runSave(changes, (conv) =>
      api<GroupStateReply>(`/enrollment/${encodeURIComponent(conv.id)}/members`, "PUT", {
        members: targetIds,
        ...(on ? { roster: conv.roster ?? "" } : {}),
      }),
    );
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
                disabled={approving}
                onClick={async () => {
                  setApproving(true);
                  try {
                    const res = await api<GroupStateReply>(
                      `/enrollment/${encodeURIComponent(c.id)}/approve`,
                      "POST",
                      { roster },
                    );
                    if (res.enrolled_members) {
                      onPatchConversation(c.id, {
                        enrolled: !!res.enrolled,
                        enrolled_members: res.enrolled_members,
                        bot_roster: res.bot_roster ?? roster,
                      });
                    } else onRefresh();
                  } catch (e) {
                    onError(e);
                    onRefresh();
                  } finally {
                    setApproving(false);
                  }
                }}
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
              onChange={setEveryone}
            />
          )}
          {members.map((m) => (
            <Switch
              key={m.id}
              label={`Bot replies for ${m.name || "Someone"}`}
              checked={isOn(m.id)}
              disabled={needsApproval && !enrolled.includes(m.id)}
              onChange={(on) => setOne(m.id, on)}
            />
          ))}
          <Switch
            label="One AI conversation for everyone"
            checked={c.shared_ai ?? false}
            disabled={savingShared}
            onChange={async (on) => {
              setSavingShared(true);
              try {
                await api(`/enrollment/${encodeURIComponent(c.id)}/shared`, "PUT", { shared: on });
                onPatchConversation(c.id, { shared_ai: on });
              } catch (e) {
                onError(e);
                onRefresh();
              } finally {
                setSavingShared(false);
              }
            }}
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
