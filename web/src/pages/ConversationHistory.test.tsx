import { act, fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { Conversation, State } from "../lib/api";
import { ConversationHistory } from "./ConversationHistory";

const mockLoad = vi.fn();

vi.mock("../lib/useConversationMessages", () => ({
  useConversationMessages: () => ({
    messages: [],
    cursor: "",
    error: null,
    loading: false,
    importFinished: true,
    olderNotice: "",
    load: mockLoad,
    refreshNow: vi.fn(),
  }),
}));

vi.mock("../lib/useThreadScroll", () => ({
  useThreadScroll: () => ({
    restore: vi.fn(),
    followSend: vi.fn(),
    onScroll: vi.fn(),
  }),
}));

vi.mock("../lib/useReadReceipts", () => ({
  useReadReceipts: () => vi.fn(),
}));

vi.mock("../lib/useViewedHistoryRefresh", () => ({
  useViewedHistoryRefresh: vi.fn(),
}));

vi.mock("../lib/pendingSends", () => ({
  usePending: () => [],
  useSubmittedSignal: () => vi.fn(),
  removePending: vi.fn(),
}));

const mockApi = vi.fn(async (_path: string, _method?: string, _body?: unknown) => ({}));
vi.mock("../lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/api")>();
  return {
    ...actual,
    api: (path: string, method?: string, body?: unknown) => mockApi(path, method, body),
  };
});

const baseState: State = {
  api_version: 1,
  paused: false,
  transport_ready: true,
  transport: "mock",
  fixture: true,
  capabilities: ["archive_history", "archive_refresh"],
};

const baseConv: Conversation = {
  id: "471",
  participant_id: "106",
  label: "William Tsao",
  protocol: "RCS",
  group: false,
  writable: false,
  enrolled: false,
  archived: true,
  identity_changed: true,
};

describe("ConversationHistory identity changed handling", () => {
  it("renders the identity changed warning banner when identity_changed is true", () => {
    render(
      <ConversationHistory
        conversation={baseConv}
        state={baseState}
        busy={false}
        act={async (fn) => { await fn(); }}
      />,
    );

    expect(screen.getByRole("alert")).toBeDefined();
    expect(
      screen.getByText(/Contact identity was refreshed by Google Messages on your phone/),
    ).toBeDefined();
    expect(screen.getByRole("button", { name: "Verify & Sync Messages" })).toBeDefined();
  });

  it("does not render identity banner when identity_changed is false", () => {
    render(
      <ConversationHistory
        conversation={{ ...baseConv, identity_changed: false, writable: true }}
        state={baseState}
        busy={false}
        act={async (fn) => { await fn(); }}
      />,
    );

    expect(screen.queryByText(/Contact identity was refreshed by Google Messages/)).toBeNull();
  });

  it("calls sync api, patches conversation, and reloads thread on clicking verify button", async () => {
    const onPatch = vi.fn();
    render(
      <ConversationHistory
        conversation={baseConv}
        state={baseState}
        busy={false}
        act={async (fn) => { await fn(); }}
        onPatchConversation={onPatch}
      />,
    );

    const btn = screen.getByRole("button", { name: "Verify & Sync Messages" });
    await act(async () => {
      fireEvent.click(btn);
    });

    expect(mockApi).toHaveBeenCalledWith("/inbox/conversations/471/sync", "POST", { since: "" });
    expect(onPatch).toHaveBeenCalledWith("471", {
      writable: true,
      identity_changed: false,
    });
    expect(mockLoad).toHaveBeenCalledWith("", true);
  });

  it("disables the verify button and shows Updating… when busy is true", () => {
    render(
      <ConversationHistory
        conversation={baseConv}
        state={baseState}
        busy={true}
        act={async (fn) => { await fn(); }}
      />,
    );

    const btn = screen.getByRole("button", { name: "Updating…" });
    expect(btn).toBeDefined();
    expect((btn as HTMLButtonElement).disabled).toBe(true);
  });

  it("takes precedence over groupChanged banner on a group conversation", () => {
    const groupConv: Conversation = {
      ...baseConv,
      group: true,
      roster: "group:p1,p2",
      identity_changed: true,
    };
    render(
      <ConversationHistory
        conversation={groupConv}
        state={baseState}
        busy={false}
        act={async (fn) => { await fn(); }}
      />,
    );

    expect(
      screen.getByText(/Contact identity was refreshed by Google Messages on your phone/),
    ).toBeDefined();
    expect(screen.queryByText(/The people in this group changed/)).toBeNull();
  });

  it("displays identity changed explanation in composer and blocks sending", () => {
    render(
      <ConversationHistory
        conversation={baseConv}
        state={baseState}
        busy={false}
        act={async (fn) => { await fn(); }}
      />,
    );

    expect(
      screen.getByText(/Contact identity changed on phone\. Verify and sync above to send\./),
    ).toBeDefined();
  });

  it("restores focus to textarea after verify and sync completes", async () => {
    const onPatch = vi.fn();
    render(
      <ConversationHistory
        conversation={baseConv}
        state={baseState}
        busy={false}
        act={async (fn) => { await fn(); }}
        onPatchConversation={onPatch}
      />,
    );

    const btn = screen.getByRole("button", { name: "Verify & Sync Messages" });
    await act(async () => {
      fireEvent.click(btn);
    });

    const textarea = screen.getByRole("textbox", { name: "Message" });
    expect(document.activeElement).toBe(textarea);
  });
});
