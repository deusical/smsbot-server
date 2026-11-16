import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { Job } from "../lib/api";
import { ActivityFeed } from "./Activity";

vi.mock("../lib/modules", () => ({
  useModules: () => ({ modules: [], error: null }),
  moduleTitle: (_: unknown, id: string) => id,
}));

const job = (extra: Partial<Job>): Job => ({
  id: "j1",
  conversation_id: "c1",
  command: "!download",
  state: "queued",
  created_at: "2026-11-07T17:39:20Z",
  ...extra,
});

const status = (j: Job) => {
  const { container } = render(<ActivityFeed jobs={[j]} conversations={[]} />);
  return container.querySelector(".status")!.textContent;
};

describe("Activity request status", () => {
  it("says Working once a worker has started on a queued request", () => {
    expect(status(job({ preparation_stage: "downloading" }))).toMatch(/^Working/);
  });
  it("keeps Queued while nothing has started or the worker is unreachable", () => {
    expect(status(job({}))).toMatch(/^Queued/);
    expect(status(job({ preparation_stage: "waiting_worker" }))).toMatch(/^Queued/);
    expect(screen.queryByText(/^Working/)).toBeNull();
  });
  it("shows the reply step as current before send, not-run on refusal, and done once sent", () => {
    const baseSteps = [
      { id: "text", module: "text.template", done: true },
      { id: "reply", module: "reply", done: false },
    ];
    const { container, unmount } = render(
      <ActivityFeed
        jobs={[job({ state: "awaiting_approval", steps: baseSteps })]}
        conversations={[]}
      />,
    );
    expect(container.querySelector(".status")!.textContent).toContain("· reply");
    fireEvent.click(container.querySelector(".feed-main")!);
    expect(container.querySelector("li.done")?.textContent).toContain("text");
    expect(container.querySelector("li.current")?.textContent).toContain("reply");
    unmount();

    const refused = render(
      <ActivityFeed
        jobs={[job({ state: "failed", code: "not_submitted", steps: baseSteps })]}
        conversations={[]}
      />,
    );
    fireEvent.click(refused.container.querySelector(".feed-main")!);
    expect(refused.container.querySelector("li.not-run")?.textContent).toContain(
      "reply",
    );
    refused.unmount();

    const sent = render(
      <ActivityFeed
        jobs={[
          job({
            state: "submitted",
            steps: [
              { id: "text", module: "text.template", done: true },
              { id: "reply", module: "reply", done: true },
            ],
          }),
        ]}
        conversations={[]}
      />,
    );
    fireEvent.click(sent.container.querySelector(".feed-main")!);
    expect(sent.container.querySelectorAll("li.done")).toHaveLength(2);
  });
});
