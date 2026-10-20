import type { State } from '../api/types';
import { stateLabel } from '../lib/format';

/** State as a word plus a dot: colour is never the only signal. */
export function StatusBadge({ state }: { state: State }) {
  return (
    <span className={`status status--${state}`}>
      <span className="status__dot" aria-hidden="true" />
      {stateLabel(state)}
    </span>
  );
}
