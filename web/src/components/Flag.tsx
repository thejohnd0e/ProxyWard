import { useEffect, useState } from 'react';

// Flags are bundled with the app (flag-icons, MIT): nothing is loaded from a CDN.
const flagLoaders = import.meta.glob('../../node_modules/flag-icons/flags/4x3/*.svg', {
  query: '?url',
  import: 'default',
}) as Record<string, () => Promise<string>>;

const byCode = new Map<string, () => Promise<string>>();
for (const [path, loader] of Object.entries(flagLoaders)) {
  const code = /\/([a-z]{2})\.svg$/.exec(path)?.[1];
  if (code) byCode.set(code, loader);
}

/** Decorative flag for an ISO 3166 country code; the country name is always written next to it. */
export function Flag({ code }: { code: string }) {
  const [url, setUrl] = useState<string | null>(null);
  const lower = code.toLowerCase();

  useEffect(() => {
    let live = true;
    const loader = byCode.get(lower);
    if (!loader) {
      setUrl(null);
      return;
    }
    void loader().then((u) => {
      if (live) setUrl(u);
    });
    return () => {
      live = false;
    };
  }, [lower]);

  if (!url) return <span className="flag flag--empty" aria-hidden="true" />;
  return <img className="flag" src={url} alt="" width={20} height={15} loading="lazy" />;
}
