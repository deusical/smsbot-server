import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { knownTasks, parseRoute, WorkerSettings, type WorkersView } from "./WorkerSettings";

afterEach(() => vi.unstubAllGlobals());

const view: WorkersView = {
  config: {
    workers: [{ name: "laptop", endpoint: "http://100.92.5.20:8767/download" }],
    routes: { "video.compress": ["laptop", "local"] },
  },
  status: [
    { name: "local", token: true, online: true, tasks: { "video.download": 1, "video.compress": 1 } },
    { name: "laptop", endpoint: "http://100.92.5.20:8767/download", token: true, online: false, tasks: {} },
  ],
  local: true,
};

it("lists every task a worker runs or a route names, and parses routes", () => {
  expect(knownTasks(view)).toEqual(["video.compress", "video.download"]);
  expect(parseRoute(" laptop,  local ,")).toEqual(["laptop", "local"]);
  expect(parseRoute("  ")).toEqual([]);
});

it("shows workers and saves edits without echoing stored tokens", async () => {
  const puts: unknown[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "PUT") puts.push(JSON.parse(String(init.body)));
      return new Response(JSON.stringify(view), { status: 200 });
    }),
  );
  render(<WorkerSettings />);
  await act(async () => {});
  expect(screen.getByText("Online")).toBeTruthy(); // local
  expect(screen.getByText("Offline")).toBeTruthy(); // laptop
  expect((screen.getByDisplayValue("laptop, local") as HTMLInputElement).value).toBe("laptop, local");
  // Blank token keeps the stored one; a route cleared to blank is dropped.
  fireEvent.change(screen.getByDisplayValue("laptop, local"), { target: { value: "" } });
  await act(async () => {
    fireEvent.click(screen.getByText("Save workers"));
  });
  expect(puts).toEqual([{ workers: [{ name: "laptop", endpoint: "http://100.92.5.20:8767/download" }], routes: {} }]);
  expect(screen.getByText("Workers saved.")).toBeTruthy();
});
