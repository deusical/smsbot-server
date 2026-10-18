import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach, beforeEach, vi } from "vitest";

// Explicit cleanup keeps test globals disabled and unmounts before timers reset.
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

// Unit/component tests must never reach a real service or provider.
beforeEach(() => {
  vi.stubGlobal("fetch", () => {
    throw new Error("Unexpected fetch: provide a synthetic response in this test");
  });
});
