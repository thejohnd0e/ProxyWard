import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

// The project is English only: no Cyrillic text in sources, styles or pages.
// (User-supplied data such as proxy names is not restricted and never lives here.)
const CYRILLIC = /[Ѐ-ӿ]/;

function walk(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) walk(path, out);
    else if (/\.(tsx?|css|html|json|md|svg)$/.test(name)) out.push(path);
  }
  return out;
}

describe('English-only project content', () => {
  it('contains no Cyrillic text in the web sources', () => {
    const offenders = [...walk('src'), ...walk('e2e'), 'index.html'].filter((file) => {
      // This file contains the pattern itself.
      if (file.endsWith('english.test.ts')) return false;
      return CYRILLIC.test(readFileSync(file, 'utf8'));
    });
    expect(offenders).toEqual([]);
  });

  it('has no language selector or translation catalogs', () => {
    const files = walk('src').map((f) => f.replace(/\\/g, '/'));
    expect(files.filter((f) => /\/(i18n|locales?|translations?)\//i.test(f))).toEqual([]);
    const pkg = JSON.parse(readFileSync('package.json', 'utf8')) as { dependencies: Record<string, string> };
    expect(Object.keys(pkg.dependencies).filter((d) => /i18n|intl|lingui|formatjs/i.test(d))).toEqual([]);
  });
});
