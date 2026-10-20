import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '../../api/client';
import * as endpoints from '../../api/endpoints';
import type { ImportPreview } from '../../api/types';
import { ImportDialog } from './ImportDialog';

vi.mock('../../api/endpoints');

const preview = vi.mocked(endpoints.previewImport);
const doImport = vi.mocked(endpoints.importProxies);

const result: ImportPreview = {
  existing_count: 3,
  limit: 100,
  importable_count: 2,
  rows: [
    { line: 1, masked: 'http://alice:***@10.0.0.1:3128', valid: true, duplicate: false, error_code: '' },
    { line: 2, masked: 'http://10.0.0.2:8080', valid: true, duplicate: true, error_code: '' },
    { line: 4, masked: '10.0.0.3:***', valid: false, duplicate: false, error_code: 'ambiguous_format' },
    { line: 5, masked: 'ftp://10.0.0.4:21', valid: false, duplicate: false, error_code: 'invalid_scheme' },
    { line: 6, masked: 'socks5://10.0.0.5:1080', valid: true, duplicate: false, error_code: '' },
  ],
};

function open() {
  const onClose = vi.fn();
  const onImported = vi.fn();
  render(<ImportDialog onClose={onClose} onImported={onImported} />);
  return { onClose, onImported, user: userEvent.setup() };
}

async function previewText(user: ReturnType<typeof userEvent.setup>, text = 'anything') {
  await user.type(screen.getByLabelText('Proxy list'), text);
  await user.click(screen.getByRole('button', { name: 'Preview' }));
  await screen.findByRole('table');
}

describe('ImportDialog', () => {
  beforeEach(() => {
    preview.mockReset();
    doImport.mockReset();
    preview.mockResolvedValue(result);
    doImport.mockResolvedValue({ imported: 2, ids: [10, 11] });
  });

  it('cannot preview an empty list', () => {
    open();
    expect(screen.getByRole('button', { name: 'Preview' })).toBeDisabled();
  });

  it('sends the text and the default protocol for the preview', async () => {
    const { user } = open();
    await user.selectOptions(screen.getByLabelText('Default protocol'), 'socks5');
    await previewText(user, '10.0.0.1:1080');
    expect(preview).toHaveBeenCalledWith('10.0.0.1:1080', 'socks5');
  });

  it('shows each row with a plain-English result and preselects the importable ones', async () => {
    const { user } = open();
    await previewText(user);
    const rows = within(screen.getByRole('table')).getAllByRole('row').slice(1);
    expect(rows).toHaveLength(5);

    expect(within(rows[0]!).getByRole('checkbox', { name: 'Import line 1' })).toBeChecked();
    expect(within(rows[0]!).getByText('Ready to import')).toBeInTheDocument();
    expect(within(rows[0]!).getByText('http://alice:***@10.0.0.1:3128')).toBeInTheDocument();

    expect(within(rows[1]!).getByRole('checkbox', { name: 'Import line 2' })).not.toBeChecked();
    expect(within(rows[1]!).getByRole('checkbox', { name: 'Import line 2' })).toBeDisabled();
    expect(within(rows[1]!).getByText(/already added or repeated/i)).toBeInTheDocument();

    expect(within(rows[2]!).getByRole('checkbox', { name: 'Import line 4' })).toBeDisabled();
    expect(within(rows[2]!).getByText(/Write credentials as scheme:\/\/user:password@host:port/)).toBeInTheDocument();
    expect(within(rows[3]!).getByText(/Use http, https or socks5/)).toBeInTheDocument();

    expect(screen.getByRole('button', { name: 'Import 2 proxies' })).toBeEnabled();
    expect(screen.getByText(/3 of 100 proxies are used/)).toBeInTheDocument();
  });

  it('imports only the selected rows by line number', async () => {
    const { user, onImported, onClose } = open();
    await previewText(user, 'the pasted text');
    await user.click(screen.getByRole('checkbox', { name: 'Import line 6' }));
    expect(screen.getByRole('button', { name: 'Import 1 proxy' })).toBeEnabled();
    await user.click(screen.getByRole('button', { name: 'Import 1 proxy' }));
    await waitFor(() => expect(doImport).toHaveBeenCalledTimes(1));
    expect(doImport).toHaveBeenCalledWith('the pasted text', 'http', [1]);
    expect(onImported).toHaveBeenCalledWith(2);
    expect(onClose).not.toHaveBeenCalledWith(expect.anything());
  });

  it('can select and clear all importable rows at once', async () => {
    const { user } = open();
    await previewText(user);
    await user.click(screen.getByRole('checkbox', { name: 'Select all importable rows' }));
    expect(screen.getByRole('button', { name: 'Import 0 proxies' })).toBeDisabled();
    await user.click(screen.getByRole('checkbox', { name: 'Select all importable rows' }));
    expect(screen.getByRole('button', { name: 'Import 2 proxies' })).toBeEnabled();
  });

  it('blocks an import that would exceed the limit', async () => {
    preview.mockResolvedValue({ ...result, existing_count: 99 });
    const { user } = open();
    await previewText(user);
    expect(screen.getByRole('alert')).toHaveTextContent(/exceed the limit of 100/);
    expect(screen.getByRole('button', { name: 'Import 2 proxies' })).toBeDisabled();
    await user.click(screen.getByRole('checkbox', { name: 'Import line 6' }));
    expect(screen.getByRole('button', { name: 'Import 1 proxy' })).toBeEnabled();
  });

  it('discards the preview when the text changes so a stale selection cannot be imported', async () => {
    const { user } = open();
    await previewText(user);
    await user.type(screen.getByLabelText('Proxy list'), ' more');
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Import \d/ })).not.toBeInTheDocument();
  });

  it('tells the user when nothing in the list can be imported', async () => {
    preview.mockResolvedValue({ ...result, importable_count: 0, rows: result.rows.filter((r) => !r.valid) });
    const { user } = open();
    await previewText(user);
    expect(screen.getByText(/Nothing in this list can be imported/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Import 0 proxies' })).toBeDisabled();
  });

  it('explains server errors in plain words', async () => {
    const { user } = open();
    await user.type(screen.getByLabelText('Proxy list'), 'x');
    preview.mockRejectedValueOnce(new ApiError(422, 'too_many_lines', 'The list has too many lines.'));
    await user.click(screen.getByRole('button', { name: 'Preview' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('more than 1,000 lines');
    preview.mockRejectedValueOnce(new ApiError(413, 'payload_too_large', 'Request body is too large.'));
    await user.click(screen.getByRole('button', { name: 'Preview' }));
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('256 KiB'));
  });

  it('keeps the dialog open and shows the reason when saving fails', async () => {
    doImport.mockRejectedValue(new ApiError(422, 'record_limit', 'At most 100 proxies can be stored.'));
    const { user, onImported } = open();
    await previewText(user);
    await user.click(screen.getByRole('button', { name: 'Import 2 proxies' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('At most 100 proxies');
    expect(onImported).not.toHaveBeenCalled();
    expect(screen.getByRole('table')).toBeInTheDocument();
  });

  it('never displays raw credentials', async () => {
    const { user } = open();
    await previewText(user, 'http://alice:RealSecret@10.0.0.1:3128');
    // The text area holds what the user typed; the preview shows only the server-masked form.
    expect(within(screen.getByRole('table')).queryByText(/RealSecret/)).not.toBeInTheDocument();
  });
});
