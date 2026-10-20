import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, getCsrfToken, setCsrfToken } from '../api/client';
import * as endpoints from '../api/endpoints';
import { makeProxy, makeSettings } from '../test/fixtures';
import { App } from './App';
import { AuthProvider } from './auth';

vi.mock('../api/endpoints');

const e = vi.mocked(endpoints);

function mount(path = '/proxies') {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <AuthProvider>
        <App />
      </AuthProvider>
    </MemoryRouter>,
  );
}

describe('authentication', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    setCsrfToken(null);
    e.listProxies.mockResolvedValue({ proxies: [makeProxy({ name: 'Visible after login' })], limit: 100, geo_configured: false });
    e.getSettings.mockResolvedValue(makeSettings());
    e.listEvents.mockResolvedValue({ events: [], next_cursor: null });
  });

  it('requires a login: protected pages show the sign-in form when there is no session', async () => {
    e.session.mockResolvedValue({ authenticated: false });
    mount('/settings');
    expect(await screen.findByRole('heading', { name: 'Proxy Checker' })).toBeInTheDocument();
    expect(screen.getByLabelText('Password')).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Settings' })).not.toBeInTheDocument();
    expect(e.getSettings).not.toHaveBeenCalled();
    expect(e.listProxies).not.toHaveBeenCalled();
  });

  it('shows the app straight away when the session is valid and keeps the CSRF token for changes', async () => {
    e.session.mockResolvedValue({ authenticated: true, csrf_token: 'csrf-1' });
    mount();
    expect(await screen.findByText('Visible after login')).toBeInTheDocument();
    expect(getCsrfToken()).toBe('csrf-1');
    expect(screen.getByRole('navigation', { name: 'Main' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Events' })).toBeInTheDocument();
  });

  it('signs in with the password and then loads the proxies', async () => {
    e.session.mockResolvedValue({ authenticated: false });
    e.login.mockResolvedValue({ csrf_token: 'csrf-2' });
    mount();
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText('Password'), 'correct horse');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByText('Visible after login')).toBeInTheDocument();
    expect(e.login).toHaveBeenCalledWith('correct horse');
    expect(getCsrfToken()).toBe('csrf-2');
  });

  it('explains a wrong password, throttling and a missing administrator in plain words', async () => {
    e.session.mockResolvedValue({ authenticated: false });
    mount();
    const user = userEvent.setup();
    const submit = async (text: string) => {
      await user.clear(screen.getByLabelText('Password'));
      await user.type(screen.getByLabelText('Password'), text);
      await user.click(screen.getByRole('button', { name: 'Sign in' }));
    };
    await screen.findByLabelText('Password');

    e.login.mockRejectedValueOnce(new ApiError(401, 'invalid_credentials', 'Invalid password.'));
    await submit('wrong');
    expect(await screen.findByRole('alert')).toHaveTextContent('The password is not correct.');
    expect(screen.getByLabelText('Password')).toHaveValue('');

    e.login.mockRejectedValueOnce(new ApiError(429, 'rate_limited', 'Too many login attempts. Try again later.'));
    await submit('again');
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Too many attempts'));

    e.login.mockRejectedValueOnce(new ApiError(503, 'not_initialized', 'x'));
    await submit('again');
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('proxy-checker admin set-password'));
  });

  it('asks for a password instead of sending an empty one', async () => {
    e.session.mockResolvedValue({ authenticated: false });
    mount();
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Sign in' }));
    expect(e.login).not.toHaveBeenCalled();
    expect(screen.getByRole('alert')).toHaveTextContent('Enter the administrator password.');
  });

  it('returns to the sign-in form when the server ends the session', async () => {
    e.session.mockResolvedValue({ authenticated: true, csrf_token: 'csrf-3' });
    e.logout.mockResolvedValue(undefined);
    mount();
    const user = userEvent.setup();
    await screen.findByText('Visible after login');
    await user.click(screen.getByRole('button', { name: 'Sign out' }));
    expect(await screen.findByLabelText('Password')).toBeInTheDocument();
    expect(e.logout).toHaveBeenCalled();
    expect(getCsrfToken()).toBeNull();
  });

  it('treats an unreachable session check as signed out rather than hanging', async () => {
    e.session.mockRejectedValue(new ApiError(0, 'network', 'Cannot reach the server.'));
    mount();
    expect(await screen.findByLabelText('Password')).toBeInTheDocument();
  });

  it('lets the user choose the theme and remembers it', async () => {
    e.session.mockResolvedValue({ authenticated: true, csrf_token: 'c' });
    mount();
    const user = userEvent.setup();
    await screen.findByText('Visible after login');
    await user.selectOptions(screen.getByLabelText('Theme'), 'dark');
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark');
    expect(window.localStorage.getItem('proxy-checker.theme')).toBe('dark');
    await user.selectOptions(screen.getByLabelText('Theme'), 'system');
    expect(document.documentElement.hasAttribute('data-theme')).toBe(false);
  });
});
