import {defineConfig} from '@playwright/test';
// One fixture server per worker: files stay serial within a worker (state carries
// over between them as before), and workers run side by side on separate servers.
// tests/start-fixture.mjs starts them all; 8787/ready answers once every one is up.
// PW_CHANNEL=chrome uses an installed Google Chrome instead of Playwright's Chromium.
const workers=Number(process.env.PW_WORKERS??4);
const port=8788+Number(process.env.TEST_PARALLEL_INDEX??0);
export default defineConfig({testDir:'./tests',workers,fullyParallel:false,timeout:30000,use:{baseURL:`http://127.0.0.1:${port}`,channel:process.env.PW_CHANNEL||undefined,trace:'off',serviceWorkers:'block'},webServer:{command:`node tests/start-fixture.mjs ${workers}`,url:'http://127.0.0.1:8787/ready',reuseExistingServer:false,timeout:60000},reporter:'list'});
