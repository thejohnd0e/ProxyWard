import { useId, useState, type FormEvent } from 'react';
import { ApiError } from '../../api/client';
import { createProxy, updateProxy } from '../../api/endpoints';
import type { Protocol, Proxy, ProxyInput, ScheduleOverride } from '../../api/types';
import { Dialog } from '../../components/Dialog';
import { splitInterval, toMinutes, type IntervalUnit } from '../../lib/format';

type Mode = 'inherit' | 'on' | 'off';

interface ScheduleState {
  mode: Mode;
  value: string;
  unit: IntervalUnit;
}

function scheduleFrom(s: ScheduleOverride | null, defaultMinutes: number): ScheduleState {
  if (!s) {
    const d = splitInterval(defaultMinutes);
    return { mode: 'inherit', value: String(d.value), unit: d.unit };
  }
  const d = splitInterval(s.interval_minutes);
  return { mode: s.enabled ? 'on' : 'off', value: String(d.value), unit: d.unit };
}

function scheduleToInput(s: ScheduleState): ScheduleOverride | null {
  if (s.mode === 'inherit') return null;
  return { enabled: s.mode === 'on', interval_minutes: toMinutes(Number(s.value), s.unit) };
}

function validInterval(s: ScheduleState): boolean {
  if (s.mode === 'inherit') return true;
  const n = Number(s.value);
  if (!Number.isInteger(n)) return false;
  const minutes = toMinutes(n, s.unit);
  return minutes >= 1 && minutes <= 10080;
}

interface Props {
  /** Present when editing. */
  proxy?: Proxy;
  onClose: () => void;
  onSaved: (saved: Proxy) => void;
}

export function ProxyDialog({ proxy, onClose, onSaved }: Props) {
  const editing = proxy !== undefined;
  const uid = useId();
  const [name, setName] = useState(proxy?.name ?? '');
  const [protocol, setProtocol] = useState<Protocol>(proxy?.protocol ?? 'http');
  const [host, setHost] = useState(proxy?.host ?? '');
  const [port, setPort] = useState(proxy ? String(proxy.port) : '');
  const [tags, setTags] = useState(proxy?.tags.join(', ') ?? '');
  const [auth, setAuth] = useState(proxy?.auth_enabled ?? false);
  const [username, setUsername] = useState(proxy?.username ?? '');
  const [password, setPassword] = useState('');
  const [emptyPassword, setEmptyPassword] = useState(false);
  const [availability, setAvailability] = useState(scheduleFrom(proxy?.availability_schedule ?? null, 5));
  const [speed, setSpeed] = useState(scheduleFrom(proxy?.speed_schedule ?? null, 360));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const validate = (): Record<string, string> => {
    const e: Record<string, string> = {};
    if (host.trim() === '') e.host = 'Enter the proxy host.';
    const p = Number(port);
    if (port.trim() === '' || !Number.isInteger(p) || p < 1 || p > 65535) e.port = 'Enter a port from 1 to 65535.';
    if (auth && username.trim() === '') e.username = 'Enter the username.';
    if (!validInterval(availability)) e.availability_schedule = 'Enter an interval from 1 minute to 7 days.';
    if (!validInterval(speed)) e.speed_schedule = 'Enter an interval from 1 minute to 7 days.';
    return e;
  };

  const buildInput = (): ProxyInput => {
    const input: ProxyInput = {
      protocol,
      host: host.trim(),
      port: Number(port),
      tags: tags.split(',').map((t) => t.trim()).filter((t) => t !== ''),
      auth_enabled: auth,
      availability_schedule: scheduleToInput(availability),
      speed_schedule: scheduleToInput(speed),
    };
    if (name.trim() !== '') input.name = name.trim();
    if (auth) {
      input.username = username.trim();
      const hadStoredPassword = proxy?.auth_enabled === true;
      if (emptyPassword) input.password = '';
      else if (password !== '' || !hadStoredPassword) input.password = password;
      // Otherwise the field is left out, which keeps the stored password.
    }
    return input;
  };

  const submit = async (ev: FormEvent) => {
    ev.preventDefault();
    const found = validate();
    setErrors(found);
    setFormError(null);
    if (Object.keys(found).length > 0) return;
    setSaving(true);
    try {
      const saved = editing ? await updateProxy(proxy.id, buildInput()) : await createProxy(buildInput());
      onSaved(saved);
    } catch (err) {
      if (err instanceof ApiError && err.status === 422 && err.code === 'validation_failed' && Object.keys(err.fields).length > 0) {
        setErrors(err.fields);
      } else if (err instanceof ApiError && err.code === 'duplicate') {
        setFormError('A proxy with the same address and credentials already exists.');
      } else if (err instanceof ApiError && err.code === 'record_limit') {
        setFormError('At most 100 proxies can be stored. Delete one before adding another.');
      } else {
        setFormError(err instanceof ApiError ? err.message : 'Something went wrong. Try again.');
      }
    } finally {
      setSaving(false);
    }
  };

  const fieldId = (key: string) => `${uid}-${key}`;
  const errorFor = (key: string) =>
    errors[key] ? (
      <p id={`${fieldId(key)}-error`} className="field__error">
        {errors[key]}
      </p>
    ) : null;
  const describedBy = (key: string) => (errors[key] ? `${fieldId(key)}-error` : undefined);

  return (
    <Dialog title={editing ? 'Edit proxy' : 'Add proxy'} onClose={onClose}>
      <form onSubmit={(e) => void submit(e)} noValidate className="form">
        {formError ? (
          <p role="alert" className="notice notice--error">
            {formError}
          </p>
        ) : null}

        <div className="field">
          <label htmlFor={fieldId('name')}>Name</label>
          <input id={fieldId('name')} value={name} onChange={(e) => setName(e.target.value)} placeholder="Defaults to host:port" autoComplete="off" aria-describedby={describedBy('name')} />
          {errorFor('name')}
        </div>

        <div className="field-row">
          <div className="field">
            <label htmlFor={fieldId('protocol')}>Protocol</label>
            <select id={fieldId('protocol')} value={protocol} onChange={(e) => setProtocol(e.target.value as Protocol)}>
              <option value="http">HTTP</option>
              <option value="https">HTTPS</option>
              <option value="socks5">SOCKS5</option>
            </select>
            {errorFor('protocol')}
          </div>
          <div className="field field--grow">
            <label htmlFor={fieldId('host')}>Host</label>
            <input id={fieldId('host')} value={host} onChange={(e) => setHost(e.target.value)} placeholder="10.0.0.5 or proxy.example.com" autoComplete="off" spellCheck={false} aria-invalid={errors.host ? true : undefined} aria-describedby={describedBy('host')} data-autofocus />
            {errorFor('host')}
          </div>
          <div className="field field--port">
            <label htmlFor={fieldId('port')}>Port</label>
            <input id={fieldId('port')} value={port} onChange={(e) => setPort(e.target.value)} inputMode="numeric" autoComplete="off" aria-invalid={errors.port ? true : undefined} aria-describedby={describedBy('port')} />
            {errorFor('port')}
          </div>
        </div>

        <div className="field">
          <label htmlFor={fieldId('tags')}>Tags</label>
          <input id={fieldId('tags')} value={tags} onChange={(e) => setTags(e.target.value)} placeholder="Separate with commas" autoComplete="off" aria-describedby={describedBy('tags')} />
          {errorFor('tags')}
        </div>

        <fieldset className="fieldset">
          <legend>Authentication</legend>
          <label className="check">
            <input type="checkbox" checked={auth} onChange={(e) => setAuth(e.target.checked)} />
            Use authentication
          </label>
          {auth ? (
            <>
              <div className="field-row">
                <div className="field field--grow">
                  <label htmlFor={fieldId('username')}>Username</label>
                  <input id={fieldId('username')} value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" spellCheck={false} aria-invalid={errors.username ? true : undefined} aria-describedby={describedBy('username')} />
                  {errorFor('username')}
                </div>
                <div className="field field--grow">
                  <label htmlFor={fieldId('password')}>Password</label>
                  <input id={fieldId('password')} type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" disabled={emptyPassword} aria-describedby={`${fieldId('password-hint')} ${describedBy('password') ?? ''}`.trim()} />
                  {errorFor('password')}
                </div>
              </div>
              <p id={fieldId('password-hint')} className="hint">
                {proxy?.auth_enabled && proxy.has_password
                  ? 'A password is stored. Leave this blank to keep it, or type a new one to replace it.'
                  : 'The password is stored encrypted and is never shown again.'}
              </p>
              <label className="check">
                <input type="checkbox" checked={emptyPassword} onChange={(e) => { setEmptyPassword(e.target.checked); if (e.target.checked) setPassword(''); }} />
                Use an empty password
              </label>
            </>
          ) : null}
        </fieldset>

        <fieldset className="fieldset">
          <legend>Schedules for this proxy</legend>
          <ScheduleField idPrefix={fieldId('avail')} label="Availability checks" groupLabel="Availability schedule" state={availability} onChange={setAvailability} error={errors.availability_schedule} />
          <ScheduleField idPrefix={fieldId('speed')} label="Speed tests" groupLabel="Speed schedule" state={speed} onChange={setSpeed} error={errors.speed_schedule} />
        </fieldset>

        <div className="dialog__actions">
          <button type="button" className="button" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" className="button button--primary" disabled={saving}>
            {editing ? 'Save changes' : 'Add proxy'}
          </button>
        </div>
      </form>
    </Dialog>
  );
}

