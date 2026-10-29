import { useCallback, useId, useState, type FormEvent, type ReactNode } from 'react';
import { ApiError } from '../../api/client';
import { changePassword, getSettings, putSettings, resumeSpeed, testTelegram } from '../../api/endpoints';
import type { SettingsUpdate, SettingsView } from '../../api/types';
import { useAuth } from '../../app/auth';
import { useTheme, type ThemeChoice } from '../../app/theme';
import { formatDateTime, splitInterval, toMinutes, type IntervalUnit } from '../../lib/format';
import { useVisiblePolling } from '../../lib/useVisiblePolling';

type Message = { kind: 'ok' | 'error'; text: string } | null;

function Msg({ message }: { message: Message }) {
  if (!message) return null;
  return (
    <p role={message.kind === 'error' ? 'alert' : 'status'} className={`notice${message.kind === 'error' ? ' notice--error' : ' notice--ok'}`}>
      {message.text}
    </p>
  );
}

function errorText(err: unknown): string {
  if (err instanceof ApiError) {
    const first = Object.values(err.fields)[0];
    return first ? `${err.message} ${first}` : err.message;
  }
  return 'Something went wrong. Try again.';
}

function Card({ title, children }: { title: string; children: ReactNode }) {
  const id = useId();
  return (
    <section className="card" aria-labelledby={id}>
      <h2 id={id}>{title}</h2>
      {children}
    </section>
  );
}

function IntervalInput(props: { label: string; minutes: number; onChange: (minutes: number) => void }) {
  const id = useId();
  const initial = splitInterval(props.minutes);
  const [value, setValue] = useState(String(initial.value));
  const [unit, setUnit] = useState<IntervalUnit>(initial.unit);
  const push = (v: string, u: IntervalUnit) => {
    const n = Number(v);
    props.onChange(Number.isFinite(n) ? toMinutes(n, u) : NaN);
  };
  return (
    <div className="field-row field-row--tight">
      <div className="field field--port">
        <label htmlFor={`${id}-v`}>{props.label}</label>
        <input id={`${id}-v`} value={value} inputMode="numeric" onChange={(e) => { setValue(e.target.value); push(e.target.value, unit); }} />
      </div>
      <div className="field">
        <label htmlFor={`${id}-u`}>Unit</label>
        <select id={`${id}-u`} value={unit} onChange={(e) => { const u = e.target.value as IntervalUnit; setUnit(u); push(value, u); }}>
          <option value="minutes">minutes</option>
          <option value="hours">hours</option>
        </select>
      </div>
    </div>
  );
}

export function SettingsPage() {
  const [settings, setSettings] = useState<SettingsView | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);

  const refresh = useCallback(async (signal?: AbortSignal) => {
    try {
      setSettings(await getSettings(signal));
      setLoadError(null);
    } catch (err) {
      if (signal?.aborted) return;
      setLoadError(errorText(err));
    }
  }, []);

  // Only the delivery and quota figures change by themselves, so polling
  // replaces just those parts and never the forms being edited.
  const [live, setLive] = useState<SettingsView | null>(null);
  useVisiblePolling(
    useCallback(
      async (signal: AbortSignal) => {
        const s = await getSettings(signal);
        setLive(s);
        setSettings((prev) => prev ?? s);
      },
      [],
    ),
    5000,
  );

  if (!settings) {
    return loadError ? (
      <p role="alert" className="notice notice--error">
        {loadError}
      </p>
    ) : (
      <p className="loading" role="status">
        Loading…
      </p>
    );
  }
  const current = live ?? settings;

  return (
    <section aria-label="Settings options">
      <div className="cards">
        <AppearanceCard />
        <MonitoringCard settings={settings} live={current} onSaved={setSettings} refresh={refresh} />
        <GeoCard settings={current} onSaved={setSettings} />
        <TelegramCard settings={current} onSaved={setSettings} />
        <SignOutCard />
        <AccountCard />
        <Card title="About">
          <p className="muted">Country flags come from the flag-icons package (MIT license) and are bundled with this application.</p>
        </Card>
      </div>
    </section>
  );
}

function AppearanceCard() {
  const [theme, setTheme] = useTheme();
  return (
    <Card title="Appearance">
      <label className="theme">
        <span>Theme</span>
        <select value={theme} onChange={(e) => setTheme(e.target.value as ThemeChoice)} aria-label="Theme">
          <option value="system">System</option>
          <option value="light">Light</option>
          <option value="dark">Dark</option>
        </select>
      </label>
    </Card>
  );
}

