import { useId, useState, type FormEvent } from 'react';
import { ApiError } from '../../api/client';
import { useAuth } from '../../app/auth';

const MIN_LENGTH = 12;

function describe(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.code === 'already_initialized') {
      return 'An administrator password was just created by someone else. Reload the page and sign in.';
    }
    if (err.code === 'rate_limited') return 'Too many attempts. Wait a minute and try again.';
    return err.message;
  }
  return 'Could not create the password. Try again.';
}

export function SetupPage() {
  const { createAdmin } = useAuth();
  const id = useId();
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if ([...password].length < MIN_LENGTH) {
      setError(`Use at least ${MIN_LENGTH} characters.`);
      return;
    }
    if (password !== confirm) {
      setError('The two passwords do not match.');
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await createAdmin(password);
    } catch (err) {
      setError(describe(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <main className="login">
      <form className="login__card" onSubmit={(e) => void submit(e)} noValidate>
        <h1>Welcome to ProxyWard</h1>
        <p className="muted">Create the administrator password. You will use it to sign in.</p>
        {error ? (
          <p role="alert" className="notice notice--error">
            {error}
          </p>
        ) : null}
        <div className="field">
          <label htmlFor={`${id}-password`}>New password</label>
          <input
            id={`${id}-password`}
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="new-password"
            autoFocus
          />
          <p className="hint">At least {MIN_LENGTH} characters.</p>
        </div>
        <div className="field">
          <label htmlFor={`${id}-confirm`}>Repeat the password</label>
          <input
            id={`${id}-confirm`}
            type="password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            autoComplete="new-password"
          />
        </div>
        <button type="submit" className="button button--primary button--block" disabled={busy}>
          Create password
        </button>
      </form>
    </main>
  );
}
