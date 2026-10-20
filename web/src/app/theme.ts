import { useCallback, useEffect, useState } from 'react';

export type ThemeChoice = 'system' | 'light' | 'dark';

const KEY = 'proxyward.theme';

function read(): ThemeChoice {
  try {
    const v = window.localStorage.getItem(KEY);
    if (v === 'light' || v === 'dark' || v === 'system') return v;
  } catch {
    // Storage can be blocked; the system theme is a fine default.
  }
  return 'system';
}

/** "system" leaves the attribute off so the CSS follows prefers-color-scheme. */
export function applyTheme(choice: ThemeChoice): void {
  const root = document.documentElement;
  if (choice === 'system') root.removeAttribute('data-theme');
  else root.setAttribute('data-theme', choice);
}

export function useTheme(): [ThemeChoice, (c: ThemeChoice) => void] {
  const [choice, setChoice] = useState<ThemeChoice>(read);

  useEffect(() => {
    applyTheme(choice);
  }, [choice]);

  const update = useCallback((c: ThemeChoice) => {
    setChoice(c);
    try {
      window.localStorage.setItem(KEY, c);
    } catch {
      // Not persisted; the choice still applies for this session.
    }
  }, []);

  return [choice, update];
}
