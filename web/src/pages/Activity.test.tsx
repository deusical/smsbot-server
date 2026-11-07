import { render, screen } from "@testing-library/react";
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
});
