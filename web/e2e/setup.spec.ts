import { expect, test } from '@playwright/test';
import { fixtures, signIn } from './helpers';

// Logs in once and stores the session. The server limits sign-ins to five a
// minute per client, so the other specs reuse this session instead of signing in.
export const STATE = 'e2e/.auth/state.json';

test('sign in once for the other specs', async ({ page, request }) => {
  const fx = await fixtures(request);
  await signIn(page, fx.admin_password);
  await expect(page.getByRole('link', { name: 'Proxies', exact: true })).toBeVisible();
  await page.context().storageState({ path: STATE });
});
