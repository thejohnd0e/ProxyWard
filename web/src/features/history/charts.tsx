import { useEffect, useId, useRef, useState } from 'react';
import type { AvailabilityPoint } from '../../api/types';
import { formatDateTime } from '../../lib/format';

/** Index ranges of consecutive non-null values. A null always ends a run. */
export function buildSegments(values: Array<number | null>): number[][] {
  const out: number[][] = [];
  let run: number[] = [];
  values.forEach((v, i) => {
    if (v === null) {
      if (run.length > 0) out.push(run);
      run = [];
    } else {
      run.push(i);
    }
  });
  if (run.length > 0) out.push(run);
  return out;
}

export interface Series {
  name: string;
  values: Array<number | null>;
}

interface LineChartProps {
  title: string;
  unit: string;
  times: string[];
  series: Series[];
  height?: number;
}

const DEFAULT_WIDTH = 720;
const PAD = { top: 12, right: 12, bottom: 28, left: 46 };

/** Width of the element in CSS pixels, so the SVG is drawn at its real size and text is not stretched. */
function useWidth(): [React.RefObject<HTMLDivElement | null>, number] {
  const ref = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(DEFAULT_WIDTH);
  useEffect(() => {
    const el = ref.current;
    if (!el || typeof ResizeObserver === 'undefined') return;
    const apply = () => {
      const w = Math.floor(el.clientWidth);
      if (w > 0) setWidth(w);
    };
    apply();
    const observer = new ResizeObserver(apply);
    observer.observe(el);
    return () => observer.disconnect();
  }, []);
  return [ref, width];
}

function niceMax(max: number): number {
  if (max <= 0) return 1;
  const pow = Math.pow(10, Math.floor(Math.log10(max)));
  const n = max / pow;
  const step = n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10;
  return step * pow;
}

function stats(series: Series[]): { all: number[]; max: number } {
  const all = series.flatMap((s) => s.values.filter((v): v is number => v !== null));
  return { all, max: all.length ? Math.max(...all) : 0 };
}

/**
 * Line chart with honest gaps: intervals without a value break the line, and
 * the scale starts at zero so heights are comparable. Exact values are in the
 * history table next to it.
 */
