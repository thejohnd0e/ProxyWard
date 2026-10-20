import { expect, test } from '@playwright/test';
import { fixtures, setTheme, shot, signIn } from './helpers';

// These tests start signed out.
test.use({ storageState: { cookies: [], origins: [] } });

test.describe('authentication', () => {
  test('protected pages require a sign-in and a wrong password is explained', async ({ page, request }, info) => {
    const fx = await fixtures(request);
    await page.goto('/settings');
    await expect(page.getByRole('heading', { name: 'Proxy Checker' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Settings' })).toHaveCount(0);
    await shot(page, info, 'login');

    await page.getByLabel('Password').fill('definitely wrong');
    await page.getByRole('button', { name: 'Sign in' }).click();
    await expect(page.getByRole('alert')).toHaveText('The password is not correct.');

    await page.getByLabel('Password').fill(fx.admin_password);
    await page.getByRole('button', { name: 'Sign in' }).click();
    await expect(page.getByRole('navigation', { name: 'Main' })).toBeVisible();
  });

  test('the session survives a reload and ends on sign out', async ({ page, request }) => {
    const fx = await fixtures(request);
    await signIn(page, fx.admin_password);
    await page.reload();
    await expect(page.getByRole('navigation', { name: 'Main' })).toBeVisible();
    await page.getByRole('button', { name: 'Sign out' }).click();
    await expect(page.getByLabel('Password')).toBeVisible();
    await page.goto('/proxies');
    await expect(page.getByLabel('Password')).toBeVisible();
  });

  test('the theme choice is applied and remembered', async ({ page, request }) => {
    const fx = await fixtures(request);
    await signIn(page, fx.admin_password);
    await setTheme(page, 'Dark');
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
    await page.reload();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
    await setTheme(page, 'System');
    await expect(page.locator('html')).not.toHaveAttribute('data-theme', /.*/);
  });

  test('the server sends hardening headers and no framing', async ({ request }) => {
    const res = await request.get('/');
    const h = res.headers();
    expect(h['content-security-policy']).toContain("default-src 'self'");
    expect(h['x-frame-options']).toBe('DENY');
    expect(h['x-content-type-options']).toBe('nosniff');
  });
});
