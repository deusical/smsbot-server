import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    // Browser journeys stay in tests/ and are owned by Playwright.
    include: ["src/**/*.test.{ts,tsx}"],
    environment: "jsdom",
    // Worker threads start far faster than the default forked processes,
    // most noticeably on Windows; files stay isolated from each other.
    pool: "threads",
    setupFiles: ["./src/test/setup.ts"],
    restoreMocks: true,
  },
});
