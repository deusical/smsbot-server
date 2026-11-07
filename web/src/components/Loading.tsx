import { useEffect, useState } from "react";

/**
 * How long a wait has lasted. Fast loads show nothing, so a page that answers in
 * a blink doesn't flash a spinner; a wait that drags says so.
 */
export type LoadPhase = "quiet" | "loading" | "slow";

export function useLoadPhase(active: boolean, quietMs = 250, slowMs = 6000): LoadPhase {
  const [phase, setPhase] = useState<LoadPhase>("quiet");
  useEffect(() => {
    setPhase("quiet");
    if (!active) return;
    const show = setTimeout(() => setPhase("loading"), quietMs);
    const slow = setTimeout(() => setPhase("slow"), slowMs);
    return () => {
      clearTimeout(show);
      clearTimeout(slow);
    };
  }, [active, quietMs, slowMs]);
  return phase;
}

/**
 * What a page shows while its data loads, or when loading failed. The caller
 * says what is loading, the error if there is one, and how to try again. A wait
 * that runs long says the server is slow and still trying, so a user doesn't
 * wonder whether to reload.
 */
export function LoadingState({
  what,
  error,
  onRetry,
  rows = 0,
}: {
  what: string;
  error?: string;
  onRetry?: () => void;
  /** Placeholder rows to hold the shape of a list while it loads. */
  rows?: number;
}) {
  const phase = useLoadPhase(!error);
  if (error)
    return (
      <div role="alert" className="load-state load-error">
        <strong>Couldn’t load {what}</strong>
        <p>{error}</p>
        {onRetry && (
          <button type="button" onClick={onRetry}>
            Try again
          </button>
        )}
      </div>
    );
  return (
    <div role="status" className="load-state" data-phase={phase}>
      <p className="load-line">
        <span className="spinner" aria-hidden="true" />
        Loading {what}…
      </p>
      {phase === "slow" && (
        <p className="muted load-slow">
          This is taking longer than usual. The server may be busy catching up with your messages. It keeps trying, so you
          don’t need to reload.
        </p>
      )}
      {rows > 0 && <SkeletonRows rows={rows} />}
    </div>
  );
}

/** Quiet placeholder rows that hold a list's shape while it loads. */
export function SkeletonRows({ rows }: { rows: number }) {
  return (
    <ul className="skeleton-rows" aria-hidden="true">
      {Array.from({ length: rows }, (_, i) => (
        <li key={i} className="skeleton-row" style={{ "--w": `${55 + ((i * 17) % 35)}%` } as React.CSSProperties} />
      ))}
    </ul>
  );
}
