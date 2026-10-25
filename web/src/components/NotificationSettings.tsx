import { useEffect, useRef, useState } from "react";
import { api, ApiError, type Conversation } from "../lib/api";
import {
  deviceName,
  disableThisDevice,
  enableThisDevice,
  pushUnsupported,
  thisDevice,
  type NotificationDevice,
  type NotificationPrefs,
  type NotificationState,
} from "../lib/notifications";
import { Icon } from "./Icon";
import { Field, Group, Notice, Segmented, StatusPill, Switch } from "./ui";

const messageModes: { value: NotificationPrefs["messages"]; label: string }[] = [
  { value: "all", label: "All" },
  { value: "bot_on", label: "Bot on" },
  { value: "chosen", label: "Chosen" },
  { value: "off", label: "Off" },
];

function problem(e: unknown): string {
  const code = e instanceof Error ? e.message : "";
  if (code === "notifications_blocked") return "Notifications are blocked for this site. Allow them in the browser's site settings, then try again.";
  if (code === "notifications_dismissed") return "Notifications were not allowed. Try again and choose Allow.";
  if (code === "device_unsubscribed") return "The browser no longer accepts notifications for this device. Turn them on again.";
  if (e instanceof ApiError && e.status === 502) return "The push service did not accept the notification. Try again shortly.";
  return "Notifications could not be updated. Try again.";
}

function summary(p: NotificationPrefs): string {
  const messages = { all: "All messages", bot_on: "Bot-on messages", chosen: `${p.conversations.length} chosen conversations`, off: "No messages" }[p.messages];
  const alerts = [p.approvals && "approvals", p.problems && "problems", p.connection && "connection"].filter(Boolean);
  return `${messages} · ${alerts.length ? "alerts for " + alerts.join(", ") : "no alerts"}`;
}

/**
 * Notifications are set up per device: each browser that turns them on gets
 * its own settings, editable from any signed-in device.
 */
export function NotificationSettings({ conversations, notify }: { conversations: Conversation[]; notify: (text: string) => void }) {
  const [state, setState] = useState<NotificationState | null>(null);
  const [mine, setMine] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [name, setName] = useState(deviceName);
  const [editing, setEditing] = useState("");
  const unsupported = pushUnsupported();

  const load = async () => {
    const next = await api<NotificationState>("/notifications");
    setState(next);
    return next;
  };
  useEffect(() => {
    let disposed = false;
    (async () => {
      try {
        await load();
        const here = await thisDevice().catch(() => null);
        if (!disposed) setMine(here?.id ?? null);
        if (here && !disposed) await load();
      } catch {
        if (!disposed) setError("Could not load notification settings.");
      }
    })();
    return () => {
      disposed = true;
    };
  }, []);

  async function run(fn: () => Promise<unknown>) {
    setBusy(true);
    setError("");
    try {
      await fn();
    } catch (e) {
      setError(problem(e));
    } finally {
      setBusy(false);
    }
  }

  const devices = state?.devices ?? [];
  const current = devices.find((d) => d.id === mine);
  const others = devices.filter((d) => d.id !== mine);
  const replace = (d: NotificationDevice) => setState((s) => (s ? { ...s, devices: s.devices.map((x) => (x.id === d.id ? d : x)) } : s));

  return (
    <>
      <Group
        title="This device"
        description="Get notified here even when SmsBot is closed. Each device has its own settings."
        aside={current ? <StatusPill tone="ok">On</StatusPill> : <StatusPill tone="idle">Off</StatusPill>}
      >
        {error && (
          <p role="alert" className="inline-alert">
            {error}
          </p>
        )}
        {unsupported ? (
          <Notice role="status">{unsupported}</Notice>
        ) : !state ? (
          <p className="muted">Loading…</p>
        ) : current ? (
          <>
            <DevicePrefs key={current.id} device={current} conversations={conversations} onSaved={replace} onError={setError} />
            <div className="action-row">
              <button type="button" disabled={busy} onClick={() => void run(async () => {
                await api(`/notifications/devices/${encodeURIComponent(current.id)}/test`, "POST");
                notify("Test notification sent");
              })}>
                <Icon name="bell" size={16} />
                Send a test
              </button>
              <button type="button" className="danger" disabled={busy} onClick={() => void run(async () => {
                await disableThisDevice(current.id);
                setMine(null);
                await load();
              })}>
                Turn off on this device
              </button>
            </div>
          </>
        ) : (
          <div className="inline-form">
            <Field label="Device name" hint="Shown in the device list, so you can tell your devices apart.">
              <input value={name} maxLength={60} onChange={(e) => setName(e.target.value)} />
            </Field>
            <button type="button" className="primary" disabled={busy || !name.trim()} onClick={() => void run(async () => {
              const d = await enableThisDevice(state.public_key, name.trim());
              setMine(d.id);
              await load();
              notify("Notifications are on for this device");
            })}>
              <Icon name="bell" size={16} />
              Turn on notifications
            </button>
          </div>
        )}
      </Group>
      {others.length > 0 && (
        <Group title="Other devices" description="Devices that already get notifications. Change their settings here too.">
          <ul className="device-list">
            {others.map((d) => (
              <li key={d.id}>
                <div className="device-row">
                  <span>
                    <strong>{d.label || "Unnamed device"}</strong>
                    <small>{summary(d.prefs)}</small>
                  </span>
                  <button type="button" className="small ghost" aria-expanded={editing === d.id} onClick={() => setEditing(editing === d.id ? "" : d.id)}>
                    {editing === d.id ? "Done" : "Edit"}
                  </button>
                  <button type="button" className="small ghost danger" disabled={busy} onClick={() => void run(async () => {
                    await api(`/notifications/devices/${encodeURIComponent(d.id)}`, "DELETE");
                    await load();
                  })}>
                    Remove
                  </button>
                </div>
                {editing === d.id && <DevicePrefs key={d.id} device={d} conversations={conversations} onSaved={replace} onError={setError} />}
              </li>
            ))}
          </ul>
        </Group>
      )}
    </>
  );
}