function ScheduleField(props: {
  idPrefix: string;
  label: string;
  groupLabel: string;
  state: ScheduleState;
  onChange: (s: ScheduleState) => void;
  error?: string | undefined;
}) {
  const { idPrefix, label, groupLabel, state, onChange, error } = props;
  const errId = `${idPrefix}-error`;
  return (
    <div className="field">
      <label htmlFor={`${idPrefix}-mode`}>{label}</label>
      <select
        id={`${idPrefix}-mode`}
        value={state.mode}
        onChange={(e) => onChange({ ...state, mode: e.target.value as Mode })}
      >
        <option value="inherit">Use the global setting</option>
        <option value="on">Always on for this proxy</option>
        <option value="off">Off for this proxy</option>
      </select>
      {state.mode !== 'inherit' ? (
        <div role="group" aria-label={groupLabel} className="field-row field-row--tight">
          <div className="field field--port">
            <label htmlFor={`${idPrefix}-value`}>Every</label>
            <input id={`${idPrefix}-value`} value={state.value} onChange={(e) => onChange({ ...state, value: e.target.value })} inputMode="numeric" aria-describedby={error ? errId : undefined} />
          </div>
          <div className="field">
            <label htmlFor={`${idPrefix}-unit`}>Unit</label>
            <select id={`${idPrefix}-unit`} value={state.unit} onChange={(e) => onChange({ ...state, unit: e.target.value as IntervalUnit })}>
              <option value="minutes">minutes</option>
              <option value="hours">hours</option>
            </select>
          </div>
        </div>
      ) : null}
      {error ? (
        <p id={errId} className="field__error">
          {error}
        </p>
      ) : null}
    </div>
  );
}
