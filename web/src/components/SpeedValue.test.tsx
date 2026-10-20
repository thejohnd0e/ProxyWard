import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { SpeedValue } from './SpeedValue';
import { StatusBadge } from './StatusBadge';

describe('SpeedValue', () => {
  it('does not show zero for unavailable speed', () => {
    render(<SpeedValue mbps={null} />);
    expect(screen.getByText('—')).toBeInTheDocument();
    expect(screen.queryByText('0 Mbps')).not.toBeInTheDocument();
    expect(screen.queryByText(/0\.00 Mbps/)).not.toBeInTheDocument();
  });

  it('explains a missing rate through the failure, in text', () => {
    render(<SpeedValue mbps={null} failure={{ code: 'timeout', message: 'The request timed out.' }} />);
    expect(screen.getByText(/The request timed out\./)).toBeInTheDocument();
  });

  it('shows a measured rate', () => {
    render(<SpeedValue mbps={31.4159} />);
    expect(screen.getByText('31.4 Mbps')).toBeInTheDocument();
    expect(screen.queryByText(/Low confidence/)).not.toBeInTheDocument();
  });

  it('marks low-confidence samples in words', () => {
    render(<SpeedValue mbps={5} lowConfidence />);
    expect(screen.getByText(/5\.00 Mbps/)).toBeInTheDocument();
    expect(screen.getByText(/Low confidence/)).toBeInTheDocument();
  });
});

describe('StatusBadge', () => {
  it.each([
    ['unknown', 'Unknown'],
    ['up', 'Up'],
    ['down', 'Down'],
    ['indeterminate', 'Indeterminate'],
  ] as const)('writes the %s state as a word', (state, label) => {
    render(<StatusBadge state={state} />);
    expect(screen.getByText(label)).toBeInTheDocument();
  });
});
