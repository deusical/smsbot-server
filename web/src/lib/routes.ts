export const pages = [
  "Home",
  "Conversations",
  "Commands",
  "Activity",
  "Archive",
  "Settings",
] as const;
export type Page = (typeof pages)[number];

export type Route = { page: Page; selected: string };

const segments: Record<Page, string> = {
  Home: "",
  Conversations: "conversations",
  Commands: "commands",
  Activity: "activity",
  Archive: "archive",
  Settings: "settings",
};

/** The URL path of a route. Only Conversations carries a selected id. */
export function routePath({ page, selected }: Route): string {
  const base = "/" + segments[page];
  return page === "Conversations" && selected
    ? `${base}/${encodeURIComponent(selected)}`
    : base;
}

/** The route a URL path shows. Anything unrecognized is Home. */
export function parseRoute(pathname: string): Route {
  const [first, second] = pathname.split("/").filter(Boolean);
  const page = pages.find((p) => segments[p] === (first ?? "")) ?? "Home";
  let selected = "";
  if (page === "Conversations" && second) {
    try {
      selected = decodeURIComponent(second);
    } catch {
      // A malformed escape just means no conversation is selected.
    }
  }
  return { page, selected };
}

/** The route a notification link ("/?open=…") points at, or null. */
export function notificationRoute(search: string): Route | null {
  const params = new URLSearchParams(search);
  const target = params.get("open");
  const id = params.get("id");
  if (target === "conversation" && id) return { page: "Conversations", selected: id };
  if (target === "activity") return { page: "Activity", selected: "" };
  if (target === "settings") return { page: "Settings", selected: "" };
  if (target === "home") return { page: "Home", selected: "" };
  return null;
}