/** Edits one device's settings, saving each change as it is made. */
function DevicePrefs({
  device,
  conversations,
  onSaved,
  onError,
}: {
  device: NotificationDevice;
  conversations: Conversation[];
  onSaved: (d: NotificationDevice) => void;
  onError: (text: string) => void;
}) {
  const [label, setLabel] = useState(device.label);
  const [prefs, setPrefs] = useState(device.prefs);
  const [query, setQuery] = useState("");
  const saved = useRef(device);
  useEffect(() => {
    saved.current = device;
  }, [device]);
  // Changes build on the latest edit, not the last render, and a failed save
  // only reverts when no newer change has been made since.
  const latest = useRef(device.prefs);
  const version = useRef(0);

  async function save(next: NotificationPrefs, nextLabel = label) {
    const mine = ++version.current;
    latest.current = next;
    setPrefs(next);
    try {
      const d = await api<NotificationDevice>(`/notifications/devices/${encodeURIComponent(device.id)}`, "PUT", { label: nextLabel.trim(), prefs: next });
      if (mine === version.current) onSaved(d);
    } catch {
      if (mine !== version.current) return;
      latest.current = saved.current.prefs;
      setPrefs(saved.current.prefs);
      onError("The setting was not saved. Try again.");
    }
  }
  const set = <K extends keyof NotificationPrefs>(key: K, value: NotificationPrefs[K]) => void save({ ...latest.current, [key]: value });
  const toggle = (key: "conversations" | "muted", id: string) => {
    const list = latest.current[key];
    set(key, list.includes(id) ? list.filter((x) => x !== id) : [...list, id]);
  };

  const listKey = prefs.messages === "chosen" ? "conversations" : "muted";
  // Picked conversations come first so they stay visible in a long inbox.
  const picked = new Set(prefs[listKey]);
  const shown = conversations
    .filter((c) => (c.label || "").toLocaleLowerCase().includes(query.trim().toLocaleLowerCase()))
    .sort((a, b) => Number(picked.has(b.id)) - Number(picked.has(a.id)));
  return (
    <div className="device-prefs">
      <div className="field">
        <label htmlFor={`device-name-${device.id}`}>Device name</label>
        <input
          id={`device-name-${device.id}`}
          value={label}
          maxLength={60}
          onChange={(e) => setLabel(e.target.value)}
          onBlur={() => label.trim() !== saved.current.label && void save(latest.current, label)}
        />
      </div>
      <div className="field">
        <span className="label">Message notifications</span>
        <Segmented label="Message notifications" value={prefs.messages} options={messageModes} onChange={(v) => set("messages", v)} />
        <small className="hint">
          {{
            all: "Every incoming message.",
            bot_on: "Only conversations with bot replies on.",
            chosen: "Only the conversations you pick below.",
            off: "No message notifications; alerts below still arrive.",
          }[prefs.messages]}
        </small>
      </div>
      {prefs.messages !== "off" && (
        <>
          <Switch label="Show message text" hint="Off shows only who wrote." checked={prefs.show_text} onChange={(v) => set("show_text", v)} />
          <div className="field">
            <span className="label">{listKey === "conversations" ? "Notify for" : "Muted conversations"}</span>
            {conversations.length > 8 && <input type="search" aria-label="Find a conversation" placeholder="Find a conversation" value={query} onChange={(e) => setQuery(e.target.value)} />}
            <div className="chips chip-picker" role="group" aria-label={listKey === "conversations" ? "Conversations to notify for" : "Muted conversations"}>
              {shown.map((c) => {
                const on = picked.has(c.id);
                return (
                  <button key={c.id} type="button" className={"chip" + (on ? " on" : "")} aria-pressed={on} title={c.label || "Conversation"} onClick={() => toggle(listKey, c.id)}>
                    {on && <Icon name="check" size={13} />}
                    <span className="chip-label">{c.label || "Conversation"}</span>
                  </button>
                );
              })}
              {shown.length === 0 && <span className="muted">{conversations.length ? "No conversation matches." : "No conversations loaded yet."}</span>}
            </div>
          </div>
        </>
      )}
      <div className="field">
        <span className="label">Alerts</span>
        <Switch label="Approvals" hint="A reply is waiting for you to approve it." checked={prefs.approvals} onChange={(v) => set("approvals", v)} />
        <Switch label="Problems" hint="A reply failed or its delivery is uncertain." checked={prefs.problems} onChange={(v) => set("problems", v)} />
        <Switch label="Phone connection" hint="SmsBot loses your phone for two minutes, and when it is back." checked={prefs.connection} onChange={(v) => set("connection", v)} />
      </div>
    </div>
  );
}
