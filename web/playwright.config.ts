import {defineConfig} from '@playwright/test';
import {shardSelection} from './tests/shards';
export default defineConfig({testDir:'./tests',...shardSelection(process.env.SMSBOT_SHARD),workers:1,fullyParallel:false,timeout:30000,use:{baseURL:'http://127.0.0.1:8788',trace:'off',serviceWorkers:'block'},webServer:{command:'node tests/start-fixture.mjs',url:'http://127.0.0.1:8788/healthz',reuseExistingServer:false,timeout:60000},reporter:'list'});
