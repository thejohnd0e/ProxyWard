import { useId, useState, type FormEvent } from 'react';
import { ApiError } from '../../api/client';
import { useAuth } from '../../app/auth';

function describe(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.code === 'invalid_credentials') return 'The password is not correct.';
    if (err.code === 'rate_limited') return 'Too many attempts. Wait a minute and try again.';
    if (err.code === 'not_initialized') {
      return 'No administrator password is set yet. Run "proxy-checker admin set-password" on the server first.';
    }
    return err.message;
  }
  return 'Sign-in failed. Try again.';
}

export function LoginPage() {
  const { signIn } = useAuth();
  const id = useId();
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (password === '') {
      setError('Enter the administrator password.');
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await signIn(password);
    } catch (err) {
      setError(describe(err));
      setPassword('');
    } finally {
      setBusy(false);
    }
  };

  return (
    <main className="login">
      <form className="login__card" onSubmit={(e) => void submit(e)} noValidate>
        <h1>Proxy Checker</h1>
        <p className="muted">Sign in to see the state of your proxies.</p>
        {error ? (
          <p role="alert" className="notice notice--error">
            {error}
          </p>
        ) : null}
        <div className="field">
          <label htmlFor={`${id}-password`}>Password</label>
          <input
            id={`${id}-password`}
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="current-password"
            autoFocus
          />
        </div>
        <button type="submit" className="button button--primary button--block" disabled={busy}>
          Sign in
        </button>
      </form>
    </main>
  );
}
