import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, setCsrfToken } from '../../api/client';
import * as endpoints from '../../api/endpoints';
import type { SettingsView } from '../../api/types';
import { AuthProvider } from '../../app/auth';
import { makeSettings } from '../../test/fixtures';
import { SettingsPage } from './SettingsPage';

vi.mock('../../api/endpoints');
const e = vi.mocked(endpoints);

function mount() {
  render(
    <MemoryRouter>
      <AuthProvider>
        <SettingsPage />
      </AuthProvider>
    </MemoryRouter>,
  );
  return userEvent.setup();
}

const card = (name: string) => screen.getByRole('region', { name });

describe('SettingsPage', () => {
  let current: SettingsView;

  beforeEach(() => {
    vi.resetAllMocks();
    setCsrfToken('t');
    current = makeSettings();
    e.session.mockResolvedValue({ authenticated: true, csrf_token: 't' });
    e.getSettings.mockImplementation(async () => current);
    e.putSettings.mockImplementation(async () => current);
  });

  it('shows the current monitoring settings with hours where the interval is whole hours', async () => {
    current = makeSettings({ monitoring: { ...makeSettings().monitoring, speed: { enabled: true, interval_minutes: 360 } } });
    mount();
    const c = within(await screen.findByRole('region', { name: 'Monitoring' }));
    expect(c.getByLabelText('Test every')).toHaveValue('6');
    expect(c.getAllByLabelText('Unit')[1]).toHaveValue('hours');
    expect(c.getByLabelText('Check every')).toHaveValue('5');
    expect(c.getByLabelText('Test speed on a schedule')).toBeChecked();
    expect(c.getByLabelText('Check availability on a schedule')).not.toBeChecked();
  });

  it('saves monitoring settings, converting hours to minutes', async () => {
    const user = mount();
    const c = within(await screen.findByRole('region', { name: 'Monitoring' }));
    await user.click(c.getByLabelText('Check availability on a schedule'));
    await user.clear(c.getByLabelText('Check every'));
    await user.type(c.getByLabelText('Check every'), '2');
    await user.selectOptions(c.getAllByLabelText('Unit')[0]!, 'hours');
    await user.clear(c.getByLabelText('Failures before a proxy is reported down'));
    await user.type(c.getByLabelText('Failures before a proxy is reported down'), '4');
    await user.click(c.getByRole('button', { name: 'Save monitoring settings' }));
    await waitFor(() => expect(e.putSettings).toHaveBeenCalledTimes(1));
    const body = e.putSettings.mock.calls[0]![0];
    expect(body.monitoring?.availability).toEqual({ enabled: true, interval_minutes: 120 });
    expect(body.monitoring?.speed).toEqual({ enabled: false, interval_minutes: 360 });
    expect(body.monitoring?.down_threshold).toBe(4);
    expect(body).not.toHaveProperty('telegram');
    expect(body).not.toHaveProperty('geo');
    expect(await screen.findByText('Monitoring settings saved.')).toBeInTheDocument();
  });

  it('rejects non-numeric values before calling the server and shows server validation text', async () => {
    const user = mount();
    const c = within(await screen.findByRole('region', { name: 'Monitoring' }));
    await user.clear(c.getByLabelText('Parallel availability checks'));
    await user.type(c.getByLabelText('Parallel availability checks'), 'many');
    await user.click(c.getByRole('button', { name: 'Save monitoring settings' }));
    expect(e.putSettings).not.toHaveBeenCalled();
    expect(await c.findByRole('alert')).toHaveTextContent('Enter whole numbers in every field.');

    await user.clear(c.getByLabelText('Parallel availability checks'));
    await user.type(c.getByLabelText('Parallel availability checks'), '99');
    e.putSettings.mockRejectedValueOnce(new ApiError(422, 'validation_failed', 'Some fields are not valid.', { availability_concurrency: 'Must be between 1 and 20.' }));
    await user.click(c.getByRole('button', { name: 'Save monitoring settings' }));
    expect(await c.findByRole('alert')).toHaveTextContent('Must be between 1 and 20.');
  });

  it('never shows or prefills secrets and only sends what was typed', async () => {
    current = makeSettings({
      geo: { ...makeSettings().geo, has_key: true },
      telegram: { ...makeSettings().telegram, has_token: true, chat_id: '-1001', configured: true },
    });
    const user = mount();
    const tg = within(await screen.findByRole('region', { name: 'Telegram notifications' }));
    expect(tg.getByLabelText('Bot token')).toHaveValue('');
    expect(tg.getByLabelText('Bot token')).toHaveAttribute('placeholder', expect.stringMatching(/token is saved/i));
    expect(tg.getByLabelText('Chat ID')).toHaveValue('-1001');
    expect(within(card('Location lookup')).getByLabelText('API key')).toHaveValue('');

    // Saving only the chat ID leaves the token alone.
    await user.clear(tg.getByLabelText('Chat ID'));
    await user.type(tg.getByLabelText('Chat ID'), '@alerts_channel');
    await user.click(tg.getByRole('button', { name: 'Save Telegram settings' }));
    await waitFor(() => expect(e.putSettings).toHaveBeenCalled());
    expect(e.putSettings.mock.calls[0]![0]).toEqual({ telegram: { chat_id: '@alerts_channel' } });

    // A typed token replaces the saved one.
    await user.type(tg.getByLabelText('Bot token'), '123456789:AAH_test-TOKEN-value-1234567890abcd');
    await user.click(tg.getByRole('button', { name: 'Save Telegram settings' }));
    await waitFor(() => expect(e.putSettings).toHaveBeenCalledTimes(2));
    expect(e.putSettings.mock.calls[1]![0].telegram?.bot_token).toBe('123456789:AAH_test-TOKEN-value-1234567890abcd');
    await waitFor(() => expect(tg.getByLabelText('Bot token')).toHaveValue(''));
  });

  it('removes the token and the geolocation key with an explicit empty value', async () => {
    current = makeSettings({
      geo: { ...makeSettings().geo, has_key: true },
      telegram: { ...makeSettings().telegram, has_token: true, chat_id: '-1', configured: true },
    });
    const user = mount();
    await user.click(await screen.findByRole('button', { name: 'Remove bot token' }));
    await waitFor(() => expect(e.putSettings).toHaveBeenCalledWith({ telegram: { bot_token: '' } }));
    await user.click(screen.getByRole('button', { name: 'Remove API key' }));
    await waitFor(() => expect(e.putSettings).toHaveBeenCalledWith({ geo: { api_key: '' } }));
  });

  it('says Not configured for location lookup and shows daily usage', async () => {
    current = makeSettings({ geo: { has_key: false, used_today: 12, daily_limit: 1000, paused: false, pause_reason: '', retry_at: null } });
    mount();
    const g = within(await screen.findByRole('region', { name: 'Location lookup' }));
    expect(g.getByText(/Not configured\./)).toBeInTheDocument();
    expect(g.getByText(/Used today: 12 of 1000 lookups\./)).toBeInTheDocument();
    expect(g.queryByRole('button', { name: 'Remove API key' })).not.toBeInTheDocument();
    await userEvent.setup().click(g.getByRole('button', { name: 'Save API key' }));
    expect(e.putSettings).not.toHaveBeenCalled();
    expect(g.getByRole('alert')).toHaveTextContent('Paste an API key first.');
  });

  it('queues a test message without claiming it was delivered', async () => {
    current = makeSettings({ telegram: { ...makeSettings().telegram, has_token: true, chat_id: '-1', configured: true } });
    e.testTelegram.mockResolvedValue({ message_id: 4, status: 'queued' });
    const user = mount();
    await user.click(await screen.findByRole('button', { name: 'Send test message' }));
    const tg = within(card('Telegram notifications'));
    expect(await tg.findByRole('status')).toHaveTextContent('Test message queued.');
    expect(tg.getByRole('status')).not.toHaveTextContent(/delivered|sent successfully/i);
  });

  it('disables the test message until Telegram is configured', async () => {
    mount();
    expect(await screen.findByRole('button', { name: 'Send test message' })).toBeDisabled();
  });

  it('shows delivery problems in the open', async () => {
    current = makeSettings({
      telegram: { has_token: true, chat_id: '-1', configured: true, delivery: { pending: 2, failed: 1, last_delivered_at: null, last_error: 'rate_limited' } },
    });
    mount();
    expect(await screen.findByText(/2 waiting, 1 not sent\./)).toBeInTheDocument();
    expect(screen.getByText(/No message has been sent yet\./)).toBeInTheDocument();
    expect(screen.getByText(/Last problem: rate limited\./)).toBeInTheDocument();
  });

  it('offers to resume paused speed tests', async () => {
    current = makeSettings({ speed_provider: { name: 'cloudflare', paused: true, reason: 'forbidden', until: null } });
    e.resumeSpeed.mockImplementation(async () => {
      current = makeSettings();
    });
    const user = mount();
    expect(await screen.findByText(/Speed tests are paused\./)).toBeInTheDocument();
    expect(screen.getByText(/refused the requests/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Resume speed tests' }));
    expect(await screen.findByText('Speed tests resumed.')).toBeInTheDocument();
    expect(e.resumeSpeed).toHaveBeenCalled();
  });

  it('checks the new password locally and then signs the user out after the change', async () => {
    e.changePassword.mockResolvedValue(undefined);
    const user = mount();
    const a = within(await screen.findByRole('region', { name: 'Administrator password' }));
    await user.type(a.getByLabelText('Current password'), 'old password value');
    await user.type(a.getByLabelText('New password'), 'short');
    await user.type(a.getByLabelText('Repeat the new password'), 'short');
    await user.click(a.getByRole('button', { name: 'Change password' }));
    expect(a.getByRole('alert')).toHaveTextContent('at least 12 characters');
    await user.clear(a.getByLabelText('New password'));
    await user.type(a.getByLabelText('New password'), 'a long enough password');
    await user.click(a.getByRole('button', { name: 'Change password' }));
    expect(a.getByRole('alert')).toHaveTextContent('do not match');
    expect(e.changePassword).not.toHaveBeenCalled();
    await user.clear(a.getByLabelText('Repeat the new password'));
    await user.type(a.getByLabelText('Repeat the new password'), 'a long enough password');
    await user.click(a.getByRole('button', { name: 'Change password' }));
    await waitFor(() => expect(e.changePassword).toHaveBeenCalledWith('old password value', 'a long enough password'));
  });

  it('explains a wrong current password', async () => {
    e.changePassword.mockRejectedValue(new ApiError(401, 'invalid_credentials', 'Current password is incorrect.'));
    const user = mount();
    const a = within(await screen.findByRole('region', { name: 'Administrator password' }));
    await user.type(a.getByLabelText('Current password'), 'nope');
    await user.type(a.getByLabelText('New password'), 'a long enough password');
    await user.type(a.getByLabelText('Repeat the new password'), 'a long enough password');
    await user.click(a.getByRole('button', { name: 'Change password' }));
    expect(await a.findByRole('alert')).toHaveTextContent('The current password is not correct.');
  });

  it('credits the bundled flag icons', async () => {
    mount();
    expect(await screen.findByText(/flag-icons package \(MIT license\)/)).toBeInTheDocument();
  });
});

describe('SettingsPage location key help', () => {
  it('tells where to get the ipapi.is key and links to the site and the documentation', async () => {
    e.getSettings.mockResolvedValue(makeSettings());
    mount();
    const region = await screen.findByRole('region', { name: 'Location lookup' });
    expect(within(region).getByText(/create a free account/i)).toBeInTheDocument();
    expect(within(region).getByRole('link', { name: 'ipapi.is' })).toHaveAttribute('href', 'https://ipapi.is/');
    expect(within(region).getByRole('link', { name: 'documentation' })).toHaveAttribute('href', 'https://ipapi.is/developers.html');
  });
});
