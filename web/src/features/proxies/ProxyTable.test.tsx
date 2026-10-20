import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';
import type { Proxy } from '../../api/types';
import { makeProxy, makeUpProxy } from '../../test/fixtures';
import { ProxyTable } from './ProxyTable';

function setup(proxies: Proxy[], opts: { geoConfigured?: boolean; selected?: number[] } = {}) {
  const handlers = {
    onToggle: vi.fn(),
    onToggleAll: vi.fn(),
    onCheck: vi.fn(),
    onEdit: vi.fn(),
    onDelete: vi.fn(),
  };
  render(
    <MemoryRouter>
      <ProxyTable
        proxies={proxies}
        geoConfigured={opts.geoConfigured ?? true}
        selected={new Set(opts.selected ?? [])}
        {...handlers}
      />
    </MemoryRouter>,
  );
  return { ...handlers, user: userEvent.setup() };
}

const row = (name: string) => screen.getByRole('row', { name: new RegExp(name) });

describe('ProxyTable', () => {
  it('shows an unchecked proxy as Unknown with dashes, not as down or zero', () => {
    setup([makeProxy({ name: 'Fresh' })]);
    const r = row('Fresh');
    expect(within(r).getByText('Unknown', { selector: '.status' })).toBeInTheDocument();
    expect(within(r).queryByText('Down')).not.toBeInTheDocument();
    expect(within(r).queryByText(/0 Mbps/)).not.toBeInTheDocument();
    expect(within(r).getAllByText('—').length).toBeGreaterThanOrEqual(3);
    expect(within(r).getByText('Never checked')).toBeInTheDocument();
  });

  it('shows the details of a healthy proxy', () => {
    setup([makeUpProxy({ name: 'Healthy', tags: ['eu', 'lab'] })]);
    const r = row('Healthy');
    expect(within(r).getByText('Up', { selector: '.status' })).toBeInTheDocument();
    expect(within(r).getByText('124 ms')).toBeInTheDocument();
    expect(within(r).getByText('http://10.0.0.5:3128')).toBeInTheDocument();
    expect(within(r).getByText('198.51.100.7')).toBeInTheDocument();
    expect(within(r).getByText('Berlin, Germany')).toBeInTheDocument();
    expect(within(r).getByText(/AS64500/)).toBeInTheDocument();
    expect(within(r).getByText(/Example GmbH/)).toBeInTheDocument();
    expect(within(r).getByText('33.5 Mbps')).toBeInTheDocument();
    expect(within(r).getByText('75% of 4 checks')).toBeInTheDocument();
    expect(within(r).getByText('eu')).toBeInTheDocument();
    expect(within(r).getByText('lab')).toBeInTheDocument();
  });

  it('keeps a failed upload as a dash next to a good download', () => {
    setup([makeUpProxy({ name: 'Mixed' })]);
    const r = row('Mixed');
    const download = within(r).getByLabelText('Download speed');
    const upload = within(r).getByLabelText('Upload speed');
    expect(download).toHaveTextContent('33.5 Mbps');
    expect(upload).toHaveTextContent('—');
    expect(upload).toHaveTextContent('The response was not valid.');
  });

  it('writes down the reason when a proxy is down', () => {
    setup([
      makeProxy({
        name: 'Broken',
        status: {
          state: 'down',
          checked_at: new Date().toISOString(),
          exit_ip: '',
          endpoint: 'ipify',
          duration_ms: 12,
          failure: { code: 'auth_failed', message: 'The proxy rejected the credentials.' },
        },
      }),
    ]);
    const r = row('Broken');
    expect(within(r).getByText('Down', { selector: '.status' })).toBeInTheDocument();
    expect(within(r).getByText('The proxy rejected the credentials.')).toBeInTheDocument();
  });

  it('tells apart geolocation that is not configured, unknown and stale', () => {
    const { rerender } = render(<div />);
    rerender(<div />);
    setup([makeUpProxy({ name: 'NoGeo', geo: null })], { geoConfigured: false });
    expect(within(row('NoGeo')).getByText('Not configured')).toBeInTheDocument();
  });

  it('shows Unknown location when geolocation is configured but has no data', () => {
    setup([makeUpProxy({ name: 'NoData', geo: null })], { geoConfigured: true });
    expect(within(row('NoData')).getByText('Unknown')).toBeInTheDocument();
  });

  it('marks stale geolocation in words', () => {
    const p = makeUpProxy({ name: 'Old' });
    p.geo = { ...p.geo!, stale: true, status: 'stale' };
    setup([p]);
    expect(within(row('Old')).getByText(/stale/i)).toBeInTheDocument();
  });

  it('shows Unknown for missing geo fields instead of blanks', () => {
    const p = makeUpProxy({ name: 'Partial' });
    p.geo = { ...p.geo!, city: '', asn: null, organization: '' };
    setup([p]);
    const r = within(row('Partial'));
    expect(r.getByText('Germany')).toBeInTheDocument();
    expect(r.getByLabelText('Network')).toHaveTextContent('Unknown');
  });

  it('disables the action of a kind that is queued or running, and only that kind', () => {
    setup([
      makeProxy({ id: 1, name: 'Busy', active_jobs: [{ id: 5, kind: 'availability', status: 'running' }] }),
      makeProxy({ id: 2, name: 'Waiting', active_jobs: [{ id: 6, kind: 'speed', status: 'pending' }] }),
      makeProxy({ id: 3, name: 'Idle' }),
    ]);
    expect(screen.getByRole('button', { name: 'Check availability of Busy' })).toBeDisabled();
    expect(within(row('Busy')).getByText('Running')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Test speed of Busy' })).toBeEnabled();

    expect(screen.getByRole('button', { name: 'Test speed of Waiting' })).toBeDisabled();
    expect(within(row('Waiting')).getByText('Queued')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Check availability of Waiting' })).toBeEnabled();

    expect(screen.getByRole('button', { name: 'Check availability of Idle' })).toBeEnabled();
    expect(screen.getByRole('button', { name: 'Test speed of Idle' })).toBeEnabled();
  });

  it('reports the actions the user takes', async () => {
    const proxy = makeProxy({ id: 9, name: 'Target' });
    const { user, onCheck, onEdit, onDelete, onToggle, onToggleAll } = setup([proxy]);
    await user.click(screen.getByRole('button', { name: 'Check availability of Target' }));
    expect(onCheck).toHaveBeenCalledWith('availability', 9);
    await user.click(screen.getByRole('button', { name: 'Test speed of Target' }));
    expect(onCheck).toHaveBeenCalledWith('speed', 9);
    await user.click(screen.getByRole('button', { name: 'Edit Target' }));
    expect(onEdit).toHaveBeenCalledWith(proxy);
    await user.click(screen.getByRole('button', { name: 'Delete Target' }));
    expect(onDelete).toHaveBeenCalledWith(proxy);
    await user.click(screen.getByRole('checkbox', { name: 'Select Target' }));
    expect(onToggle).toHaveBeenCalledWith(9, true);
    await user.click(screen.getByRole('checkbox', { name: 'Select all proxies' }));
    expect(onToggleAll).toHaveBeenCalledWith(true);
  });

  it('reflects the selection and links to the detail page', () => {
    setup([makeProxy({ id: 4, name: 'Picked' }), makeProxy({ id: 5, name: 'Other' })], { selected: [4] });
    expect(screen.getByRole('checkbox', { name: 'Select Picked' })).toBeChecked();
    expect(screen.getByRole('checkbox', { name: 'Select Other' })).not.toBeChecked();
    expect(screen.getByRole('link', { name: 'Picked' })).toHaveAttribute('href', '/proxies/4');
  });

  it('shows an empty state that invites adding a proxy', () => {
    setup([]);
    expect(screen.getByText(/No proxies yet/)).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  it('never renders a password', () => {
    setup([makeProxy({ name: 'Secret holder', auth_enabled: true, username: 'alice', has_password: true })]);
    const r = row('Secret holder');
    expect(within(r).getByText(/alice/)).toBeInTheDocument();
    expect(within(r).getByText(/Password set/)).toBeInTheDocument();
  });
});
