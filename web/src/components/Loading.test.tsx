import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LoadingState } from "./Loading";

describe("LoadingState", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("stays quiet for a fast load, then shows what is loading, then admits a slow one", () => {
    const { container } = render(<LoadingState what="settings" />);
    const state = container.querySelector(".load-state")!;
    expect(state.getAttribute("data-phase")).toBe("quiet");
    expect(screen.queryByText(/taking longer/)).toBeNull();
    act(() => void vi.advanceTimersByTime(300));
    expect(state.getAttribute("data-phase")).toBe("loading");
    expect(screen.getByRole("status")).toHaveTextContent("Loading settings…");
    act(() => void vi.advanceTimersByTime(6000));
    expect(state.getAttribute("data-phase")).toBe("slow");
    expect(screen.getByText(/taking longer than usual/)).toBeInTheDocument();
    expect(screen.getByText(/don’t need to reload/)).toBeInTheDocument();
  });

  it("shows a failure with the reason and a way to try again", async () => {
    vi.useRealTimers();
    const retry = vi.fn();
    render(<LoadingState what="commands" error="Could not reach the server." onRetry={retry} />);
    expect(screen.getByRole("alert")).toHaveTextContent("Couldn’t load commands");
    expect(screen.getByRole("alert")).toHaveTextContent("Could not reach the server.");
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(retry).toHaveBeenCalledOnce();
  });

  it("offers no retry button when the page retries on its own", () => {
    render(<LoadingState what="activity" error="Connection lost. Retrying…" />);
    expect(screen.queryByRole("button")).toBeNull();
  });
});
