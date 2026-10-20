import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '../../api/client';
import * as endpoints from '../../api/endpoints';
import { makeProxy } from '../../test/fixtures';
import { ProxyDialog } from './ProxyDialog';

vi.mock('../../api/endpoints');

const create = vi.mocked(endpoints.createProxy);
const update = vi.mocked(endpoints.updateProxy);

function open(proxy?: ReturnType<typeof makeProxy>) {
  const onClose = vi.fn();
  const onSaved = vi.fn();
  render(<ProxyDialog proxy={proxy} onClose={onClose} onSaved={onSaved} />);
  return { onClose, onSaved, user: userEvent.setup() };
}

describe('ProxyDialog', () => {
  beforeEach(() => {
    create.mockReset();
    update.mockReset();
    create.mockResolvedValue(makeProxy());
    update.mockResolvedValue(makeProxy());
  });

  it('creates a proxy from the form', async () => {
    const { user, onSaved } = open();
    await user.type(screen.getByLabelText('Name'), 'Edge');
    await user.selectOptions(screen.getByLabelText('Protocol'), 'socks5');
    await user.type(screen.getByLabelText('Host'), 'Edge.Example.com');
    await user.type(screen.getByLabelText('Port'), '1080');
    await user.type(screen.getByLabelText('Tags'), ' eu , lab ,');
    await user.click(screen.getByRole('button', { name: 'Add proxy' }));
    await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
    expect(create.mock.calls[0]![0]).toMatchObject({
      name: 'Edge',
      protocol: 'socks5',
      host: 'Edge.Example.com',
      port: 1080,
      tags: ['eu', 'lab'],
      auth_enabled: false,
      availability_schedule: null,
      speed_schedule: null,
    });
    expect(create.mock.calls[0]![0]).not.toHaveProperty('username');
    expect(create.mock.calls[0]![0]).not.toHaveProperty('password');
    expect(onSaved).toHaveBeenCalled();
  });

  it('asks for the required fields before calling the server', async () => {
    const { user } = open();
    await user.click(screen.getByRole('button', { name: 'Add proxy' }));
    expect(create).not.toHaveBeenCalled();
    expect(screen.getByText('Enter the proxy host.')).toBeInTheDocument();
    expect(screen.getByText('Enter a port from 1 to 65535.')).toBeInTheDocument();
    await user.type(screen.getByLabelText('Host'), 'h.lan');
    await user.type(screen.getByLabelText('Port'), '70000');
    await user.click(screen.getByRole('button', { name: 'Add proxy' }));
    expect(create).not.toHaveBeenCalled();
    expect(screen.getByText('Enter a port from 1 to 65535.')).toBeInTheDocument();
  });

  it('requires a username when authentication is on and allows an empty password', async () => {
    const { user } = open();
    await user.type(screen.getByLabelText('Host'), 'h.lan');
    await user.type(screen.getByLabelText('Port'), '8080');
    await user.click(screen.getByLabelText('Use authentication'));
    await user.click(screen.getByRole('button', { name: 'Add proxy' }));
    expect(create).not.toHaveBeenCalled();
    expect(screen.getByText('Enter the username.')).toBeInTheDocument();
    await user.type(screen.getByLabelText('Username'), 'alice');
    await user.click(screen.getByRole('button', { name: 'Add proxy' }));
    await waitFor(() => expect(create).toHaveBeenCalled());
    expect(create.mock.calls[0]![0]).toMatchObject({ auth_enabled: true, username: 'alice', password: '' });
  });

  it('keeps the authentication choice and entered credentials when the protocol changes', async () => {
    const { user } = open();
    await user.click(screen.getByLabelText('Use authentication'));
    await user.type(screen.getByLabelText('Username'), 'alice');
    await user.type(screen.getByLabelText('Password'), 'secret-pass');
    await user.selectOptions(screen.getByLabelText('Protocol'), 'socks5');
    await user.selectOptions(screen.getByLabelText('Protocol'), 'https');
    expect(screen.getByLabelText('Use authentication')).toBeChecked();
    expect(screen.getByLabelText('Username')).toHaveValue('alice');
    expect(screen.getByLabelText('Password')).toHaveValue('secret-pass');
  });

  describe('editing', () => {
    const stored = makeProxy({ id: 7, name: 'Stored', host: 'stored.lan', port: 3128, auth_enabled: true, username: 'bob', has_password: true });

    it('never prefills the stored password and says it is kept', () => {
      open(stored);
      expect(screen.getByLabelText('Password')).toHaveValue('');
      expect(screen.getByText(/password is stored/i)).toBeInTheDocument();
      expect(screen.getByLabelText('Username')).toHaveValue('bob');
    });

    it('keeps the stored password by not sending one', async () => {
      const { user, onSaved } = open(stored);
      await user.clear(screen.getByLabelText('Name'));
      await user.type(screen.getByLabelText('Name'), 'Renamed');
      await user.click(screen.getByRole('button', { name: 'Save changes' }));
      await waitFor(() => expect(update).toHaveBeenCalledTimes(1));
      const [id, body] = update.mock.calls[0]!;
      expect(id).toBe(7);
      expect(body.name).toBe('Renamed');
      expect(body.auth_enabled).toBe(true);
      expect(body.username).toBe('bob');
      expect(body).not.toHaveProperty('password');
      expect(onSaved).toHaveBeenCalled();
    });

    it('replaces the password only when one is typed', async () => {
      const { user } = open(stored);
      await user.type(screen.getByLabelText('Password'), 'new-secret');
      await user.click(screen.getByRole('button', { name: 'Save changes' }));
      await waitFor(() => expect(update).toHaveBeenCalled());
      expect(update.mock.calls[0]![1].password).toBe('new-secret');
    });

    it('can set an empty password explicitly', async () => {
      const { user } = open(stored);
      await user.click(screen.getByLabelText('Use an empty password'));
      expect(screen.getByLabelText('Password')).toBeDisabled();
      await user.click(screen.getByRole('button', { name: 'Save changes' }));
      await waitFor(() => expect(update).toHaveBeenCalled());
      expect(update.mock.calls[0]![1].password).toBe('');
    });

    it('removes credentials when authentication is switched off', async () => {
      const { user } = open(stored);
      await user.click(screen.getByLabelText('Use authentication'));
      await user.click(screen.getByRole('button', { name: 'Save changes' }));
      await waitFor(() => expect(update).toHaveBeenCalled());
      const body = update.mock.calls[0]![1];
      expect(body.auth_enabled).toBe(false);
      expect(body).not.toHaveProperty('username');
      expect(body).not.toHaveProperty('password');
    });
  });

  it('shows server-side field errors next to the fields', async () => {
    create.mockRejectedValue(new ApiError(422, 'validation_failed', 'Some fields are not valid.', { host: 'Must be a valid hostname or IP address without scheme or port.' }));
    const { user, onSaved } = open();
    await user.type(screen.getByLabelText('Host'), 'http://bad');
    await user.type(screen.getByLabelText('Port'), '80');
    await user.click(screen.getByRole('button', { name: 'Add proxy' }));
    expect(await screen.findByText('Must be a valid hostname or IP address without scheme or port.')).toBeInTheDocument();
    expect(onSaved).not.toHaveBeenCalled();
    expect(screen.getByLabelText('Host')).toHaveAccessibleDescription(/valid hostname/);
  });

  it('explains duplicates and the proxy limit in plain words', async () => {
    create.mockRejectedValueOnce(new ApiError(409, 'duplicate', 'x'));
    const { user } = open();
    await user.type(screen.getByLabelText('Host'), 'h.lan');
    await user.type(screen.getByLabelText('Port'), '80');
    await user.click(screen.getByRole('button', { name: 'Add proxy' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('already exists');
    create.mockRejectedValueOnce(new ApiError(422, 'record_limit', 'x'));
    await user.click(screen.getByRole('button', { name: 'Add proxy' }));
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('100 proxies'));
  });

  it('offers schedule overrides that default to the global setting', async () => {
    const { user } = open();
    const availability = screen.getByLabelText('Availability checks');
    expect(availability).toHaveValue('inherit');
    await user.selectOptions(availability, 'on');
    const group = screen.getByRole('group', { name: 'Availability schedule' });
    await user.clear(within(group).getByLabelText('Every'));
    await user.type(within(group).getByLabelText('Every'), '2');
    await user.selectOptions(within(group).getByLabelText('Unit'), 'hours');
    await user.type(screen.getByLabelText('Host'), 'h.lan');
    await user.type(screen.getByLabelText('Port'), '80');
    await user.click(screen.getByRole('button', { name: 'Add proxy' }));
    await waitFor(() => expect(create).toHaveBeenCalled());
    expect(create.mock.calls[0]![0].availability_schedule).toEqual({ enabled: true, interval_minutes: 120 });
    expect(create.mock.calls[0]![0].speed_schedule).toBeNull();
  });

  it('closes with Escape and the Cancel button without saving', async () => {
    const { user, onClose } = open();
    await user.keyboard('{Escape}');
    expect(onClose).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onClose).toHaveBeenCalledTimes(2);
    expect(create).not.toHaveBeenCalled();
  });
});
