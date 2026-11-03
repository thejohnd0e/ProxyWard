import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import * as endpoints from '../../api/endpoints';
import type { AvailabilityPoint, Chart, SpeedPoint } from '../../api/types';
import { makeProxy } from '../../test/fixtures';
import { ProxyDetail } from './ProxyDetail';

vi.mock('../../api/endpoints');
vi.mock('../history/HistoryTable', () => ({
  AvailabilityHistory: () => null,
  SpeedHistory: () => null,
}));
vi.mock('../history/charts', () => ({
  StateStrip: () => null,
  LineChart: ({ title }: { title: string }) => <h2>{title}</h2>,
}));

const e = vi.mocked(endpoints);

function mount() {
  render(
    <MemoryRouter initialEntries={['/proxies/1']}>
      <Routes>
        <Route path="/proxies/:id" element={<ProxyDetail />} />
      </Routes>
    </MemoryRouter>,
  );
  return userEvent.setup();
}

const emptyChart = <P,>(kind: 'availability' | 'speed'): Chart<P> => ({
  kind,
  from: '2026-11-03T00:00:00.000Z',
  to: '2026-11-04T00:00:00.000Z',
  interval_seconds: 900,
  points: [],
});

describe('ProxyDetail charts', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    e.getProxy.mockResolvedValue(makeProxy());
    e.availabilityChart.mockResolvedValue(emptyChart<AvailabilityPoint>('availability'));
    e.speedChart.mockResolvedValue(emptyChart<SpeedPoint>('speed'));
  });

  it('shows a chart failure independently and retries that chart', async () => {
    e.availabilityChart.mockRejectedValueOnce(new Error('offline'));
    e.speedChart.mockResolvedValueOnce(emptyChart<SpeedPoint>('speed'));
    e.speedChart.mockImplementationOnce(() => new Promise(() => {}));
    const user = mount();

    expect(await screen.findByRole('heading', { name: 'Speed' })).toBeInTheDocument();
    expect(await screen.findByRole('alert', { name: 'Availability chart unavailable' })).toHaveTextContent('Availability history is unavailable.');
    expect(screen.queryByRole('heading', { name: 'Latency of successful checks' })).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Retry availability chart' }));
    expect(await screen.findByRole('heading', { name: 'Latency of successful checks' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Speed' })).toBeInTheDocument();
    expect(screen.queryByRole('status', { name: 'Speed chart loading' })).not.toBeInTheDocument();
  });

  it('does not present a previous range chart as the selected range while the new request is pending', async () => {
    let resolveAvailability!: (chart: Chart<AvailabilityPoint>) => void;
    e.availabilityChart.mockImplementationOnce(() => Promise.resolve(emptyChart<AvailabilityPoint>('availability')));
    e.availabilityChart.mockImplementationOnce(() => new Promise((resolve) => { resolveAvailability = resolve; }));
    const user = mount();
    expect(await screen.findByRole('heading', { name: 'Latency of successful checks' })).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Last 7 days' }));
    await waitFor(() => expect(e.availabilityChart).toHaveBeenCalledTimes(2));
    expect(screen.queryByRole('heading', { name: 'Latency of successful checks' })).not.toBeInTheDocument();
    expect(screen.getByRole('status', { name: 'Availability chart loading' })).toBeInTheDocument();

    resolveAvailability(emptyChart<AvailabilityPoint>('availability'));
    expect(await screen.findByRole('heading', { name: 'Latency of successful checks' })).toBeInTheDocument();
  });
});
