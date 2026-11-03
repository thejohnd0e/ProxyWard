import { expect, test } from '@playwright/test';

test('recovers from a settings server error with an explicit retry', async ({ page }) => {
  let requests = 0;
  let failureMode: 'server-error' | 'offline' | 'success' = 'server-error';
  await page.route('**/api/settings', async (route) => {
    requests += 1;
    if (failureMode === 'server-error') {
      await route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: { code: 'server_error', message: 'Settings are temporarily unavailable.' } }) });
      return;
    }
    if (failureMode === 'offline') {
      await route.abort('failed');
      return;
    }
    await route.continue();
  });

  await page.goto('/');
  await expect(page.getByRole('navigation', { name: 'Main' })).toBeVisible();
  await page.getByRole('button', { name: 'Settings' }).click();
  const dialog = page.getByRole('dialog', { name: 'Settings' });
  await expect(dialog.getByRole('alert')).toContainText('Settings are temporarily unavailable.');
  failureMode = 'offline';
  await dialog.getByRole('button', { name: 'Retry settings' }).click();
  await expect(dialog.getByRole('alert')).toContainText('Cannot reach the server.');
  failureMode = 'success';
  await dialog.getByRole('button', { name: 'Retry settings' }).click();
  await expect(dialog.getByRole('region', { name: 'Monitoring' })).toBeVisible();
  expect(requests).toBeGreaterThanOrEqual(3);
});
