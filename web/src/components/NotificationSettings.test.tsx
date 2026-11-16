import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { clearResources } from "../lib/resource";
import { NotificationSettings } from "./NotificationSettings";

beforeEach(() => clearResources());
afterEach(() => {
  clearResources();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function stubPush(subscribed: boolean) {
  Object.defineProperty(window, "isSecureContext", { configurable: true, value: true });
  vi.stubGlobal("Notification", { permission: "granted", requestPermission: vi.fn(async () => "granted") });
  vi.stubGlobal("PushManager", function PushManager() {});
  const sub = subscribed
    ? {
        endpoint: "https://fcm.googleapis.com/fcm/send/synthetic-device",
        options: { applicationServerKey: null },
        toJSON: () => ({
          endpoint: "https://fcm.googleapis.com/fcm/send/synthetic-device",
          keys: { p256dh: "synthetic-p256dh", auth: "synthetic-auth" },
        }),
        unsubscribe: vi.fn(async () => true),
      }
    : null;
  Object.defineProperty(navigator, "serviceWorker", {
    configurable: true,
    value: {
      getRegistration: vi.fn(async () => ({ pushManager: { getSubscription: vi.fn(async () => sub) } })),
    },
  });
}

it("looks up this device once and then fetches /notifications once on mount and on Refresh", async () => {
  stubPush(true);
  const calls: string[] = [];
  let deviceChecked = false;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const method = init?.method ?? "GET";
      const url = String(input);
      calls.push(`${method} ${url}`);
      if (method === "POST" && url === "/api/v1/notifications/devices") {
        deviceChecked = true;
        return new Response(
          JSON.stringify({
            id: "dev-1",
            label: "Desk browser",
            created: "2026-11-15T00:00:00Z",
            prefs: { messages: "all", conversations: [], muted: [], show_text: true, approvals: true, problems: true, connection: true },
          }),
          { status: 200 },
        );
      }
      expect(deviceChecked).toBe(true);
      return new Response(
        JSON.stringify({
          public_key: "BEl62iUYgUivxIkv69yViEuiBIa-Ib9-SkvMeAtA3LFgDzkrxZJjSgSnfckjBJuBkr3qBUYIHBQFLXYp5Nksh8U",
          devices: [
            {
              id: "dev-1",
              label: "Desk browser",
              created: "2026-11-15T00:00:00Z",
              prefs: { messages: "all", conversations: [], muted: [], show_text: true, approvals: true, problems: true, connection: true },
            },
          ],
        }),
        { status: 200 },
      );
    }),
  );

  render(<NotificationSettings conversations={[]} notify={() => {}} />);
  await act(async () => {});
  expect(calls).toEqual(["POST /api/v1/notifications/devices", "GET /api/v1/notifications"]);
  expect(screen.getByDisplayValue("Desk browser")).toBeTruthy();

  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  });
  expect(calls).toEqual([
    "POST /api/v1/notifications/devices",
    "GET /api/v1/notifications",
    "POST /api/v1/notifications/devices",
    "GET /api/v1/notifications",
  ]);
});

it("fetches /notifications only once when this browser has no subscription", async () => {
  stubPush(false);
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push(`${init?.method ?? "GET"} ${String(input)}`);
      return new Response(JSON.stringify({ public_key: "synthetic", devices: [] }), { status: 200 });
    }),
  );

  render(<NotificationSettings conversations={[]} notify={() => {}} />);
  await act(async () => {});
  expect(calls).toEqual(["GET /api/v1/notifications"]);
  expect(screen.getByRole("button", { name: "Turn on notifications" })).toBeTruthy();
});
