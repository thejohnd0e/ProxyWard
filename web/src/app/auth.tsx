import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { onUnauthenticated, setCsrfToken } from '../api/client';
import * as endpoints from '../api/endpoints';

type AuthState = { status: 'loading' } | { status: 'anonymous' } | { status: 'authenticated' };

interface AuthContext {
  state: AuthState;
  signIn: (password: string) => Promise<void>;
  signOut: () => Promise<void>;
  /** Forget the session locally, for example after the password changed. */
  expire: () => void;
}

const Ctx = createContext<AuthContext | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<AuthState>({ status: 'loading' });

  const expire = useCallback(() => {
    setCsrfToken(null);
    setState({ status: 'anonymous' });
  }, []);

  useEffect(() => {
    onUnauthenticated(expire);
    const controller = new AbortController();
    endpoints
      .session(controller.signal)
      .then((s) => {
        if (s.authenticated && s.csrf_token) {
          setCsrfToken(s.csrf_token);
          setState({ status: 'authenticated' });
        } else {
          setState({ status: 'anonymous' });
        }
      })
      .catch(() => {
        if (!controller.signal.aborted) setState({ status: 'anonymous' });
      });
    return () => {
      controller.abort();
      onUnauthenticated(null);
    };
  }, [expire]);

  const signIn = useCallback(async (password: string) => {
    const out = await endpoints.login(password);
    setCsrfToken(out.csrf_token);
    setState({ status: 'authenticated' });
  }, []);

  const signOut = useCallback(async () => {
    try {
      await endpoints.logout();
    } finally {
      expire();
    }
  }, [expire]);

  const value = useMemo(() => ({ state, signIn, signOut, expire }), [state, signIn, signOut, expire]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useAuth(): AuthContext {
  const ctx = useContext(Ctx);
  if (!ctx) throw new Error('useAuth must be used inside AuthProvider');
  return ctx;
}
