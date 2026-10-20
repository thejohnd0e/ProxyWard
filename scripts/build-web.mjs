// Builds the web interface and installs it into internal/webui/dist, which the
// Go binary embeds. Only that directory is ever cleaned, and its .gitkeep stays.
//
//   node scripts/build-web.mjs            build (installs dependencies if needed)
//   node scripts/build-web.mjs --skip-install

import { spawnSync } from 'node:child_process';
import { cpSync, existsSync, mkdirSync, readdirSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const web = join(root, 'web');
const built = join(web, 'dist');
const target = join(root, 'internal', 'webui', 'dist');

const npm = process.platform === 'win32' ? 'npm.cmd' : 'npm';

function run(args, cwd) {
  const res = spawnSync(npm, args, { cwd, stdio: 'inherit', shell: process.platform === 'win32' });
  if (res.status !== 0) {
    console.error(`npm ${args.join(' ')} failed`);
    process.exit(res.status ?? 1);
  }
}

if (!process.argv.includes('--skip-install') && !existsSync(join(web, 'node_modules'))) {
  run(['ci', '--no-audit', '--no-fund'], web);
}
run(['run', 'build'], web);

if (!existsSync(join(built, 'index.html'))) {
  console.error('The build did not produce web/dist/index.html');
  process.exit(1);
}

// Safety: refuse to delete anything outside internal/webui/dist.
if (resolve(target) !== join(root, 'internal', 'webui', 'dist') || !resolve(target).startsWith(root + sep)) {
  console.error('Unexpected target directory');
  process.exit(1);
}
mkdirSync(target, { recursive: true });
for (const name of readdirSync(target)) {
  if (name === '.gitkeep') continue;
  rmSync(join(target, name), { recursive: true, force: true });
}
cpSync(built, target, { recursive: true });
writeFileSync(join(target, '.gitkeep'), '');

let files = 0;
let bytes = 0;
(function count(dir) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) count(p);
    else {
      files++;
      bytes += statSync(p).size;
    }
  }
})(target);
console.log(`Installed ${files} files (${(bytes / 1024).toFixed(0)} KiB) into ${target}`);
