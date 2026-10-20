import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { AvailabilityPoint } from '../../api/types';
import { LineChart, StateStrip, buildSegments } from './charts';

const t = (i: number) => new Date(Date.UTC(2026, 9, 20, i)).toISOString();

describe('buildSegments', () => {
  it('splits runs of values at gaps instead of drawing through them', () => {
    expect(buildSegments([10, 20, null, 30, null, null, 40, 50])).toEqual([
      [0, 1],
      [3],
      [6, 7],
    ]);
  });
  it('returns nothing when there are no values', () => {
    expect(buildSegments([null, null])).toEqual([]);
    expect(buildSegments([])).toEqual([]);
  });
});

describe('LineChart', () => {
  const times = [0, 1, 2, 3, 4, 5].map(t);

  it('draws a separate line for each run and a dot for a lone sample', () => {
    const { container } = render(
      <LineChart title="Download speed" unit="Mbps" times={times} series={[{ name: 'Download', values: [10, 12, null, 30, null, 20] }]} />,
    );
    expect(container.querySelectorAll('path[data-segment]')).toHaveLength(1); // only [0,1] has two points
    expect(container.querySelectorAll('circle[data-sample]')).toHaveLength(4); // 2 + 1 lone + 1 lone
    expect(container.querySelectorAll('circle[data-sample="lone"]')).toHaveLength(2);
  });

  it('never plots a missing value as zero', () => {
    const { container } = render(<LineChart title="Upload speed" unit="Mbps" times={times} series={[{ name: 'Upload', values: [null, null, null, null, null, null] }]} />);
    expect(container.querySelectorAll('path[data-segment], circle[data-sample]')).toHaveLength(0);
    expect(screen.getByText('No samples in this period.')).toBeInTheDocument();
  });

  it('describes the data in text, including the gaps', () => {
    render(<LineChart title="Download speed" unit="Mbps" times={times} series={[{ name: 'Download', values: [10, null, null, 30, null, null] }]} />);
    const img = screen.getByRole('img', { name: /Download speed/ });
    expect(img).toHaveAccessibleName(/2 of 6 intervals have samples/);
    expect(img).toHaveAccessibleName(/10\.0.*30\.0 Mbps/);
  });

  it('supports several series', () => {
    const { container } = render(
      <LineChart title="Speed" unit="Mbps" times={times} series={[{ name: 'Download', values: [1, 2, 3, 4, 5, 6] }, { name: 'Upload', values: [2, 2, 2, null, 2, 2] }]} />,
    );
    expect(container.querySelectorAll('path[data-segment]')).toHaveLength(3);
    expect(screen.getAllByText('Download').length).toBeGreaterThan(0);
    expect(screen.getAllByText('Upload').length).toBeGreaterThan(0);
  });
});

function point(over: Partial<AvailabilityPoint>): AvailabilityPoint {
  return { t: t(0), samples: 0, up: 0, down: 0, indeterminate: 0, avg_duration_ms: null, ...over };
}

describe('StateStrip', () => {
  it('colours each interval by what happened and leaves empty intervals blank', () => {
    const points = [
      point({ t: t(0), samples: 2, up: 2 }),
      point({ t: t(1) }),
      point({ t: t(2), samples: 2, up: 1, down: 1 }),
      point({ t: t(3), samples: 1, indeterminate: 1 }),
    ];
    const { container } = render(<StateStrip points={points} />);
    const cells = Array.from(container.querySelectorAll('[data-cell]')).map((c) => c.getAttribute('data-cell'));
    expect(cells).toEqual(['up', 'empty', 'down', 'indeterminate']);
  });

  it('summarizes in words', () => {
    render(<StateStrip points={[point({ samples: 3, up: 2, down: 1 }), point({}), point({ samples: 1, up: 1 })]} />);
    expect(screen.getByRole('img')).toHaveAccessibleName(/3 up, 1 down, 0 indeterminate/);
    expect(screen.getByRole('img')).toHaveAccessibleName(/1 interval without checks/);
  });
});
