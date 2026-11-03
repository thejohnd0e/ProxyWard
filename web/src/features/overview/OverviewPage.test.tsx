import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '../../api/client';
import * as endpoints from '../../api/endpoints';
import { makeProxy, makeUpProxy } from '../../test/fixtures';
import { OverviewPage } from './OverviewPage';

vi.mock('../../api/endpoints');
const e = vi.mocked(endpoints);

const down = makeProxy({
  id: 2,
  name: 'Rejected gateway',
  status: {
    state: 'down',
    checked_at: new Date().toISOString(),
    exit_ip: '',
    endpoint: 'ipify',
    duration_ms: 8,
    failure: { code: 'auth_failed', message: 'The proxy rejected the credentials.' },
  },
});

function mount() {
  render(
    <MemoryRouter>
      <OverviewPage />
    </MemoryRouter>,
  );
}

describe('OverviewPage', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    e.listProxies.mockResolvedValue({
      proxies: [makeUpProxy({ id: 1, name: 'Office gateway' }), down],
      limit: 100,
      geo_configured: true,
    });
  });

  it('reconciles counts and lists actionable problems from live proxy state', async () => {
    mount();
    expect(await screen.findByRole('heading', { name: 'Overview' })).toBeInTheDocument();
    expect(await screen.findByText('2 proxies · 2 checked in the last 24 hours')).toBeInTheDocument();
    expect(screen.getByText('Observation coverage')).toBeInTheDocument();
    expect(screen.getByText('Needs attention')).toBeInTheDocument();
    expect(screen.getByText('The proxy rejected the credentials.')).toBeInTheDocument();
    expect(screen.getAllByRole('link', { name: 'Rejected gateway' })[0]).toHaveAttribute('href', '/proxies/2');
    expect(screen.getAllByRole('link', { name: 'Office gateway' })[0]).toHaveAttribute('href', '/proxies/1');
  });

  it('says plainly when there are no proxies yet', async () => {
    e.listProxies.mockResolvedValue({ proxies: [], limit: 100, geo_configured: false });
    mount();
    expect(await screen.findByText('No proxies yet. Add one to start collecting evidence.')).toBeInTheDocument();
  });

  it('keeps the last data and offers a retry after a failed refresh', async () => {
    e.listProxies.mockRejectedValueOnce(new ApiError(0, 'network', 'Cannot reach the server.'));
    e.listProxies.mockResolvedValue({ proxies: [down], limit: 100, geo_configured: true });
    mount();
    const user = userEvent.setup();
    expect(await screen.findByRole('alert')).toHaveTextContent('Cannot reach the server.');
    await user.click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByText('The proxy rejected the credentials.')).toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });
});
