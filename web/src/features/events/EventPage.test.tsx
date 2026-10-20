import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import * as endpoints from '../../api/endpoints';
import type { AppEvent } from '../../api/types';
import { makeProxy } from '../../test/fixtures';
import { EventPage, describeDelivery } from './EventPage';

vi.mock('../../api/endpoints');
const e = vi.mocked(endpoints);

const ev = (id: number, over: Partial<AppEvent> = {}): AppEvent => ({
  id,
  proxy_id: 1,
  proxy_name: 'Alpha',
  kind: 'proxy_down',
  message: `Proxy "Alpha" is down after 3 consecutive failures (last reason: auth_failed). #${id}`,
  created_at: '2026-10-20T10:00:00Z',
  delivery: { status: 'delivered', attempts: 0, last_error: '', delivered_at: '2026-10-20T10:00:15Z' },
  ...over,
});

function mount() {
  render(
    <MemoryRouter>
      <EventPage />
    </MemoryRouter>,
  );
  return userEvent.setup();
}

describe('describeDelivery', () => {
  it('states every delivery outcome in plain English', () => {
    expect(describeDelivery({ status: 'none', attempts: 0, last_error: '', delivered_at: null })).toBe('No notification');
    expect(describeDelivery({ status: 'pending', attempts: 0, last_error: '', delivered_at: null })).toBe('Waiting to send');
    expect(describeDelivery({ status: 'pending', attempts: 2, last_error: 'timeout', delivered_at: null })).toBe(
      'Waiting to retry, 2 attempts so far: Telegram did not answer in time',
    );
    expect(describeDelivery({ status: 'failed', attempts: 5, last_error: 'forbidden', delivered_at: null })).toBe(
      'Not sent after 5 attempts: The bot was blocked or cannot post to the chat',
    );
    expect(describeDelivery({ status: 'failed', attempts: 5, last_error: 'surprising', delivered_at: null })).toBe('Not sent after 5 attempts: Sending failed');
    expect(describeDelivery({ status: 'delivered', attempts: 1, last_error: '', delivered_at: new Date().toISOString() })).toMatch(/^Sent to Telegram /);
  });
});

describe('EventPage', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    e.listProxies.mockResolvedValue({ proxies: [makeProxy({ id: 1, name: 'Alpha' }), makeProxy({ id: 2, name: 'Bravo' })], limit: 100, geo_configured: false });
  });

  it('lists events with their delivery state, link to the proxy and a clear empty state', async () => {
    e.listEvents.mockResolvedValue({
      events: [
        ev(2, { kind: 'proxy_recovered', message: 'Proxy "Alpha" is up again.', delivery: { status: 'failed', attempts: 5, last_error: 'rate_limited', delivered_at: null } }),
        ev(1, { proxy_id: null, proxy_name: null }),
      ],
      next_cursor: null,
    });
    mount();
    const table = await screen.findByRole('table');
    const rows = within(table).getAllByRole('row').slice(1);
    expect(within(rows[0]!).getByText('Recovered')).toBeInTheDocument();
    expect(within(rows[0]!).getByRole('link', { name: 'Alpha' })).toHaveAttribute('href', '/proxies/1');
    expect(within(rows[0]!).getByText(/Not sent after 5 attempts: Telegram asked to slow down/)).toBeInTheDocument();
    expect(within(rows[1]!).getByText('Went down')).toBeInTheDocument();
    expect(within(rows[1]!).getByText('Deleted proxy')).toBeInTheDocument();
  });

  it('invites the user to wait when there are no events', async () => {
    e.listEvents.mockResolvedValue({ events: [], next_cursor: null });
    mount();
    expect(await screen.findByText('No events yet')).toBeInTheDocument();
  });

  it('applies the type and proxy filters', async () => {
    e.listEvents.mockResolvedValue({ events: [ev(1)], next_cursor: null });
    const user = mount();
    await screen.findByRole('table');
    await user.selectOptions(screen.getByLabelText('Event type'), 'proxy_recovered');
    await waitFor(() => expect(e.listEvents).toHaveBeenLastCalledWith(expect.objectContaining({ kinds: ['proxy_recovered'], proxyId: null }), expect.anything()));
    await user.selectOptions(await screen.findByRole('combobox', { name: 'Proxy' }), '2');
    await waitFor(() => expect(e.listEvents).toHaveBeenLastCalledWith(expect.objectContaining({ proxyId: 2 }), expect.anything()));
  });

  it('loads older events on demand without duplicating any', async () => {
    e.listEvents
      .mockResolvedValueOnce({ events: [ev(3), ev(2)], next_cursor: 'cur-1' })
      .mockResolvedValueOnce({ events: [ev(2), ev(1)], next_cursor: null });
    const user = mount();
    await screen.findByRole('table');
    await user.click(screen.getByRole('button', { name: 'Load more' }));
    await waitFor(() => expect(within(screen.getByRole('table')).getAllByRole('row')).toHaveLength(4)); // header + 3 events
    expect(e.listEvents).toHaveBeenLastCalledWith(expect.objectContaining({ cursor: 'cur-1' }));
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument();
  });

  it('shows errors in an alert', async () => {
    e.listEvents.mockRejectedValue(new Error('x'));
    mount();
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not load events.');
  });
});