function SignOutCard() {
  const { signOut } = useAuth();
  return (
    <Card title="Account">
      <p className="muted">Sign out of this administrator session.</p>
      <button type="button" className="button button--small" onClick={() => void signOut()}>
        Sign out
      </button>
    </Card>
  );
}

function MonitoringCard({ settings, live, onSaved, refresh }: { settings: SettingsView; live: SettingsView; onSaved: (s: SettingsView) => void; refresh: () => Promise<void> }) {
  const m = settings.monitoring;
  const id = useId();
  const [availOn, setAvailOn] = useState(m.availability.enabled);
  const [availMin, setAvailMin] = useState(m.availability.interval_minutes);
  const [speedOn, setSpeedOn] = useState(m.speed.enabled);
  const [speedMin, setSpeedMin] = useState(m.speed.interval_minutes);
  const [conc, setConc] = useState(String(m.availability_concurrency));
  const [timeout, setTimeoutS] = useState(String(m.availability_timeout_seconds));
  const [sample, setSample] = useState(String(m.speed_sample_mib));
  const [dirTimeout, setDirTimeout] = useState(String(m.speed_direction_timeout_seconds));
  const [threshold, setThreshold] = useState(String(m.down_threshold));
  const [retention, setRetention] = useState(String(m.retention_days));
  const [msg, setMsg] = useState<Message>(null);
  const [resumeMsg, setResumeMsg] = useState<Message>(null);
  const [busy, setBusy] = useState(false);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    const nums = [availMin, speedMin, Number(conc), Number(timeout), Number(sample), Number(dirTimeout), Number(threshold), Number(retention)];
    if (nums.some((n) => !Number.isInteger(n))) {
      setMsg({ kind: 'error', text: 'Enter whole numbers in every field.' });
      return;
    }
    const body: SettingsUpdate = {
      monitoring: {
        availability: { enabled: availOn, interval_minutes: availMin },
        speed: { enabled: speedOn, interval_minutes: speedMin },
        availability_concurrency: Number(conc),
        availability_timeout_seconds: Number(timeout),
        speed_sample_mib: Number(sample),
        speed_direction_timeout_seconds: Number(dirTimeout),
        down_threshold: Number(threshold),
        retention_days: Number(retention),
      },
    };
    setBusy(true);
    setMsg(null);
    try {
      onSaved(await putSettings(body));
      setMsg({ kind: 'ok', text: 'Monitoring settings saved.' });
    } catch (err) {
      setMsg({ kind: 'error', text: errorText(err) });
    } finally {
      setBusy(false);
    }
  };

  const sp = live.speed_provider;
  return (
    <Card title="Monitoring">
      {sp.paused ? (
        <div className="notice notice--warn" role="status">
          <p>
            Speed tests are paused.{' '}
            {sp.reason === 'forbidden'
              ? 'The speed test service refused the requests, so nothing is sent until you resume.'
              : `The speed test service asked to slow down${sp.until ? `; tests continue after ${formatDateTime(sp.until)}` : ''}.`}
          </p>
          <button
            type="button"
            className="button button--small"
            onClick={() => {
              setResumeMsg(null);
              resumeSpeed().then(
                () => { setResumeMsg({ kind: 'ok', text: 'Speed tests resumed.' }); void refresh(); },
                (err: unknown) => setResumeMsg({ kind: 'error', text: errorText(err) }),
              );
            }}
          >
            Resume speed tests
          </button>
        </div>
      ) : null}
      <Msg message={resumeMsg} />
      <form onSubmit={(e) => void save(e)} noValidate className="form">
        <fieldset className="fieldset">
          <legend>Availability checks</legend>
          <label className="check">
            <input type="checkbox" checked={availOn} onChange={(e) => setAvailOn(e.target.checked)} />
            Check availability on a schedule
          </label>
          <IntervalInput label="Check every" minutes={availMin} onChange={setAvailMin} />
        </fieldset>
        <fieldset className="fieldset">
          <legend>Speed tests</legend>
          <label className="check">
            <input type="checkbox" checked={speedOn} onChange={(e) => setSpeedOn(e.target.checked)} />
            Test speed on a schedule
          </label>
          <IntervalInput label="Test every" minutes={speedMin} onChange={setSpeedMin} />
          <p className="hint">Each test downloads and uploads one sample through the proxy, so it uses data. Keep the interval long.</p>
        </fieldset>
        <div className="field-grid">
          <Num id={`${id}-c`} label="Parallel availability checks" hint="1 to 20" value={conc} onChange={setConc} />
          <Num id={`${id}-t`} label="Availability timeout, seconds" hint="1 to 60" value={timeout} onChange={setTimeoutS} />
          <Num id={`${id}-s`} label="Speed sample size, MiB" hint="1 to 64" value={sample} onChange={setSample} />
          <Num id={`${id}-d`} label="Speed timeout per direction, seconds" hint="1 to 120" value={dirTimeout} onChange={setDirTimeout} />
          <Num id={`${id}-th`} label="Failures before a proxy is reported down" hint="1 to 10" value={threshold} onChange={setThreshold} />
          <Num id={`${id}-r`} label="Keep history for, days" hint="1 to 365" value={retention} onChange={setRetention} />
        </div>
        <p className="hint">Changing the number of parallel checks applies after the server restarts.</p>
        <Msg message={msg} />
        <div className="form__actions">
          <button type="submit" className="button button--primary" disabled={busy}>
            Save monitoring settings
          </button>
        </div>
      </form>
    </Card>
  );
}

