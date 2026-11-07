import { useEffect, useState } from "react";
import { api, ApiError } from "../lib/api";
import { LoadingState } from "./Loading";
import { Field, Group, Notice, StatusPill } from "./ui";

export type WorkerEntry = { name: string; endpoint: string; token_file?: string };
export type WorkerStatus = {
  name: string;
  endpoint?: string;
  token: boolean;
  online: boolean;
  tasks: Record<string, number>;
};
export type WorkersView = {
  config: { workers: WorkerEntry[]; routes?: Record<string, string[]> };
  status: WorkerStatus[];
  local: boolean;
};

type Draft = WorkerEntry & { token: string };

/** Tasks any worker runs, or that already have a route, in name order. */
export function knownTasks(view: WorkersView): string[] {
  const names = new Set(Object.keys(view.config.routes ?? {}));
  for (const s of view.status) Object.keys(s.tasks).forEach((t) => names.add(t));
  return [...names].sort();
}

/** "laptop, local" -> ["laptop", "local"]; blank means the default order. */
export function parseRoute(text: string): string[] {
  return text
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
}

function problem(e: unknown): string {
  if (e instanceof ApiError && e.detail.detail) return e.detail.detail;
  if (e instanceof ApiError && e.status === 503) return "Workers are not available on this server.";
  return "Workers could not be saved. Try again.";
}

/**
 * Workers run tasks for commands, such as downloading and compressing a video.
 * "local" is this server's own worker; others run on other machines and are
 * reached over Tailscale. The same settings live in workers.json on the server.
 */
export function WorkerSettings() {
  const [view, setView] = useState<WorkersView | null>(null);
  const [drafts, setDrafts] = useState<Draft[]>([]);
  const [routes, setRoutes] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);

  function load(v: WorkersView) {
    setView(v);
    setDrafts(v.config.workers.map((w) => ({ ...w, token: "" })));
    setRoutes(Object.fromEntries(Object.entries(v.config.routes ?? {}).map(([t, ws]) => [t, ws.join(", ")])));
  }

  useEffect(() => {
    api<WorkersView>("/workers").then(load, (e) => setError(problem(e)));
  }, []);

  async function save() {
    setBusy(true);
    setError("");
    setSaved(false);
    try {
      const body = {
        workers: drafts.map((d) => ({
          name: d.name.trim(),
          endpoint: d.endpoint.trim(),
          ...(d.token_file ? { token_file: d.token_file } : {}),
          ...(d.token.trim() ? { token: d.token.trim() } : {}),
        })),
        routes: Object.fromEntries(
          Object.entries(routes)
            .map(([t, text]) => [t, parseRoute(text)] as const)
            .filter(([, ws]) => ws.length > 0),
        ),
      };
      load(await api<WorkersView>("/workers", "PUT", body));
      setSaved(true);
    } catch (e) {
      setError(problem(e));
    } finally {
      setBusy(false);
    }
  }

  function edit(i: number, patch: Partial<Draft>) {
    setDrafts((ds) => ds.map((d, j) => (j === i ? { ...d, ...patch } : d)));
    setSaved(false);
  }

  if (!view) return error ? <Notice tone="bad" role="alert">{error}</Notice> : <LoadingState what="workers" />;
  const status = (name: string) => view.status.find((s) => s.name === name);
  const tasks = knownTasks(view);

  return (
    <>
      <Group
        title="Workers"
        description="Workers run tasks for commands, such as downloading and compressing a video. A task goes to the first worker in order that runs it and is online. Workers on other machines are reached over Tailscale; start one with scripts/start-worker.ps1."
      >
        <ul className="worker-list">
          {view.local && <WorkerRow status={status("local")} name="local" />}
          {drafts.map((d, i) => (
            <li key={i}>
              <WorkerRow status={status(d.name)} name={d.name || "New worker"} />
              <div className="form-grid">
                <Field label="Name" hint="Lowercase letters, digits or -.">
                  <input value={d.name} onChange={(e) => edit(i, { name: e.target.value })} />
                </Field>
                <Field label="Address" hint="http://<Tailscale address>:8767/download">
                  <input value={d.endpoint} onChange={(e) => edit(i, { endpoint: e.target.value })} />
                </Field>
                {d.token_file ? (
                  <Field label="Token">
                    <input value={"From " + d.token_file} disabled />
                  </Field>
                ) : (
                  <Field label="Token" hint="The worker's token file contents. Stored encrypted, never shown.">
                    <input
                      type="password"
                      autoComplete="off"
                      value={d.token}
                      placeholder={status(d.name)?.token ? "Stored" : ""}
                      onChange={(e) => edit(i, { token: e.target.value })}
                    />
                  </Field>
                )}
              </div>
              <button type="button" className="danger" onClick={() => setDrafts((ds) => ds.filter((_, j) => j !== i))}>
                Remove
              </button>
            </li>
          ))}
        </ul>
        <button type="button" onClick={() => setDrafts((ds) => [...ds, { name: "", endpoint: "", token: "" }])}>
          Add worker
        </button>
      </Group>
      <Group
        title="Where tasks run"
        description="By default a task may run on every worker, in the order above, then local. To choose, list workers in order, separated by commas."
      >
        {tasks.length === 0 ? (
          <p className="muted">No worker has reported its tasks yet.</p>
        ) : (
          <div className="form-grid">
            {tasks.map((t) => (
              <Field key={t} label={t}>
                <input
                  value={routes[t] ?? ""}
                  placeholder="Any worker"
                  onChange={(e) => {
                    setRoutes((r) => ({ ...r, [t]: e.target.value }));
                    setSaved(false);
                  }}
                />
              </Field>
            ))}
          </div>
        )}
      </Group>
      {error && (
        <Notice tone="bad" role="alert">
          {error}
        </Notice>
      )}
      {saved && (
        <Notice tone="ok" role="status">
          Workers saved.
        </Notice>
      )}
      <button type="button" className="primary" disabled={busy} onClick={() => void save()}>
        Save workers
      </button>
    </>
  );
}

function WorkerRow({ name, status }: { name: string; status?: WorkerStatus }) {
  const tasks = Object.entries(status?.tasks ?? {})
    .map(([t, v]) => `${t}@${v}`)
    .sort();
  const pill = !status ? (
    <StatusPill tone="idle">Not saved</StatusPill>
  ) : !status.token ? (
    <StatusPill tone="wait">No token</StatusPill>
  ) : status.online ? (
    <StatusPill tone="ok">Online</StatusPill>
  ) : (
    <StatusPill tone="bad">Offline</StatusPill>
  );
  return (
    <div className="worker-row">
      <strong>{name}</strong> {pill}
      <span className="muted">{tasks.length ? tasks.join(", ") : status?.online ? "No tasks" : ""}</span>
    </div>
  );
}
