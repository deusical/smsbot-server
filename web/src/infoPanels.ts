import {useState} from "react";

type Panel = "archive-scope" | "sync-details";
const memory = new Map<Panel, boolean>();
// Only a display preference is retained in this tab; never IDs or API data.
export function useInfoPanel(panel: Panel): [boolean, (shown: boolean) => void] {
  const key = `smsbot:hide-info:${panel}`;
  const [shown, setShown] = useState(() => {
    try { if (sessionStorage.getItem(key) === "1") return false; } catch { /* Private browsers may block storage. */ }
    return memory.get(panel) ?? true;
  });
  return [shown, (next) => {
    memory.set(panel, next);
    try { if (next) sessionStorage.removeItem(key); else sessionStorage.setItem(key, "1"); } catch { /* In-memory choice still works. */ }
    setShown(next);
  }];
}
