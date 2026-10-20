// Starts the end-to-end stand: the real application wired to local fixture
// proxies and fake public services (see internal/e2e). Used by Playwright and
// for manual checks. Nothing here talks to the internet.
//
//   node scripts/e2e-server.mjs                  app on 127.0.0.1:18080, control on :18081

import { spawn, spawnSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const outDir = join(root, '.e2e');
const exe = join(outDir, process.platform === 'win32' ? 'e2e-server.exe' : 'e2e-server');
const go = process.env.GO || 'go';

mkdirSync(outDir, { recursive: true });
const build = spawnSync(go, ['test', '-tags', 'e2e', '-c', '-o', exe, './internal/e2e'], { cwd: root, stdio: 'inherit' });
if (build.status !== 0) {
  console.error('Could not build the e2e stand');
  process.exit(build.status ?? 1);
}

const child = spawn(exe, ['-test.run', '^TestServe$', '-test.timeout', '0'], {
  cwd: root,
  stdio: 'inherit',
  env: { ...process.env, E2E_SERVE: '1' },
});

const stop = (signal) => () => {
  if (!child.killed) child.kill(signal);
};
process.on('SIGINT', stop('SIGINT'));
process.on('SIGTERM', stop('SIGTERM'));
child.on('exit', (code) => process.exit(code ?? 0));