function Num({ id, label, hint, value, onChange }: { id: string; label: string; hint: string; value: string; onChange: (v: string) => void }) {
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      <input id={id} value={value} inputMode="numeric" onChange={(e) => onChange(e.target.value)} aria-describedby={`${id}-hint`} />
      <p id={`${id}-hint`} className="hint">
        {hint}
      </p>
    </div>
  );
}

function GeoCard({ settings, onSaved }: { settings: SettingsView; onSaved: (s: SettingsView) => void }) {
  const id = useId();
  const g = settings.geo;
  const [source, setSource] = useState(g.source);
  const [msg, setMsg] = useState<Message>(null);
  const [busy, setBusy] = useState(false);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setMsg(null);
    try {
      onSaved(await putSettings({ geo: { source } }));
      setMsg({ kind: 'ok', text: 'Location source saved.' });
    } catch (err) {
      setMsg({ kind: 'error', text: errorText(err) });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title="Location lookup">
      <p>
        Country, city and network of each exit address come from a public service. ProxyWard asks the service through the proxy
        itself, so the service sees the proxy&apos;s address and never yours. No account or key is needed.
      </p>
      <form className="form" noValidate onSubmit={(e) => void save(e)}>
        <div className="field">
          <label htmlFor={`${id}-source`}>Source</label>
          <select id={`${id}-source`} value={source} onChange={(e) => setSource(e.target.value)}>
            <option value="auto">Automatic: try the sources in turn</option>
            {g.sources.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
            <option value="off">Off: do not look up locations</option>
          </select>
          <p className="hint">
            Automatic uses the next source when one fails. A single source is the only one asked. Results are kept for 24 hours.
          </p>
        </div>
        <p className="muted">
          Sources:{' '}
          {g.sources.map((s, i) => (
            <span key={s.id}>
              {i > 0 ? ', ' : ''}
              <a href={s.site} target="_blank" rel="noopener noreferrer">
                {s.name}
              </a>
            </span>
          ))}
          .
        </p>
        <Msg message={msg} />
        <div className="form__actions">
          <button type="submit" className="button button--primary" disabled={busy}>
            Save location source
          </button>
        </div>
      </form>
    </Card>
  );
}

function TelegramCard({ settings, onSaved }: { settings: SettingsView; onSaved: (s: SettingsView) => void }) {
  const id = useId();
  const t = settings.telegram;
  const [token, setToken] = useState('');
  const [chat, setChat] = useState(t.chat_id);
  const [msg, setMsg] = useState<Message>(null);
  const [testMsg, setTestMsg] = useState<Message>(null);
  const [busy, setBusy] = useState(false);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    const update: NonNullable<SettingsUpdate['telegram']> = { chat_id: chat.trim() };
    if (token.trim() !== '') update.bot_token = token.trim();
    setBusy(true);
    setMsg(null);
    try {
      onSaved(await putSettings({ telegram: update }));
      setToken('');
      setMsg({ kind: 'ok', text: 'Telegram settings saved.' });
    } catch (err) {
      setMsg({ kind: 'error', text: errorText(err) });
    } finally {
      setBusy(false);
    }
  };

  const removeToken = async () => {
    setBusy(true);
    setMsg(null);
    try {
      onSaved(await putSettings({ telegram: { bot_token: '' } }));
      setMsg({ kind: 'ok', text: 'Bot token removed.' });
    } catch (err) {
      setMsg({ kind: 'error', text: errorText(err) });
    } finally {
      setBusy(false);
    }
  };

  const sendTest = async () => {
    setTestMsg(null);
    try {
      await testTelegram();
      setTestMsg({ kind: 'ok', text: 'Test message queued. Its delivery is shown below once it is sent.' });
    } catch (err) {
      setTestMsg({ kind: 'error', text: errorText(err) });
    }
  };

  const d = t.delivery;
  return (
    <Card title="Telegram notifications">
      <form onSubmit={(e) => void save(e)} noValidate className="form">
        <div className="field">
          <label htmlFor={`${id}-token`}>Bot token</label>
          <input id={`${id}-token`} type="password" value={token} onChange={(e) => setToken(e.target.value)} autoComplete="off" placeholder={t.has_token ? 'A token is saved. Paste a new one to replace it.' : 'Paste the token from @BotFather'} />
        </div>
        <div className="field">
          <label htmlFor={`${id}-chat`}>Chat ID</label>
          <input id={`${id}-chat`} value={chat} onChange={(e) => setChat(e.target.value)} autoComplete="off" spellCheck={false} placeholder="For example -1001234567890 or @channel" />
        </div>
        <Msg message={msg} />
        <div className="form__actions">
          <button type="submit" className="button button--primary" disabled={busy}>
            Save Telegram settings
          </button>
          {t.has_token ? (
            <button type="button" className="button button--danger" disabled={busy} onClick={() => void removeToken()}>
              Remove bot token
            </button>
          ) : null}
        </div>
      </form>
      <div className="form">
        <button type="button" className="button" disabled={!t.configured} onClick={() => void sendTest()}>
          Send test message
        </button>
        {!t.configured ? <p className="hint">Save a bot token and a chat ID to send a test message.</p> : null}
        <Msg message={testMsg} />
        <p className="muted" aria-live="polite">
          {d.pending} waiting, {d.failed} not sent.
          {d.last_delivered_at ? ` Last message sent ${formatDateTime(d.last_delivered_at)}.` : ' No message has been sent yet.'}
          {d.last_error ? ` Last problem: ${d.last_error.replace(/_/g, ' ')}.` : ''}
        </p>
      </div>
    </Card>
  );
}

