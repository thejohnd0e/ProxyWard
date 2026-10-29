import { render, screen, waitFor, within } from '@testing-library/react';
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
    expect(await screen.findByRole('heading', { name: 'ProxyWard' })).toBeInTheDocument();
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

  it('shows the application version beside the brand', async () => {
    e.session.mockResolvedValue({ authenticated: true, csrf_token: 'csrf-version' });
    mount();
    const brand = await screen.findByText(/ProxyWard/, { selector: '.brand' });
    expect(brand).toHaveTextContent('ProxyWard v0.1.1');
  });

  it('opens settings in a dialog without leaving the current page', async () => {
    e.session.mockResolvedValue({ authenticated: true, csrf_token: 'csrf-settings' });
    mount();
    const user = userEvent.setup();
    await screen.findByText('Visible after login');
    await user.click(screen.getByRole('button', { name: 'Settings' }));
    expect(await screen.findByRole('dialog', { name: 'Settings' })).toBeInTheDocument();
    expect(screen.getByText('Visible after login')).toBeInTheDocument();
    expect(await screen.findByRole('heading', { name: 'Monitoring' })).toBeInTheDocument();
  });

  it('closes the settings dialog with Escape and returns focus to the settings button', async () => {
    e.session.mockResolvedValue({ authenticated: true, csrf_token: 'csrf-escape' });
    mount();
    const user = userEvent.setup();
    await screen.findByText('Visible after login');
    const settingsButton = screen.getByRole('button', { name: 'Settings' });
    await user.click(settingsButton);
    await screen.findByRole('dialog', { name: 'Settings' });
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog', { name: 'Settings' })).not.toBeInTheDocument();
    expect(settingsButton).toHaveFocus();
  });

  it('closes the settings dialog from its close button and backdrop', async () => {
    e.session.mockResolvedValue({ authenticated: true, csrf_token: 'csrf-close' });
    mount();
    const user = userEvent.setup();
    await screen.findByText('Visible after login');
    await user.click(screen.getByRole('button', { name: 'Settings' }));
    await user.click(await screen.findByRole('button', { name: 'Close dialog' }));
    expect(screen.queryByRole('dialog', { name: 'Settings' })).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Settings' }));
    const dialog = await screen.findByRole('dialog', { name: 'Settings' });
    await user.click(dialog.parentElement!);
    expect(screen.queryByRole('dialog', { name: 'Settings' })).not.toBeInTheDocument();
  });

  it('redirects the legacy settings route to the proxies page', async () => {
    e.session.mockResolvedValue({ authenticated: true, csrf_token: 'csrf-legacy-settings' });
    mount('/settings');
    expect(await screen.findByText('Visible after login')).toBeInTheDocument();
    expect(screen.queryByRole('dialog', { name: 'Settings' })).not.toBeInTheDocument();
    expect(e.getSettings).not.toHaveBeenCalled();
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
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('create it'));
  });

  it('offers first-run setup when no administrator exists and signs in after creating the password', async () => {
    e.session.mockResolvedValue({ authenticated: false, needs_setup: true });
    e.setupAdmin.mockResolvedValue({ csrf_token: 'csrf-setup' });
    mount();
    const user = userEvent.setup();
    expect(await screen.findByRole('heading', { name: 'Welcome to ProxyWard' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Sign in' })).not.toBeInTheDocument();

    await user.type(screen.getByLabelText('New password'), 'short');
    await user.type(screen.getByLabelText('Repeat the password'), 'short');
    await user.click(screen.getByRole('button', { name: 'Create password' }));
    expect(screen.getByRole('alert')).toHaveTextContent('at least 12 characters');

    await user.clear(screen.getByLabelText('New password'));
    await user.clear(screen.getByLabelText('Repeat the password'));
    await user.type(screen.getByLabelText('New password'), 'a long enough password');
    await user.type(screen.getByLabelText('Repeat the password'), 'a different password');
    await user.click(screen.getByRole('button', { name: 'Create password' }));
    expect(screen.getByRole('alert')).toHaveTextContent('do not match');
    expect(e.setupAdmin).not.toHaveBeenCalled();

    await user.clear(screen.getByLabelText('Repeat the password'));
    await user.type(screen.getByLabelText('Repeat the password'), 'a long enough password');
    await user.click(screen.getByRole('button', { name: 'Create password' }));
    expect(await screen.findByText('Visible after login')).toBeInTheDocument();
    expect(e.setupAdmin).toHaveBeenCalledWith('a long enough password');
    expect(getCsrfToken()).toBe('csrf-setup');
  });

  it('tells the user to sign in when someone else finished the setup first', async () => {
    e.session.mockResolvedValue({ authenticated: false, needs_setup: true });
    e.setupAdmin.mockRejectedValue(new ApiError(409, 'already_initialized', 'x'));
    mount();
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText('New password'), 'a long enough password');
    await user.type(screen.getByLabelText('Repeat the password'), 'a long enough password');
    await user.click(screen.getByRole('button', { name: 'Create password' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Reload the page and sign in');
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
    await user.click(screen.getByRole('button', { name: 'Settings' }));
    await user.click(await screen.findByRole('button', { name: 'Sign out' }));
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
    const topbar = within(screen.getByRole('banner'));
    expect(topbar.queryByLabelText('Theme')).not.toBeInTheDocument();
    expect(topbar.queryByRole('button', { name: 'Sign out' })).not.toBeInTheDocument();
    await user.click(topbar.getByRole('button', { name: 'Settings' }));
    const dialog = await screen.findByRole('dialog', { name: 'Settings' });
    expect(within(dialog).getByRole('heading', { name: 'Appearance' })).toBeInTheDocument();
    await user.selectOptions(within(dialog).getByLabelText('Theme'), 'dark');
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark');
    expect(window.localStorage.getItem('proxyward.theme')).toBe('dark');
    await user.selectOptions(within(dialog).getByLabelText('Theme'), 'system');
    expect(document.documentElement.hasAttribute('data-theme')).toBe(false);
  });

  it('signs out from the Settings dialog and returns to the login form', async () => {
    e.session.mockResolvedValue({ authenticated: true, csrf_token: 'csrf-signout' });
    e.logout.mockResolvedValue(undefined);
    mount();
    const user = userEvent.setup();
    await screen.findByText('Visible after login');
    const topbar = within(screen.getByRole('banner'));
    expect(topbar.queryByRole('button', { name: 'Sign out' })).not.toBeInTheDocument();
    await user.click(topbar.getByRole('button', { name: 'Settings' }));
    const dialog = await screen.findByRole('dialog', { name: 'Settings' });
    expect(within(dialog).getByRole('heading', { name: 'Account' })).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Sign out' }));
    expect(await screen.findByLabelText('Password')).toBeInTheDocument();
    expect(e.logout).toHaveBeenCalledOnce();
    expect(screen.queryByRole('dialog', { name: 'Settings' })).not.toBeInTheDocument();
  });
});
