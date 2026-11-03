import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '../../api/client';
import * as endpoints from '../../api/endpoints';
import { ExportDialog } from './ExportDialog';

vi.mock('../../api/endpoints');
const e = vi.mocked(endpoints);

const proxies = [
  { id: 1, name: 'Alpha' },
  { id: 2, name: 'Bravo' },
];

let clicked: HTMLAnchorElement | null = null;
const createObjectURL = vi.fn(() => 'blob:export');
const revokeObjectURL = vi.fn();

beforeEach(() => {
  vi.resetAllMocks();
  clicked = null;
  Object.defineProperty(URL, 'createObjectURL', { value: createObjectURL, configurable: true, writable: true });
  Object.defineProperty(URL, 'revokeObjectURL', { value: revokeObjectURL, configurable: true, writable: true });
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
    clicked = this;
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

function mount(props: Partial<Parameters<typeof ExportDialog>[0]> = {}) {
  const onClose = vi.fn();
  const onRetryProxies = vi.fn();
  const view = render(
    <ExportDialog proxies={proxies} proxyError={null} onRetryProxies={onRetryProxies} onClose={onClose} {...props} />,
  );
  return { user: userEvent.setup(), onClose, onRetryProxies, ...view };
}

describe('ExportDialog', () => {
  it('starts with the last 24 hours and every proxy selected', () => {
    mount();
    const from = screen.getByLabelText('Start of the period') as HTMLInputElement;
    const to = screen.getByLabelText('End of the period') as HTMLInputElement;
    const span = new Date(to.value).getTime() - new Date(from.value).getTime();
    expect(Math.abs(span - 24 * 60 * 60 * 1000)).toBeLessThanOrEqual(60 * 60 * 1000);
    expect(screen.getByRole('checkbox', { name: 'Select all proxies' })).toBeChecked();
    expect(screen.getByRole('checkbox', { name: 'Alpha' })).toBeChecked();
    expect(screen.getByRole('checkbox', { name: 'Bravo' })).toBeChecked();
  });

  it('sends the chosen local period as UTC and only the selected proxies', async () => {
    e.exportData.mockResolvedValue(new Blob(['{}'], { type: 'application/json' }));
    const { user } = mount();
    const from = screen.getByLabelText('Start of the period');
    const to = screen.getByLabelText('End of the period');
    await user.clear(from);
    await user.type(from, '2026-04-30T09:00');
    await user.clear(to);
    await user.type(to, '2026-05-01T09:00');
    await user.click(screen.getByRole('checkbox', { name: 'Bravo' }));
    await user.click(screen.getByRole('button', { name: 'Download export' }));

    await waitFor(() => expect(e.exportData).toHaveBeenCalledTimes(1));
    const [input, signal] = e.exportData.mock.calls[0]!;
    expect(input.proxyIds).toEqual([1]);
    expect(new Date(input.from).toISOString()).toBe(new Date(2026, 3, 30, 9, 0).toISOString());
    expect(new Date(input.to).toISOString()).toBe(new Date(2026, 4, 1, 9, 0).toISOString());
    expect(signal).toBeDefined();
  });

  it('includes a result recorded at the moment the dialog opens', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-05-01T09:05:30.500Z'));
    try {
      mount();
      const to = screen.getByLabelText('End of the period') as HTMLInputElement;
      expect(new Date(to.value).getTime()).toBeGreaterThanOrEqual(Date.now());
    } finally {
      vi.useRealTimers();
    }
  });

  it('downloads the JSON and closes when the server answers', async () => {
    e.exportData.mockResolvedValue(new Blob(['{"schema_version":1}'], { type: 'application/json' }));
    const { user, onClose } = mount();
    await user.click(screen.getByRole('button', { name: 'Download export' }));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(createObjectURL).toHaveBeenCalledTimes(1);
    expect(clicked?.download).toBe('proxyward-export.json');
    expect(clicked?.href).toBe('blob:export');
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:export');
  });

  it('refuses an empty proxy selection and an invalid period', async () => {
    const { user } = mount();
    await user.click(screen.getByRole('checkbox', { name: 'Select all proxies' }));
    await user.click(screen.getByRole('button', { name: 'Download export' }));
    expect(screen.getByRole('alert')).toHaveTextContent('Select at least one proxy.');
    expect(e.exportData).not.toHaveBeenCalled();

    await user.click(screen.getByRole('checkbox', { name: 'Select all proxies' }));
    const from = screen.getByLabelText('Start of the period');
    const to = screen.getByLabelText('End of the period');
    await user.clear(from);
    await user.type(from, '2026-05-02T09:00');
    await user.clear(to);
    await user.type(to, '2026-05-01T09:00');
    await user.click(screen.getByRole('button', { name: 'Download export' }));
    expect(screen.getByRole('alert')).toHaveTextContent('The end of the period must be later than its start.');
    expect(e.exportData).not.toHaveBeenCalled();
  });

  it('shows a server refusal instead of saving a file', async () => {
    e.exportData.mockRejectedValue(
      new ApiError(413, 'export_too_large', 'The selected period and proxies hold more than 100000 records. Narrow the period or select fewer proxies.'),
    );
    const { user, onClose } = mount();
    await user.click(screen.getByRole('button', { name: 'Download export' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Narrow the period or select fewer proxies.');
    expect(createObjectURL).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: 'Download export' })).toBeEnabled();
  });

  it('does not offer an export while the proxy list is unavailable', async () => {
    const { user, onRetryProxies } = mount({ proxies: [], proxyError: 'Could not load proxies.' });
    expect(screen.getByRole('alert')).toHaveTextContent('Could not load proxies.');
    expect(screen.getByRole('button', { name: 'Download export' })).toBeDisabled();
    await user.click(screen.getByRole('button', { name: 'Try again' }));
    expect(onRetryProxies).toHaveBeenCalled();
  });

  it('aborts an export that is still running when the dialog closes', async () => {
    let resolve: (b: Blob) => void = () => undefined;
    e.exportData.mockReturnValue(new Promise<Blob>((r) => (resolve = r)));
    const { user, unmount } = mount();
    await user.click(screen.getByRole('button', { name: 'Download export' }));
    const signal = e.exportData.mock.calls[0]![1] as AbortSignal;
    unmount();
    expect(signal.aborted).toBe(true);
    resolve(new Blob(['{}']));
  });
});
