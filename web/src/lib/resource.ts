import { useCallback, useEffect, useRef, useState } from "react";

/**
 * Data a page has already loaded, kept so coming back to the page shows it at
 * once while a fresh copy loads quietly in the background.
 *
 * Memory only: nothing is written to browser storage, so a browser refresh or
 * closing the tab starts clean, and signing out forgets it all.
 */
type Entry = { data: unknown; at: number };
const memory = new Map<string, Entry>();

function read<T>(key: string): { data: T; at: number } | undefined {
  return memory.get(key) as { data: T; at: number } | undefined;
}

/** The kept copy of a resource, if any, without fetching anything. */
export function peekResource<T>(key: string): { data: T; at: number } | undefined {
  return read<T>(key);
}

export function storeResource<T>(key: string, data: T) {
  memory.set(key, { data, at: Date.now() });
}

/** Forget everything kept, for example when the user signs out. */
export function clearResources() {
  memory.clear();
}

export type Resource<T> = {
  /** The latest copy, or null until the first load finishes. May be a kept one. */
  data: T | null;
  /** When data was loaded, in ms since the epoch. */
  updatedAt: number | null;
  /** True while a fresh copy is being fetched, including the quiet first one. */
  refreshing: boolean;
  /** Why the last attempt failed. With data, the page keeps showing it. */
  error: string;
  refresh: () => Promise<void>;
  /** Replace the kept copy, for example with the answer to a save. */
  set: (value: T) => void;
};

/**
 * Loads a resource, showing any kept copy first. A fresh copy is always
 * fetched when the page opens and whenever refresh() is called. Failures keep
 * the old data on screen rather than replacing it with an error.
 */
export function useResource<T>(
  key: string,
  load: () => Promise<T>,
  describe: (e: unknown) => string,
  { enabled = true }: { enabled?: boolean } = {},
): Resource<T> {
  const [entry, setEntry] = useState(() => read<T>(key));
  const [refreshing, setRefreshing] = useState(enabled);
  const [error, setError] = useState("");
  const loader = useRef(load);
  loader.current = load;
  const explain = useRef(describe);
  explain.current = describe;
  const alive = useRef(true);
  const inFlight = useRef<Promise<void> | null>(null);
  const current = useRef(key);
  current.current = key;

  const refresh = useCallback(() => {
    if (inFlight.current) return inFlight.current;
    const forKey = key;
    setRefreshing(true);
    const run = loader
      .current()
      .then((data) => {
        storeResource(forKey, data);
        if (alive.current && current.current === forKey) {
          setEntry({ data, at: Date.now() });
          setError("");
        }
      })
      .catch((e) => {
        if (alive.current && current.current === forKey) setError(explain.current(e));
      })
      .finally(() => {
        inFlight.current = null;
        if (alive.current) setRefreshing(false);
      });
    inFlight.current = run;
    return run;
  }, [key]);

  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  useEffect(() => {
    setEntry(read<T>(key));
    setError("");
    if (enabled) void refresh();
    else setRefreshing(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, enabled]);

  const set = useCallback(
    (value: T) => {
      storeResource(key, value);
      setEntry({ data: value, at: Date.now() });
      setError("");
    },
    [key],
  );

  return { data: entry ? entry.data : null, updatedAt: entry?.at ?? null, refreshing, error, refresh, set };
}
