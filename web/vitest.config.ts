import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    // Browser journeys stay in tests/ and are owned by Playwright.
    include: ["src/**/*.test.{ts,tsx}"],
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    restoreMocks: true,
  },
});
