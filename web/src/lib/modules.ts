import { useEffect, useState } from "react";
import { api, type CommandModule } from "./api";

// The module catalog changes only when the server does, so one request per
// page load serves every view.
let catalog: Promise<CommandModule[]> | null = null;

export function loadModules(): Promise<CommandModule[]> {
  if (!catalog)
    catalog = api<CommandModule[]>("/command-modules").catch((e) => {
      catalog = null;
      throw e;
    });
  return catalog;
}

export function useModules(): { modules: CommandModule[]; error: unknown } {
  const [modules, setModules] = useState<CommandModule[]>([]);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    let active = true;
    loadModules()
      .then((m) => active && setModules(m))
      .catch((e) => active && setError(e));
    return () => {
      active = false;
    };
  }, []);
  return { modules, error };
}

/** The module's title, or its id while the catalog loads. */
export function moduleTitle(modules: CommandModule[], id: string): string {
  return modules.find((m) => m.id === id)?.title ?? id;
}

/** Groups for the step picker, by what a module works on. */
export function moduleGroup(id: string): string {
  if (id.startsWith("ai.")) return "AI";
  if (id.startsWith("context.")) return "Memory";
  if (id.startsWith("video.")) return "Video";
  if (id === "reply") return "Send";
  return "Text";
}
