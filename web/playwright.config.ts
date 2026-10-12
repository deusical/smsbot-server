import {defineConfig} from '@playwright/test';
export default defineConfig({testDir:'./tests',workers:1,fullyParallel:false,timeout:30000,use:{baseURL:'http://127.0.0.1:8788',trace:'off'},webServer:{command:'node tests/start-fixture.mjs',url:'http://127.0.0.1:8788/healthz',reuseExistingServer:false,timeout:60000},reporter:'list'});
