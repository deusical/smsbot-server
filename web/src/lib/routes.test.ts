import { describe, expect, it } from "vitest";
import { notificationRoute, pages, parseRoute, routePath } from "./routes";

describe("routes", () => {
  it("gives every page its own path and reads it back", () => {
    const paths = pages.map((page) => routePath({ page, selected: "" }));
    expect(new Set(paths).size).toBe(pages.length);
    expect(paths).toEqual(["/", "/conversations", "/commands", "/activity", "/archive", "/settings"]);
    for (const page of pages) expect(parseRoute(routePath({ page, selected: "" })).page).toBe(page);
  });
  it("puts the selected conversation in the path, escaped", () => {
    for (const id of ["demo-chat", "a/b c?d#e%f", "été"]) {
      const path = routePath({ page: "Conversations", selected: id });
      expect(path.startsWith("/conversations/")).toBe(true);
      expect(path.slice("/conversations/".length)).not.toMatch(/[/?# ]/);
      expect(parseRoute(path)).toEqual({ page: "Conversations", selected: id });
    }
  });
  it("ignores a selection on other pages and treats unknown paths as Home", () => {
    expect(routePath({ page: "Settings", selected: "demo-chat" })).toBe("/settings");
    expect(parseRoute("/settings/extra")).toEqual({ page: "Settings", selected: "" });
    expect(parseRoute("/nonsense")).toEqual({ page: "Home", selected: "" });
    expect(parseRoute("/conversations/%E0%A4%A")).toEqual({ page: "Conversations", selected: "" });
    expect(parseRoute("/conversations/")).toEqual({ page: "Conversations", selected: "" });
  });
  it("maps notification links to routes", () => {
    expect(notificationRoute("?open=conversation&id=demo%20chat")).toEqual({ page: "Conversations", selected: "demo chat" });
    expect(notificationRoute("?open=activity")?.page).toBe("Activity");
    expect(notificationRoute("?open=settings")?.page).toBe("Settings");
    expect(notificationRoute("?open=home")?.page).toBe("Home");
    expect(notificationRoute("?open=conversation")).toBeNull();
    expect(notificationRoute("?open=other")).toBeNull();
    expect(notificationRoute("")).toBeNull();
  });
});
