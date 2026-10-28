import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { RowMenu } from './RowMenu';

function setup() {
  const onEdit = vi.fn();
  const onDelete = vi.fn();
  render(
    <div>
      <button type="button">Elsewhere</button>
      <RowMenu name="Alpha" paused={false} onToggleMonitoring={vi.fn()} onEdit={onEdit} onDelete={onDelete} />
    </div>,
  );
  return { onEdit, onDelete, user: userEvent.setup(), more: screen.getByRole('button', { name: 'More actions for Alpha' }) };
}

describe('RowMenu', () => {
  it('is closed until opened and moves focus into the menu', async () => {
    const { user, more } = setup();
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
    await user.click(more);
    expect(screen.getByRole('menu', { name: 'Actions for Alpha' })).toBeInTheDocument();
    expect(screen.getByRole('menuitem', { name: 'Edit' })).toHaveFocus();
  });

  it('closes on Escape and returns focus to the button', async () => {
    const { user, more } = setup();
    await user.click(more);
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
    expect(more).toHaveFocus();
  });

  it('closes on a click outside, on a second click of the button and on Tab', async () => {
    const { user, more } = setup();
    await user.click(more);
    await user.click(screen.getByRole('button', { name: 'Elsewhere' }));
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
    await user.click(more);
    await user.click(more);
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
    await user.click(more);
    await user.keyboard('{Tab}');
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
  });

  it('moves between the items with the arrow keys and activates with Enter', async () => {
    const { user, more, onDelete, onEdit } = setup();
    await user.click(more);
    await user.keyboard('{ArrowDown}');
    expect(screen.getByRole('menuitem', { name: 'Pause' })).toHaveFocus();
    await user.keyboard('{ArrowDown}');
    expect(screen.getByRole('menuitem', { name: 'Delete' })).toHaveFocus();
    await user.keyboard('{ArrowDown}');
    expect(screen.getByRole('menuitem', { name: 'Edit' })).toHaveFocus();
    await user.keyboard('{ArrowUp}{Enter}');
    expect(onDelete).toHaveBeenCalledTimes(1);
    expect(onEdit).not.toHaveBeenCalled();
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
  });
});
