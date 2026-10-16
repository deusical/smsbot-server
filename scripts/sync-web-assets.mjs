// Keep the embedded dashboard equal to the current Vite output, preserving .keep.
import {fileURLToPath} from 'node:url';
import {dirname, join, relative} from 'node:path';
import {readdir, readFile, mkdir, copyFile, unlink, lstat} from 'node:fs/promises';
const root = fileURLToPath(new URL('../', import.meta.url));
const source = join(root, 'web', 'dist');
const target = join(root, 'internal', 'httpapi', 'assets');
async function files(dir, base = dir) {
  const result = [];
  for (const entry of await readdir(dir, {withFileTypes: true})) {
    const path = join(dir, entry.name);
    if (entry.isSymbolicLink()) throw new Error('Asset symlinks are not supported');
    if (entry.isDirectory()) result.push(...await files(path, base));
    else if (entry.isFile()) result.push(relative(base, path));
    else throw new Error('Unsupported asset entry');
  }
  return result;
}
// Validate the source before changing the embedded copy.
await readFile(join(source, 'index.html'));
const current = await files(source);
await mkdir(target, {recursive: true});
if ((await lstat(target)).isSymbolicLink()) throw new Error('Asset target must be a directory');
const previous = await files(target);
for (const name of current) {
  const destination = join(target, name);
  await mkdir(dirname(destination), {recursive: true});
  const data = await readFile(join(source, name));
  let old;
  try { old = await readFile(destination); } catch (err) { if (err.code !== 'ENOENT') throw err; }
  if (!old || !data.equals(old)) await copyFile(join(source, name), destination);
}
const retained = new Set([...current, '.keep']);
for (const name of previous) if (!retained.has(name)) await unlink(join(target, name));
console.log(`Embedded dashboard synchronized: ${current.length} files`);
