import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '../../api/client';
import * as endpoints from '../../api/endpoints';
import type { Job } from '../../api/types';
import { makeProxy, makeUpProxy } from '../../test/fixtures';
import { ProxyPage } from './ProxyPage';

vi.mock('../../api/endpoints');
const e = vi.mocked(endpoints);

const job = (id: number, proxy_id: number): Job => ({
  id, proxy_id, kind: 'availability', status: 'pending', source: 'manual',
  due_at: '', created_at: '', started_at: null, finished_at: null,
});

function mount() {
  render(
    <MemoryRouter>
      <ProxyPage />
    </MemoryRouter>,
  );
  return userEvent.setup();
}

const proxies = () => [
  makeUpProxy({ id: 1, name: 'Alpha', host: 'alpha.lan' }),
  makeProxy({ id: 2, name: 'Bravo', host: 'bravo.lan', status: { state: 'down', checked_at: new Date().toISOString(), exit_ip: '', endpoint: 'ipify', duration_ms: 5, failure: { code: 'auth_failed', message: 'The proxy rejected the credentials.' } } }),
  makeProxy({ id: 3, name: 'Charlie', host: 'charlie.lan', tags: ['lab'] }),
];

describe('ProxyPage', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    e.listProxies.mockResolvedValue({ proxies: proxies(), limit: 100, geo_configured: true });
    e.enqueueChecks.mockResolvedValue({ new: [job(1, 1)], existing: [], missing_proxy_ids: [] });
  });

  it('lists proxies and summarizes their states in words', async () => {
    mount();
    expect(await screen.findByRole('link', { name: 'Alpha' })).toBeInTheDocument();
    expect(screen.getByText(/3 of 100 proxies: 1 up, 1 down, 0 indeterminate, 1 unknown/)).toBeInTheDocument();
  });

  it('checks all proxies when nothing is selected', async () => {
    const user = mount();
    await screen.findByRole('link', { name: 'Alpha' });
    await user.click(screen.getByRole('button', { name: 'Check all proxies' }));
    await waitFor(() => expect(e.enqueueChecks).toHaveBeenCalledWith('availability', { all: true }));
    expect(await screen.findByRole('status')).toHaveTextContent('Queued 1 availability check.');
  });

  it('tests the speed of the selected proxies only', async () => {
    const user = mount();
    await screen.findByRole('link', { name: 'Alpha' });
    await user.click(screen.getByRole('checkbox', { name: 'Select Alpha' }));
    await user.click(screen.getByRole('checkbox', { name: 'Select Charlie' }));
    expect(screen.getByRole('button', { name: 'Check selected (2)' })).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Test speed of selected (2)' }));
    await waitFor(() => expect(e.enqueueChecks).toHaveBeenCalledWith('speed', { proxyIds: expect.arrayContaining([1, 3]) }));
    expect(e.enqueueChecks.mock.calls[0]![1]).toEqual({ proxyIds: expect.not.arrayContaining([2]) });
  });

  it('says when checks were already queued', async () => {
    e.enqueueChecks.mockResolvedValue({ new: [job(9, 3)], existing: [job(1, 1), job(2, 2)], missing_proxy_ids: [] });
    const user = mount();
    await screen.findByRole('link', { name: 'Alpha' });
    await user.click(screen.getByRole('button', { name: 'Check all proxies' }));
    expect(await screen.findByRole('status')).toHaveTextContent('Queued 1 availability check. 2 already queued or running.');
  });

  it('shows why a check could not be queued', async () => {
    e.enqueueChecks.mockRejectedValue(new ApiError(503, 'unavailable', 'Checks are not available.'));
    const user = mount();
    await screen.findByRole('link', { name: 'Alpha' });
    await user.click(screen.getByRole('button', { name: 'Check all proxies' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Checks are not available.');
  });

  it('searches and filters by state', async () => {
    const user = mount();
    await screen.findByRole('link', { name: 'Alpha' });
    await user.type(screen.getByRole('searchbox', { name: 'Search proxies' }), 'lab');
    expect(screen.queryByRole('link', { name: 'Alpha' })).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Charlie' })).toBeInTheDocument();
    await user.clear(screen.getByRole('searchbox', { name: 'Search proxies' }));
    await user.selectOptions(screen.getByLabelText('Filter by state'), 'down');
    expect(screen.getByRole('link', { name: 'Bravo' })).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Alpha' })).not.toBeInTheDocument();
    await user.type(screen.getByRole('searchbox', { name: 'Search proxies' }), 'zzz');
    expect(screen.getByText('No proxy matches these filters.')).toBeInTheDocument();
  });

  const names = () => screen.getAllByRole('link').filter((a) => a.className === 'proxy-name').map((a) => a.textContent);
  const header = (name: string) => screen.getByRole('button', { name: new RegExp(`^${name}`) });

  it('sorts by a column header, ascending first and descending on the second click', async () => {
    const user = mount();
    await screen.findByRole('link', { name: 'Alpha' });
    expect(names()).toEqual(['Alpha', 'Bravo', 'Charlie']);
    expect(header('Proxy').closest('th')).toHaveAttribute('aria-sort', 'ascending');
    await user.click(header('Proxy'));
    expect(names()).toEqual(['Charlie', 'Bravo', 'Alpha']);
    expect(header('Proxy').closest('th')).toHaveAttribute('aria-sort', 'descending');
    await user.click(header('Status'));
    expect(names()).toEqual(['Bravo', 'Charlie', 'Alpha']);
    expect(header('Status').closest('th')).toHaveAttribute('aria-sort', 'ascending');
    expect(header('Proxy').closest('th')).not.toHaveAttribute('aria-sort');
    await user.click(header('Status'));
    expect(names()).toEqual(['Alpha', 'Charlie', 'Bravo']);
  });

  it('offers the same sorting as a list and a direction button on narrow screens', async () => {
    const user = mount();
    await screen.findByRole('link', { name: 'Alpha' });
    await user.selectOptions(screen.getByLabelText('Sort by'), 'status');
    expect(names()).toEqual(['Bravo', 'Charlie', 'Alpha']);
    await user.click(screen.getByRole('button', { name: /Ascending order/ }));
    expect(names()).toEqual(['Alpha', 'Charlie', 'Bravo']);
  });

  const menu = async (user: ReturnType<typeof mount>, proxy: string, item: 'Edit' | 'Delete') => {
    await user.click(screen.getByRole('button', { name: `More actions for ${proxy}` }));
    await user.click(screen.getByRole('menuitem', { name: item }));
  };

  it('asks before deleting and then deletes', async () => {
    e.deleteProxy.mockResolvedValue(undefined);
    const user = mount();
    await screen.findByRole('link', { name: 'Alpha' });
    await menu(user, 'Bravo', 'Delete');
    const dialog = screen.getByRole('dialog', { name: 'Delete proxy' });
    expect(dialog).toHaveTextContent('Delete "Bravo"?');
    expect(e.deleteProxy).not.toHaveBeenCalled();
    await user.click(within(dialog).getByRole('button', { name: 'Delete proxy' }));
    await waitFor(() => expect(e.deleteProxy).toHaveBeenCalledWith(2));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
  });

  it('does not delete when the confirmation is cancelled', async () => {
    const user = mount();
    await screen.findByRole('link', { name: 'Alpha' });
    await menu(user, 'Bravo', 'Delete');
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(e.deleteProxy).not.toHaveBeenCalled();
  });

  it('deletes several selected proxies after one confirmation', async () => {
    e.deleteProxy.mockResolvedValue(undefined);
    const user = mount();
    await screen.findByRole('link', { name: 'Alpha' });
    await user.click(screen.getByRole('checkbox', { name: 'Select all proxies' }));
    await user.click(screen.getByRole('button', { name: 'Delete selected (3)' }));
    await user.click(screen.getByRole('button', { name: 'Delete 3 proxies' }));
    await waitFor(() => expect(e.deleteProxy).toHaveBeenCalledTimes(3));
  });

  it('keeps showing the last data when a refresh fails, and says so', async () => {
    e.listProxies.mockResolvedValueOnce({ proxies: proxies(), limit: 100, geo_configured: true });
    e.listProxies.mockRejectedValue(new ApiError(0, 'network', 'Cannot reach the server. Check your connection and try again.'));
    const user = mount();
    await screen.findByRole('link', { name: 'Alpha' });
    // Any action refreshes the list; the failure must not blank the table.
    await user.click(screen.getByRole('button', { name: 'Check all proxies' }));
    expect(await screen.findByText(/Showing the last loaded data\./)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Alpha' })).toBeInTheDocument();
  });

  it('blocks adding when the limit is reached', async () => {
    e.listProxies.mockResolvedValue({ proxies: proxies(), limit: 3, geo_configured: true });
    mount();
    await screen.findByRole('link', { name: 'Alpha' });
    expect(screen.getByRole('button', { name: 'Add proxy' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Import list' })).toBeDisabled();
    expect(screen.getByText(/limit of 3 proxies is reached/)).toBeInTheDocument();
  });

  it('opens the add, edit and import dialogs', async () => {
    const user = mount();
    await screen.findByRole('link', { name: 'Alpha' });
    await user.click(screen.getByRole('button', { name: 'Add proxy' }));
    expect(screen.getByRole('dialog', { name: 'Add proxy' })).toBeInTheDocument();
    await user.keyboard('{Escape}');
    await menu(user, 'Alpha', 'Edit');
    expect(screen.getByRole('dialog', { name: 'Edit proxy' })).toBeInTheDocument();
    await user.keyboard('{Escape}');
    await user.click(screen.getByRole('button', { name: 'Import list' }));
    expect(screen.getByRole('dialog', { name: 'Import list' })).toBeInTheDocument();
  });
});
