import { act, render } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useVisiblePolling } from './useVisiblePolling';

function Harness({ load, intervalMs, resetKey }: { load: (signal: AbortSignal) => Promise<void>; intervalMs: number; resetKey?: string }) {
  useVisiblePolling(load, intervalMs, resetKey);
  return null;
}

function setVisibility(state: 'visible' | 'hidden') {
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => state });
  document.dispatchEvent(new Event('visibilitychange'));
}

describe('useVisiblePolling', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    setVisibility('visible');
  });
  afterEach(() => {
    vi.useRealTimers();
    setVisibility('visible');
  });

  it('loads immediately and then on every interval', async () => {
    const load = vi.fn().mockResolvedValue(undefined);
    render(<Harness load={load} intervalMs={5000} />);
    await act(async () => {});
    expect(load).toHaveBeenCalledTimes(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    expect(load).toHaveBeenCalledTimes(2);
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(load).toHaveBeenCalledTimes(4);
  });

  it('never runs two loads at once', async () => {
    let running = 0;
    let maxRunning = 0;
    const load = vi.fn(async () => {
      running++;
      maxRunning = Math.max(maxRunning, running);
      await new Promise((r) => setTimeout(r, 12_000)); // slower than the interval
      running--;
    });
    render(<Harness load={load} intervalMs={5000} />);
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
    expect(maxRunning).toBe(1);
    expect(load.mock.calls.length).toBeGreaterThan(1);
  });

  it('does not poll while the tab is hidden and catches up when it is shown', async () => {
    const load = vi.fn().mockResolvedValue(undefined);
    render(<Harness load={load} intervalMs={5000} />);
    await act(async () => {});
    expect(load).toHaveBeenCalledTimes(1);
    act(() => setVisibility('hidden'));
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
    expect(load).toHaveBeenCalledTimes(1);
    act(() => setVisibility('visible'));
    await act(async () => {});
    expect(load).toHaveBeenCalledTimes(2);
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    expect(load).toHaveBeenCalledTimes(3);
  });

  it('aborts the running load and stops polling on unmount', async () => {
    let signal: AbortSignal | undefined;
    const load = vi.fn((s: AbortSignal) => {
      signal = s;
      return new Promise<void>(() => {}); // never settles
    });
    const view = render(<Harness load={load} intervalMs={5000} />);
    await act(async () => {});
    expect(signal?.aborted).toBe(false);
    view.unmount();
    expect(signal?.aborted).toBe(true);
    await vi.advanceTimersByTimeAsync(30_000);
    expect(load).toHaveBeenCalledTimes(1);
  });

  it('keeps polling after a failed load', async () => {
    const load = vi.fn().mockRejectedValueOnce(new Error('boom')).mockResolvedValue(undefined);
    render(<Harness load={load} intervalMs={5000} />);
    await act(async () => {});
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    expect(load).toHaveBeenCalledTimes(2);
  });

  it('loads again at once when the reset key changes and aborts the previous load', async () => {
    const signals: AbortSignal[] = [];
    const load = vi.fn((s: AbortSignal) => {
      signals.push(s);
      return new Promise<void>(() => {});
    });
    const view = render(<Harness load={load} intervalMs={5000} resetKey="a" />);
    await act(async () => {});
    expect(load).toHaveBeenCalledTimes(1);
    view.rerender(<Harness load={load} intervalMs={5000} resetKey="b" />);
    await act(async () => {});
    expect(load).toHaveBeenCalledTimes(2);
    expect(signals[0]?.aborted).toBe(true);
    expect(signals[1]?.aborted).toBe(false);
    view.rerender(<Harness load={load} intervalMs={5000} resetKey="b" />);
    await act(async () => {});
    expect(load).toHaveBeenCalledTimes(2);
  });
});