function AccountCard() {
  const id = useId();
  const { expire } = useAuth();
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [again, setAgain] = useState('');
  const [msg, setMsg] = useState<Message>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (next.length < 12) {
      setMsg({ kind: 'error', text: 'The new password needs at least 12 characters.' });
      return;
    }
    if (next !== again) {
      setMsg({ kind: 'error', text: 'The new passwords do not match.' });
      return;
    }
    setBusy(true);
    setMsg(null);
    try {
      await changePassword(current, next);
      expire(); // every session, including this one, was signed out
    } catch (err) {
      setMsg({ kind: 'error', text: err instanceof ApiError && err.code === 'invalid_credentials' ? 'The current password is not correct.' : errorText(err) });
      setBusy(false);
    }
  };

  return (
    <Card title="Administrator password">
      <form onSubmit={(e) => void submit(e)} noValidate className="form">
        <div className="field">
          <label htmlFor={`${id}-cur`}>Current password</label>
          <input id={`${id}-cur`} type="password" value={current} onChange={(e) => setCurrent(e.target.value)} autoComplete="current-password" />
        </div>
        <div className="field">
          <label htmlFor={`${id}-new`}>New password</label>
          <input id={`${id}-new`} type="password" value={next} onChange={(e) => setNext(e.target.value)} autoComplete="new-password" />
          <p className="hint">At least 12 characters. Changing it signs out every session.</p>
        </div>
        <div className="field">
          <label htmlFor={`${id}-again`}>Repeat the new password</label>
          <input id={`${id}-again`} type="password" value={again} onChange={(e) => setAgain(e.target.value)} autoComplete="new-password" />
        </div>
        <Msg message={msg} />
        <div className="form__actions">
          <button type="submit" className="button button--primary" disabled={busy}>
            Change password
          </button>
        </div>
      </form>
    </Card>
  );
}
