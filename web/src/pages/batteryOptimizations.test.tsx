import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Conversation, Message, State } from "../lib/api";
import { ConversationHistory } from "./ConversationHistory";
import { MessageActions } from "../components/MessageActions";

describe("MessageActions on-demand loading and error isolation", () => {
  const message: Message = {
    id: "msg-1",
    direction: "incoming",
    text: "Hello from test",
    at: new Date().toISOString(),
    protocol: "RCS",
  };

  beforeEach(() => {
    vi.stubGlobal("matchMedia", vi.fn((query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })));
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("does not fetch capabilities on mount, loads on demand, and keeps local actions accessible", async () => {
    const user = userEvent.setup();
    let actionsCalled = 0;
    let resolveActions!: (value: unknown) => void;
    const actionsPromise = new Promise(resolve => {
      resolveActions = resolve;
    });

    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/messages/msg-1/actions")) {
        actionsCalled++;
        await actionsPromise;
        return new Response(
          JSON.stringify({
            reply: true,
            react: true,
            edit: false,
            version: "v-1",
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      return new Response("{}", { status: 200 });
    }));

    render(
      <MessageActions
        message={message}
        conversation="conv-1"
        label="Alice"
        writable={true}
        eligibilityKey="elig-1"
        onReply={vi.fn()}
        onChanged={vi.fn()}
      />,
    );

    // No capabilities should be fetched merely because the component mounted in viewport
    expect(actionsCalled).toBe(0);

    // Open message actions
    await user.click(screen.getByRole("button", { name: "Message actions" }));

    // Now actions should be requested on demand
    expect(actionsCalled).toBe(1);

    // Local actions ("Copy text" and "Details") are immediately accessible while capabilities load
    expect(screen.getByRole("status")).toHaveTextContent("Loading actions...");
    expect(screen.getByRole("button", { name: "Copy text" })).toBeVisible();
    expect(screen.getByRole("button", { name: "Details" })).toBeVisible();
    expect(screen.queryByRole("button", { name: "Reply" })).not.toBeInTheDocument();

    // Resolve capabilities
    resolveActions(null);

    // Once capabilities arrive, Reply button and reaction picker appear
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Reply" })).toBeVisible();
    });
    expect(screen.getByRole("button", { name: "React 👍" })).toBeVisible();
  });

  it("re-fetches capabilities declaratively if message updates while the dialog is open", async () => {
    const user = userEvent.setup();
    let actionsCalled = 0;

    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/messages/msg-1/actions")) {
        actionsCalled++;
        return new Response(
          JSON.stringify({
            reply: true,
            react: true,
            edit: false,
            version: `v-${actionsCalled}`,
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      return new Response("{}", { status: 200 });
    }));

    const { rerender } = render(
      <MessageActions
        message={{ ...message, revision: 1 }}
        conversation="conv-1"
        label="Alice"
        writable={true}
        eligibilityKey="elig-1"
        onReply={vi.fn()}
        onChanged={vi.fn()}
      />,
    );

    // Open message actions
    await user.click(screen.getByRole("button", { name: "Message actions" }));
    await waitFor(() => {
      expect(actionsCalled).toBe(1);
      expect(screen.getByRole("button", { name: "Reply" })).toBeVisible();
    });

    // Background poll updates message revision while dialog is open
    rerender(
      <MessageActions
        message={{ ...message, revision: 2 }}
        conversation="conv-1"
        label="Alice"
        writable={true}
        eligibilityKey="elig-1"
        onReply={vi.fn()}
        onChanged={vi.fn()}
      />,
    );

    // Re-fetch should be triggered automatically without getting stuck on "Loading actions..."
    await waitFor(() => {
      expect(actionsCalled).toBe(2);
      expect(screen.getByRole("button", { name: "Reply" })).toBeVisible();
    });
  });

  it("isolates copy text error from capabilities error and keeps details view clean", async () => {
    const user = userEvent.setup();

    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/messages/msg-1/actions")) {
        return new Response("Service Unavailable", { status: 503 });
      }
      return new Response("{}", { status: 200 });
    }));

    // Mock clipboard failure
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: {
        writeText: vi.fn().mockRejectedValue(new Error("Permission denied")),
      },
    });

    render(
      <MessageActions
        message={message}
        conversation="conv-1"
        label="Alice"
        writable={true}
        eligibilityKey="elig-1"
        onReply={vi.fn()}
        onChanged={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Message actions" }));

    // Capability failure produces capability alert and Try again button
    await waitFor(() => {
      expect(screen.getByText("Message actions are unavailable. Try again.")).toBeVisible();
    });

    // Click details: capability error banner does not leak into details view
    await user.click(screen.getByRole("button", { name: "Details" }));
    expect(screen.getByRole("dialog", { name: "Message details" })).toBeVisible();
    expect(screen.queryByText("Message actions are unavailable. Try again.")).not.toBeInTheDocument();

    // Go back to actions
    await user.click(screen.getByRole("button", { name: "Back to actions" }));
    expect(screen.getByText("Message actions are unavailable. Try again.")).toBeVisible();

    // Click copy text: copy failure produces distinct copy error message
    await user.click(screen.getByRole("button", { name: "Copy text" }));
    await waitFor(() => {
      expect(screen.getByText("Could not copy. Select the message text to copy it.")).toBeVisible();
    });
    // The capability Try again button still exists for capabilities, not copy
    expect(screen.getByRole("button", { name: "Try again" })).toBeVisible();
  });
});

