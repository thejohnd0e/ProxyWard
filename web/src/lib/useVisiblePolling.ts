import { useEffect, useRef } from 'react';

/**
 * Calls load now and then every intervalMs, but only while the tab is visible.
 * Loads never overlap: the next one is scheduled after the previous finished.
 * A load still running when the component unmounts is aborted. When resetKey
 * changes (for example a filter), the running load is aborted and a fresh one
 * starts at once instead of waiting for the next interval.
 */
export function useVisiblePolling(load: (signal: AbortSignal) => Promise<void>, intervalMs: number, resetKey?: unknown): void {
  const latest = useRef(load);
  latest.current = load;

  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    let running = false;
    let stopped = false;

    const visible = () => document.visibilityState !== 'hidden';

    const schedule = () => {
      if (stopped || timer !== undefined || !visible()) return;
      timer = setTimeout(() => {
        timer = undefined;
        void tick();
      }, intervalMs);
    };

    const tick = async () => {
      if (stopped || running || !visible()) return;
      running = true;
      try {
        await latest.current(controller.signal);
      } catch {
        // The caller shows its own errors; polling simply continues.
      } finally {
        running = false;
        schedule();
      }
    };

    const onVisibility = () => {
      if (visible()) {
        if (timer !== undefined) {
          clearTimeout(timer);
          timer = undefined;
        }
        void tick();
      } else if (timer !== undefined) {
        clearTimeout(timer);
        timer = undefined;
      }
    };

    document.addEventListener('visibilitychange', onVisibility);
    void tick();

    return () => {
      stopped = true;
      controller.abort();
      if (timer !== undefined) clearTimeout(timer);
      document.removeEventListener('visibilitychange', onVisibility);
    };
  }, [intervalMs, resetKey]);
}
