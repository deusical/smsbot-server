// Each Playwright worker talks to its own fixture server (see playwright.config.ts),
// so test files on different workers never share fixture state.
export const port = 8788 + Number(process.env.TEST_PARALLEL_INDEX ?? 0);
export const origin = `http://127.0.0.1:${port}`;
export const connectionFile = `.test-state/connection-${port}.json`;
