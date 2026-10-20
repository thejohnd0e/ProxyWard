import type { Failure } from '../api/types';
import { formatMbps } from '../lib/format';

interface Props {
  mbps: number | null;
  lowConfidence?: boolean;
  failure?: Failure | null;
}

/**
 * A measured rate. A missing rate is a dash with the reason available, never
 * "0 Mbps": zero would claim a measurement that did not happen.
 */
export function SpeedValue({ mbps, lowConfidence = false, failure = null }: Props) {
  if (mbps === null) {
    return (
      <span className="speed speed--missing" title={failure?.message}>
        {formatMbps(null)}
        {failure ? <span className="visually-hidden"> Failed: {failure.message}</span> : null}
      </span>
    );
  }
  return (
    <span className="speed">
      {formatMbps(mbps)}
      {lowConfidence ? (
        <span className="speed__note" title="The sample was shorter than 250 ms, so the rate is only a rough indication.">
          {' '}
          Low confidence
        </span>
      ) : null}
    </span>
  );
}
