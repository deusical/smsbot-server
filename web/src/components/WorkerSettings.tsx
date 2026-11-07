import { useEffect, useRef, useState } from "react";
import { api, ApiError } from "../lib/api";
import { LoadingState } from "./Loading";
import { RefreshBar } from "./RefreshBar";
import { useResource } from "../lib/resource";
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

function loadProblem(e: unknown): string {
  if (e instanceof ApiError && e.status === 503) return "Workers are not available on this server.";
  return "Could not reach the server.";
}

const draftsOf = (v: WorkersView): Draft[] => v.config.workers.map((w) => ({ ...w, token: "" }));
const routesOf = (v: WorkersView): Record<string, string> =>
  Object.fromEntries(Object.entries(v.config.routes ?? {}).map(([t, ws]) => [t, ws.join(", ")]));

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
  // What was loaded last time shows at once; a fresh copy replaces it quietly.
  const resource = useResource<WorkersView>("workers", () => api<WorkersView>("/workers"), loadProblem);
  const view = resource.data;
  const [drafts, setDrafts] = useState<Draft[]>(() => (view ? draftsOf(view) : []));
  const [routes, setRoutes] = useState<Record<string, string>>(() => (view ? routesOf(view) : {}));
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);
  // The saved settings the drafts were last copied from, and whether the user
  // has edited since. A refresh never overwrites edits in progress.
  const synced = useRef<string | null>(view ? JSON.stringify(view.config) : null);
  const touched = useRef(false);

  useEffect(() => {
    if (!view) return;
    const signature = JSON.stringify(view.config);
    if (synced.current === signature || (synced.current !== null && touched.current)) return;
    synced.current = signature;
    setDrafts(draftsOf(view));
    setRoutes(routesOf(view));
  }, [view]);

  function load(v: WorkersView) {
    touched.current = false;
    synced.current = JSON.stringify(v.config);
    resource.set(v);
    setDrafts(draftsOf(v));
    setRoutes(routesOf(v));
  }

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
    touched.current = true;
    setDrafts((ds) => ds.map((d, j) => (j === i ? { ...d, ...patch } : d)));
    setSaved(false);
  }

  if (!view)
    return (
      <LoadingState
        what="workers"
        error={resource.error}
        onRetry={() => void resource.refresh()}
      />
    );
  const status = (name: string) => view.status.find((s) => s.name === name);
  const tasks = knownTasks(view);

  return (
    <>
      <RefreshBar
        updatedAt={resource.updatedAt}
        refreshing={resource.refreshing}
        error={resource.error}
        onRefresh={() => void resource.refresh()}
      />
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
              <button type="button" className="danger" onClick={() => {
                  touched.current = true;
                  setDrafts((ds) => ds.filter((_, j) => j !== i));
                }}>
                Remove
              </button>
            </li>
          ))}
        </ul>
        <button type="button" onClick={() => {
            touched.current = true;
            setDrafts((ds) => [...ds, { name: "", endpoint: "", token: "" }]);
          }}>
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
                    touched.current = true;
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