export function LineChart({ title, unit, times, series, height = 180 }: LineChartProps) {
  const uid = useId();
  const [wrap, W] = useWidth();
  const n = times.length;
  const { all, max } = stats(series);
  const top = niceMax(max);
  const plotW = W - PAD.left - PAD.right;
  const plotH = height - PAD.top - PAD.bottom;
  const x = (i: number) => PAD.left + (n <= 1 ? plotW / 2 : (i / (n - 1)) * plotW);
  const y = (v: number) => PAD.top + plotH - (v / top) * plotH;

  const withData = Math.max(0, ...series.map((s) => s.values.filter((v) => v !== null).length));
  const range = all.length ? `${Math.min(...all).toFixed(1)} to ${Math.max(...all).toFixed(1)} ${unit}` : '';
  const label = all.length
    ? `${title}: ${withData} of ${n} intervals have samples, from ${range}.`
    : `${title}: no samples in this period.`;

  const ticks = [0, top / 2, top];
  const wanted = W < 480 ? [0, n - 1] : [0, Math.floor((n - 1) / 2), n - 1];
  const labelAt = wanted.filter((i, idx, a) => n > 0 && a.indexOf(i) === idx);

  return (
    <figure className="chart">
      <figcaption>
        <span>
          <strong>{title}</strong> <span className="muted">({unit})</span>
        </span>
        <span className="chart__legend">
          {series.map((s, i) => (
            <span key={s.name} className={`legend legend--${i}`}>
              <span className="legend__swatch" aria-hidden="true" />
              {s.name}
            </span>
          ))}
        </span>
      </figcaption>
      <div ref={wrap} className="chart__wrap">
      <svg width={W} height={height} viewBox={`0 0 ${W} ${height}`} role="img" aria-label={label} className="chart__svg">
        {ticks.map((t) => (
          <g key={t}>
            <line x1={PAD.left} x2={W - PAD.right} y1={y(t)} y2={y(t)} className="chart__grid" />
            <text x={PAD.left - 6} y={y(t) + 4} textAnchor="end" className="chart__tick">
              {t % 1 === 0 ? t : t.toFixed(1)}
            </text>
          </g>
        ))}
        {labelAt.map((i) => (
          <text key={i} x={x(i)} y={height - 8} textAnchor={i === 0 ? 'start' : i === n - 1 ? 'end' : 'middle'} className="chart__tick">
            {formatDateTime(times[i])}
          </text>
        ))}
        {series.map((s, si) =>
          buildSegments(s.values).map((seg, k) =>
            seg.length > 1 ? (
              <path
                key={`${uid}-${si}-${k}`}
                data-segment=""
                className={`chart__line chart__line--${si}`}
                d={seg.map((i, j) => `${j === 0 ? 'M' : 'L'}${x(i).toFixed(1)} ${y(s.values[i] as number).toFixed(1)}`).join(' ')}
              />
            ) : null,
          ),
        )}
        {series.map((s, si) =>
          s.values.map((v, i) => {
            if (v === null) return null;
            const lone = (s.values[i - 1] ?? null) === null && (s.values[i + 1] ?? null) === null;
            return (
              <circle key={`${si}-${i}`} data-sample={lone ? 'lone' : 'point'} cx={x(i)} cy={y(v)} r={lone ? 3.5 : 2} className={`chart__dot chart__dot--${si}`}>
                <title>
                  {`${s.name}: ${v.toFixed(1)} ${unit}, ${formatDateTime(times[i])}`}
                </title>
              </circle>
            );
          }),
        )}
      </svg>
      </div>
      {all.length === 0 ? <p className="chart__empty">No samples in this period.</p> : null}
    </figure>
  );
}

type CellKind = 'up' | 'down' | 'indeterminate' | 'empty';

function cellKind(p: AvailabilityPoint): CellKind {
  if (p.samples === 0) return 'empty';
  if (p.down > 0) return 'down';
  if (p.up > 0) return 'up';
  return 'indeterminate';
}

/** One cell per interval: down wins over up, and intervals without checks stay blank. */
export function StateStrip({ points }: { points: AvailabilityPoint[] }) {
  const total = points.reduce((acc, p) => ({ up: acc.up + p.up, down: acc.down + p.down, ind: acc.ind + p.indeterminate }), { up: 0, down: 0, ind: 0 });
  const empty = points.filter((p) => p.samples === 0).length;
  const label = `Availability: ${total.up} up, ${total.down} down, ${total.ind} indeterminate; ${empty} ${empty === 1 ? 'interval' : 'intervals'} without checks.`;
  return (
    <figure className="chart">
      <figcaption>
        <strong>Availability</strong>
        <span className="chart__legend">
          <span className="legend legend--up">
            <span className="legend__swatch" aria-hidden="true" />
            Up
          </span>
          <span className="legend legend--down">
            <span className="legend__swatch" aria-hidden="true" />
            Down
          </span>
          <span className="legend legend--indeterminate">
            <span className="legend__swatch" aria-hidden="true" />
            Indeterminate
          </span>
        </span>
      </figcaption>
      <div className="strip" role="img" aria-label={label}>
        {points.map((p, i) => {
          const kind = cellKind(p);
          return (
            <span
              key={i}
              data-cell={kind}
              className={`strip__cell strip__cell--${kind}`}
              title={`${formatDateTime(p.t)}: ${kind === 'empty' ? 'no checks' : `${p.up} up, ${p.down} down, ${p.indeterminate} indeterminate`}`}
            />
          );
        })}
      </div>
    </figure>
  );
}
