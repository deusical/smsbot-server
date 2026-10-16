import {test} from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp, mkdir, writeFile, readFile, copyFile, stat, rm, access} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {basename, dirname, join, resolve} from 'node:path';
import {execFileSync} from 'node:child_process';
async function fixture(t) {
  const root = await mkdtemp(join(tmpdir(), 'smsbot-assets-'));
  t.after(async () => {
    assert.equal(dirname(resolve(root)), resolve(tmpdir()));
    assert.ok(basename(root).startsWith('smsbot-assets-'));
    await rm(root, {recursive: true, force: true});
  });
  await mkdir(join(root, 'scripts'));
  await copyFile(new URL('./sync-web-assets.mjs', import.meta.url), join(root, 'scripts', 'sync-web-assets.mjs'));
  const source = join(root, 'web', 'dist');
  const target = join(root, 'internal', 'httpapi', 'assets');
  await mkdir(source, {recursive: true});
  await mkdir(target, {recursive: true});
  const run = () => execFileSync(process.execPath, [join(root, 'scripts', 'sync-web-assets.mjs')], {stdio: 'pipe'});
  return {source, target, run};
}
test('replacement removes obsolete bundles, preserves .keep and avoids rewriting unchanged files', async t => {
  const {source, target, run} = await fixture(t);
  await mkdir(join(source, 'assets'));
  await mkdir(join(target, 'assets'));
  await writeFile(join(source, 'index.html'), 'synthetic current dashboard');
  await writeFile(join(source, 'assets', 'current.js'), 'synthetic bundle');
  await writeFile(join(target, 'assets', 'obsolete.js'), 'synthetic old bundle');
  await writeFile(join(target, '.keep'), '');
  run();
  assert.equal(await readFile(join(target, 'index.html'), 'utf8'), 'synthetic current dashboard');
  assert.equal(await readFile(join(target, 'assets', 'current.js'), 'utf8'), 'synthetic bundle');
  await access(join(target, '.keep'));
  await assert.rejects(access(join(target, 'assets', 'obsolete.js')));
  const before = (await stat(join(target, 'index.html'))).mtimeMs;
  run();
  assert.equal((await stat(join(target, 'index.html'))).mtimeMs, before);
  await writeFile(join(source, 'index.html'), 'synthetic updated dashboard');
  run();
  assert.equal(await readFile(join(target, 'index.html'), 'utf8'), 'synthetic updated dashboard');
});
test('missing production entrypoint leaves the existing embedded copy intact', async t => {
  const {target, run} = await fixture(t);
  await writeFile(join(target, 'index.html'), 'synthetic existing dashboard');
  assert.throws(run);
  assert.equal(await readFile(join(target, 'index.html'), 'utf8'), 'synthetic existing dashboard');
});