describe("ConversationHistory visibility guard", () => {
  const conversation: Conversation = {
    id: "conv-1",
    participant_id: "alice-1",
    label: "Alice",
    protocol: "RCS",
    group: false,
    writable: true,
    enrolled: true,
  };

  const state: State = {
    api_version: 1,
    paused: false,
    transport_ready: true,
    transport: "google",
    fixture: true,
    capabilities: ["archive_history"],
  };

  const originalDescriptor =
    Object.getOwnPropertyDescriptor(Document.prototype, "visibilityState") ||
    Object.getOwnPropertyDescriptor(document, "visibilityState");

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
    if (originalDescriptor) {
      Object.defineProperty(document, "visibilityState", originalDescriptor);
    }
  });

  it("suspends polling timer when document is hidden and resumes immediately when visible", async () => {
    vi.useFakeTimers();
    let pollCount = 0;

    vi.stubGlobal("matchMedia", vi.fn((query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })));

    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/changes") || url.includes("/history")) {
        pollCount++;
        return new Response(
          JSON.stringify({
            messages: [],
            next_cursor: "",
            change_cursor: 1,
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      return new Response("{}", { status: 200 });
    }));

    // Start with visible document
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "visible",
    });

    render(
      <ConversationHistory
        conversation={conversation}
        state={state}
        busy={false}
        act={vi.fn()}
      />,
    );

    // Allow initial load to run
    await act(async () => {
      await vi.runOnlyPendingTimersAsync();
    });
    expect(pollCount).toBeGreaterThanOrEqual(1);
    const countAfterMount = pollCount;

    // Simulate tab hiding / phone screen locked
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "hidden",
    });

    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });

    // Advance fake timers by 9 seconds (3 polling cycles while hidden)
    await act(async () => {
      vi.advanceTimersByTime(9000);
      await vi.runOnlyPendingTimersAsync();
    });

    // Polling timer was stopped, so pollCount must not increment while hidden
    expect(pollCount).toBe(countAfterMount);

    // Simulate tab becoming visible again
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "visible",
    });

    await act(async () => {
      document.dispatchEvent(new Event("visibilitychange"));
      await vi.runOnlyPendingTimersAsync();
    });

    // Immediate catch-up poll occurred upon returning to visible
    expect(pollCount).toBeGreaterThan(countAfterMount);
    const countAfterVisible = pollCount;

    // And timer is restarted: advance by 3000ms
    await act(async () => {
      vi.advanceTimersByTime(3000);
      await vi.runOnlyPendingTimersAsync();
    });

    expect(pollCount).toBeGreaterThan(countAfterVisible);
  });
});
